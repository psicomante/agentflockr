package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func apply(s *session.Session, record []byte) error {
	var r struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(record, &r); err != nil {
		return err
	}
	if r.ID != "" {
		s.ID = r.ID
	}
	if r.CWD != "" {
		s.Directory = r.CWD
	}
	return nil
}

func TestScanSkipsABadRecordAndKeepsTheSession(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	body := `{"id":"one","cwd":"` + dir + `"}` + "\nthis is not json\n" + `{"id":"two"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "t.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &Source{Provider: session.Codex, Roots: []string{root}, Apply: apply}
	got := src.Scan(context.Background(), time.Now())
	if got.Err != nil || len(got.Sessions) != 1 || got.Sessions[0].ID != "two" || len(got.Warnings) != 1 {
		t.Fatalf("a bad record must be skipped with a warning: %+v", got)
	}
	// The offset moved past the bad line, so the next scan has nothing to redo.
	if again := src.Scan(context.Background(), time.Now()); len(again.Warnings) != 0 || len(again.Sessions) != 1 {
		t.Fatalf("the bad record was read again: %+v", again)
	}
}

func TestScanSkipsAnUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any folder")
	}
	root, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "t.jsonl"), []byte(`{"id":"one","cwd":"`+dir+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	got := (&Source{Provider: session.Codex, Roots: []string{root}, Apply: apply}).Scan(context.Background(), time.Now())
	if got.Err != nil || len(got.Sessions) != 1 || len(got.Warnings) != 1 {
		t.Fatalf("an unreadable folder must be skipped with a warning: %+v", got)
	}
}

// history writes n transcripts under a new root, every seventh with a bad
// record, and returns the root.
func history(t *testing.T, n int) string {
	t.Helper()
	root, dir := t.TempDir(), t.TempDir()
	for i := range n {
		body := fmt.Sprintf(`{"id":"s%02d","cwd":"%s"}`+"\n", i, dir)
		if i%7 == 0 {
			body += "this is not json\n" + fmt.Sprintf(`{"id":"s%02d-again"}`+"\n", i)
		}
		// Two folders, so paths do not come in creation order alone.
		sub := filepath.Join(root, fmt.Sprint(i%2))
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("t%02d.jsonl", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestParallelScanMatchesSerialScan(t *testing.T) {
	root := history(t, 50)
	if os.Geteuid() != 0 {
		// One file that cannot be read becomes a warning in its place.
		locked := filepath.Join(root, "1", "t49.jsonl")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o600)
	}
	now := time.Now()
	serial := (&Source{Provider: session.Codex, Roots: []string{root}, Apply: apply, Workers: 1}).Scan(context.Background(), now)
	parallel := (&Source{Provider: session.Codex, Roots: []string{root}, Apply: apply, Workers: 8}).Scan(context.Background(), now)
	if serial.Err != nil || parallel.Err != nil {
		t.Fatalf("scans failed: %v, %v", serial.Err, parallel.Err)
	}
	if len(serial.Sessions) < 40 || len(serial.Warnings) < 8 {
		t.Fatalf("the fixture should give sessions and warnings: %d sessions, %v", len(serial.Sessions), serial.Warnings)
	}
	if !reflect.DeepEqual(serial.Sessions, parallel.Sessions) {
		t.Fatalf("sessions differ:\nserial   %v\nparallel %v", serial.Sessions, parallel.Sessions)
	}
	if !reflect.DeepEqual(serial.Warnings, parallel.Warnings) {
		t.Fatalf("warnings differ:\nserial   %q\nparallel %q", serial.Warnings, parallel.Warnings)
	}
}

func TestParallelScanStopsWhenCancelled(t *testing.T) {
	root := history(t, 50)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	read := map[string]int{} // reads per transcript, across both scans
	counting := func(s *session.Session, record []byte) error {
		mu.Lock()
		read[s.Transcript]++
		if len(read) == 10 {
			// Cancel partway through the history.
			cancel()
		}
		mu.Unlock()
		return apply(s, record)
	}
	src := &Source{Provider: session.Codex, Roots: []string{root}, Apply: counting, Workers: 8}
	first := src.Scan(ctx, time.Now())
	if !errors.Is(first.Err, context.Canceled) {
		t.Fatalf("a cancelled scan must say so: %v", first.Err)
	}
	mu.Lock()
	finished := map[string]bool{}
	for path := range read {
		finished[path] = true
	}
	clear(read)
	mu.Unlock()
	if len(finished) == 0 || len(finished) == 50 {
		t.Fatalf("the cancel should stop the scan partway: %d files read", len(finished))
	}

	again := src.Scan(context.Background(), time.Now())
	if again.Err != nil || len(again.Sessions) != 50 {
		t.Fatalf("the next scan must finish: %v, %d sessions", again.Err, len(again.Sessions))
	}
	for path := range read {
		if finished[path] {
			t.Errorf("%s was read again although the cancelled scan finished it", filepath.Base(path))
		}
	}
	if len(read)+len(finished) != 50 {
		t.Fatalf("every file must be read once: %d then %d", len(finished), len(read))
	}
}
