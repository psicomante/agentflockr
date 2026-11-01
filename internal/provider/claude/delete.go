package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Delete removes the transcript and its adjacent subagent directory. Every
// removal is confined with os.Root to the verified project folder, after
// checking the exact filename, transcript identity, and that no live process
// owns the session.
func (a *Adapter) Delete(ctx context.Context, s session.Session) error {
	if err := provider.CheckDeletable(ctx, s); err != nil {
		return err
	}
	if a.Home == "" || s.Transcript == "" {
		return fmt.Errorf("Claude home and transcript are required")
	}
	target, err := filepath.Abs(s.Transcript)
	if err != nil {
		return err
	}
	var base, owner string
	var parts []string
	for _, home := range a.homes() {
		root, err := filepath.Abs(filepath.Join(home, "projects"))
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, target)
		if err != nil {
			return err
		}
		candidate := strings.Split(rel, string(os.PathSeparator))
		if len(candidate) == 2 && candidate[0] != ".." && candidate[0] != "." && candidate[1] == s.ID+".jsonl" {
			base, owner, parts = root, home, candidate
			break
		}
	}
	if owner == "" {
		return fmt.Errorf("refusing transcript outside the configured Claude project/session path")
	}
	if err := (&Adapter{Home: owner}).checkLive(s.ID); err != nil {
		return err
	}
	projects, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer projects.Close()
	project, err := projects.OpenRoot(parts[0])
	if err != nil {
		return fmt.Errorf("open Claude project: %w", err)
	}
	defer project.Close()
	name := parts[1]
	info, err := project.Lstat(name)
	if os.IsNotExist(err) {
		if _, sideErr := project.Lstat(s.ID); os.IsNotExist(sideErr) {
			return nil
		}
		return fmt.Errorf("transcript is missing but session artifacts remain; cannot verify ownership")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular or symlinked Claude transcript")
	}
	f, err := project.Open(name)
	if err != nil {
		return err
	}
	if err := verifyClaudeIdentity(f, s.ID); err != nil {
		f.Close()
		return err
	}
	f.Close()
	afterVerify()
	// The transcript must still be the one verified above. Check before
	// removing anything, so a session that wrote meanwhile loses nothing, and
	// again before removing the transcript itself.
	unchanged := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := project.Lstat(name)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
			return fmt.Errorf("transcript changed during deletion; close the native session and retry")
		}
		return nil
	}
	if err := unchanged(); err != nil {
		return err
	}
	// Rooted operations confine every removal to this verified project directory.
	// The adjacent directory is owned by the verified transcript's exact ID.
	if child, err := project.Lstat(s.ID); err == nil {
		if !child.IsDir() || child.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing unexpected Claude session artifact path")
		}
		if err := project.RemoveAll(s.ID); err != nil {
			return fmt.Errorf("delete session artifacts: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := unchanged(); err != nil {
		return err
	}
	return project.Remove(name)
}

// afterVerify runs between checking the transcript's identity and removing
// anything. Tests replace it to change the transcript at that moment.
var afterVerify = func() {}

// verifyClaudeIdentity reads the transcript with its own loop rather than
// jsonl.Read, on purpose. r was opened through os.Root, confined to the
// project folder; jsonl.Read opens by path. And a damaged record must stop a
// deletion, where jsonl.Read skips it with a warning. The record limit is
// the same one.
func verifyClaudeIdentity(r io.Reader, id string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, provider.ReadBufferSize), provider.MaxRecordSize)
	found := false
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var record struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("cannot verify transcript identity: %w", err)
		}
		if record.SessionID != "" {
			if record.SessionID != id {
				return fmt.Errorf("transcript belongs to a different session")
			}
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("transcript has no matching session identity")
	}
	return nil
}

func (a *Adapter) checkLive(id string) error {
	states, err := readLive(a.Home)
	if err != nil {
		return err
	}
	for _, active := range states {
		if active.SessionID != id {
			continue
		}
		ok, err := running(active.PID, active.WrittenAt)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("close this running Claude session before deleting its history")
		}
	}
	return nil
}
