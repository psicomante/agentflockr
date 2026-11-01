// Package jsonl incrementally reads provider transcripts stored as JSON Lines.
package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Apply folds one complete JSON record into the session being parsed.
type Apply func(s *session.Session, record []byte) error

type entry struct {
	info    os.FileInfo
	offset  int64
	session session.Session
}

// Source scans transcript files under Roots. It is owned by one scan worker.
// Cached offsets permit incremental reads of active logs; unchanged historical
// transcripts are not parsed again. Files that need reading are read several
// at once, which mostly helps the first scan after startup.
type Source struct {
	Provider session.Provider
	Roots    []string
	// MaxDepth limits how many directory levels below a root are walked;
	// zero means unlimited.
	MaxDepth int
	// StaleAfter is passed to provider.Observe; zero means its default.
	StaleAfter time.Duration
	// Apply runs on several files at once, so it must write only to the
	// session it is given.
	Apply Apply
	// Workers is how many transcripts are read at once; zero means
	// runtime.GOMAXPROCS(0), as reading and decoding JSON is CPU-bound once
	// the files are in the page cache.
	Workers int
	cache   map[string]entry
}

func (f *Source) Scan(ctx context.Context, now time.Time) provider.Result {
	result := provider.Result{Provider: f.Provider}
	if f.cache == nil {
		f.cache = map[string]entry{}
	}
	var paths []string
	for _, root := range f.Roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				// One unreadable folder must not fail the scan of every
				// session of the agent.
				if d != nil && d.IsDir() && !errors.Is(err, context.Canceled) {
					result.Warnings = append(result.Warnings, err.Error())
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if f.MaxDepth > 0 {
					rel, _ := filepath.Rel(root, path)
					if rel != "." && strings.Count(rel, string(os.PathSeparator))+1 > f.MaxDepth {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if filepath.Ext(path) == ".jsonl" {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			result.Err = err
			return result
		}
	}
	plans := make([]plan, len(paths))
	var jobs []int
	// planned is how many paths were looked at before the scan was cancelled.
	planned := len(paths)
	for i, path := range paths {
		if ctx.Err() != nil {
			planned = i
			break
		}
		info, err := os.Stat(path)
		if err != nil {
			plans[i].statErr = err
			continue
		}
		cached, ok := f.cache[path]
		if !ok || replaced(cached.info, info) {
			cached = entry{session: session.Session{Provider: f.Provider, Transcript: path, Status: session.Unknown}}
		}
		plans[i].info, plans[i].start = info, cached
		if changed(cached.info, info) {
			plans[i].read = true
			jobs = append(jobs, i)
		}
	}
	f.readAll(ctx, paths, plans, jobs)

	// Every finished read is cached, even past a cancellation, so the next
	// refresh carries on from there instead of reading it again.
	for _, i := range jobs {
		switch p := plans[i]; {
		case !p.done:
		case p.err != nil:
			delete(f.cache, paths[i])
		default:
			f.cache[paths[i]] = p.next
		}
	}
	// The result is merged in path order, and stops at the first path left
	// unread, so sessions and warnings are exactly what reading one file at a
	// time would give.
	seen := map[string]bool{}
	for i, path := range paths {
		p := plans[i]
		if i == planned || (p.read && !p.done) {
			result.Err = ctx.Err()
			return result
		}
		seen[path] = true
		if p.statErr != nil {
			result.Warnings = append(result.Warnings, p.statErr.Error())
			continue
		}
		current := p.start
		if p.read {
			result.Warnings = append(result.Warnings, p.warnings...)
			if p.err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", filepath.Base(path), p.err))
				continue
			}
			current = p.next
		}
		if current.session.ID != "" && filepath.IsAbs(current.session.Directory) {
			result.Sessions = append(result.Sessions, provider.Observe(current.session, now, f.StaleAfter))
		}
	}
	for path := range f.cache {
		if !seen[path] {
			delete(f.cache, path)
		}
	}
	return result
}

// plan is one path's part of a scan: what the cache had, whether the file
// needs reading, and, once read, what the read gave.
type plan struct {
	info    os.FileInfo
	statErr error
	start   entry // the cached state to read on from
	read    bool  // the file changed since it was cached
	done    bool  // the read finished
	next    entry
	// warnings are the records the read skipped, in file order; err means
	// the file could not be read, and it is dropped from the cache.
	warnings []string
	err      error
}

// replaced reports whether a cached read no longer describes the file: it is
// another file now, it shrank, or it was rewritten at the same size. Such a
// file is read again from the start.
func replaced(cached, info os.FileInfo) bool {
	return !os.SameFile(cached, info) || info.Size() < cached.Size() ||
		(info.Size() == cached.Size() && !info.ModTime().Equal(cached.ModTime()))
}

// changed reports whether a file needs reading: it was never read, or it
// changed size or time since.
func changed(cached, info os.FileInfo) bool {
	return cached == nil || info.Size() != cached.Size() || !info.ModTime().Equal(cached.ModTime())
}

// readAll reads the planned jobs, several at once when there are several.
// Workers write only their own plan; the cache is left to Scan. A cancelled
// context stops new reads from starting, and reads already running finish.
func (f *Source) readAll(ctx context.Context, paths []string, plans []plan, jobs []int) {
	run := func(i int) {
		p := &plans[i]
		p.next = p.start
		offset, skipped, err := Read(paths[i], p.start.offset, &p.next.session, f.Apply)
		p.next.offset, p.next.info = offset, p.info
		p.warnings, p.err, p.done = skipped, err, true
	}
	workers := f.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if len(jobs) < 2 || workers == 1 {
		for _, i := range jobs {
			if ctx.Err() != nil {
				return
			}
			run(i)
		}
		return
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(jobs)) {
		wg.Go(func() {
			for i := range next {
				run(i)
			}
		})
	}
	for _, i := range jobs {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
}

// Parse reads a whole transcript from the beginning.
func Parse(path string, p session.Provider, apply Apply) (session.Session, error) {
	s := session.Session{Provider: p, Transcript: path, Status: session.Unknown}
	// Skipped records are not reported: Parse is for one-off reads, such as a
	// preview, where a damaged record should only be left out.
	_, _, err := Read(path, 0, &s, apply)
	return s, err
}

// Read applies records from offset and returns an offset only through complete
// JSONL records. A concurrently appended final line is retried on the next
// scan, rather than losing its lifecycle event.
//
// A complete record that cannot be applied is skipped and reported in the
// returned warnings, and the offset moves past it. Stopping there would drop
// the whole session and re-read the file from the same point on every refresh.
func Read(path string, offset int64, s *session.Session, apply Apply) (int64, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, nil, err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return offset, nil, err
	}
	r := bufio.NewReaderSize(f, provider.ReadBufferSize)
	var line []byte
	var warnings []string
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > provider.MaxRecordSize {
			return offset, warnings, fmt.Errorf("transcript record exceeds %d MiB", provider.MaxRecordSize>>20)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			if len(bytes.TrimSpace(line)) > 0 && json.Valid(line) {
				if err := apply(s, line); err != nil {
					warnings = append(warnings, fmt.Sprintf("%s: skipped invalid record at byte %d: %v", filepath.Base(path), offset, err))
				}
				offset += int64(len(line))
			}
			return offset, warnings, nil
		}
		if err != nil {
			return offset, warnings, err
		}
		if len(bytes.TrimSpace(line)) > 0 {
			if err := apply(s, line); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: skipped invalid record at byte %d: %v", filepath.Base(path), offset, err))
			}
		}
		offset += int64(len(line))
		line = line[:0]
	}
}
