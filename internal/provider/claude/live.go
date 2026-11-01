package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// liveState is what a running Claude Code process says about itself in
// <home>/sessions/<pid>.json. Claude Code removes the file when it exits, but
// a crash can leave one behind, so only a running PID counts.
//
// The file matters because Claude Code (2.1 and later) writes a turn to the
// transcript only after the turn is over: while it waits on a question or a
// permission, the transcript still ends with the user's message.
type liveState struct {
	SessionID  string `json:"sessionId"`
	PID        int    `json:"pid"`
	Status     string `json:"status"` // "busy", "waiting" or "idle"
	WaitingFor string `json:"waitingFor"`
	UpdatedAt  int64  `json:"statusUpdatedAt"` // milliseconds since the epoch
	// WrittenAt is the file's modification time, not part of its content. A
	// process that started after it cannot be the one that wrote the file.
	WrittenAt time.Time `json:"-"`
}

// readLive reads every live-state file of one Claude home. The .key files next
// to them hold a peer token and are never read. A file that cannot be read or
// parsed is returned as an error after the others.
func readLive(home string) ([]liveState, error) {
	dir := filepath.Join(home, "sessions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var states []liveState
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := provider.ReadFileLimit(path, provider.MaxFileSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var s liveState
		if json.Unmarshal(data, &s) != nil {
			errs = append(errs, fmt.Errorf("cannot read Claude live-session metadata %s", entry.Name()))
			continue
		}
		if info, err := os.Stat(path); err == nil {
			s.WrittenAt = info.ModTime()
		}
		states = append(states, s)
	}
	return states, errors.Join(errs...)
}

// processAge reports how long pid has been running. It is a variable so tests
// can substitute it. ok is false when the age cannot be determined.
var processAge = func(pid int) (age time.Duration, ok bool) {
	// etime is portable across macOS and Linux: [[dd-]hh:]mm:ss.
	out, err := provider.Run(context.Background(), "ps", "-o", "etime=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0, false
	}
	return parseElapsed(strings.TrimSpace(string(out)))
}

// parseElapsed reads the etime format of ps: [[dd-]hh:]mm:ss.
func parseElapsed(s string) (time.Duration, bool) {
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	seconds := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return 0, false
		}
		seconds = seconds*60 + n
	}
	return time.Duration(days*24*60*60+seconds) * time.Second, true
}

// running reports whether the process that wrote a status file at writtenAt is
// still alive. EPERM means the PID exists but belongs to someone else. A crash
// can leave the file behind and the system can hand its PID to another
// process; one that started after the file was last written cannot be its
// author, so it does not count. A zero writtenAt skips that check.
func running(pid int, writtenAt time.Time) (bool, error) {
	ok, err := pidExists(pid)
	if !ok || err != nil || writtenAt.IsZero() {
		return ok, err
	}
	if age, known := processAge(pid); known {
		// A second of slack covers etime's whole-second rounding.
		if started := time.Now().Add(-age); started.After(writtenAt.Add(time.Second)) {
			return false, nil
		}
	}
	return true, nil
}

// pidExists reports whether a process with pid exists. It is a variable so
// tests can make it fail.
var pidExists = func(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	defer process.Release()
	err = process.Signal(syscall.Signal(0))
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, fmt.Errorf("cannot determine Claude process state: %w", err)
}

// SessionProcess finds the running Claude Code process of s through the
// status file it keeps, so its tmux pane is found even when it was started
// with plain `claude` or --session-id, which name no session to match. A file
// left by a crash, or whose PID was reused, does not count (see running).
func (a *Adapter) SessionProcess(s session.Session) (int, bool) {
	if s.ID == "" {
		return 0, false
	}
	for _, home := range a.homes() {
		// An unreadable file only hides its own session; the arguments are
		// still matched.
		states, _ := readLive(home)
		for _, state := range states {
			if state.SessionID != s.ID {
				continue
			}
			// A process whose state cannot be read is not reported: the
			// refresh already warns about it (applyLive), and the session's
			// arguments are still matched.
			if ok, _ := running(state.PID, state.WrittenAt); ok {
				return state.PID, true
			}
		}
	}
	return 0, false
}

// applyLive lets a running Claude Code's own status replace what its
// transcript shows. It runs after the stale-signal rule: a running process
// is the proof that rule asks for.
func (a *Adapter) applyLive(result *provider.Result) {
	live := map[string]liveState{}
	for _, home := range a.homes() {
		states, err := readLive(home)
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}
		for _, s := range states {
			ok, err := running(s.PID, s.WrittenAt)
			if err != nil {
				// The session falls back to its transcript; say why its
				// live status is missing.
				result.Warnings = append(result.Warnings, fmt.Sprintf("Claude Code session %s: %v", session.Text(s.SessionID, 80), err))
			}
			if ok && s.SessionID != "" {
				live[s.SessionID] = s
			}
		}
	}
	for i := range result.Sessions {
		s := &result.Sessions[i]
		state, ok := live[s.ID]
		if !ok {
			continue
		}
		switch state.Status {
		case "waiting":
			evidence := "Claude Code: waiting for input"
			if reason := session.Text(state.WaitingFor, 80); reason != "" {
				evidence = "Claude Code: waiting, " + reason
			}
			provider.SetState(s, session.Waiting, evidence)
		case "busy":
			provider.SetState(s, session.Working, "Claude Code: running a turn")
		case "idle":
			// The process is open between turns. A finished turn stays done;
			// anything else in the transcript is out of date.
			if s.Status != session.Completed {
				provider.SetState(s, session.Idle, "Claude Code: open, no turn running")
			}
		default:
			continue
		}
		if at := time.UnixMilli(state.UpdatedAt); state.UpdatedAt > 0 && at.After(s.UpdatedAt) {
			s.UpdatedAt = at
		}
	}
}
