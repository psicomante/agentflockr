package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// liveHome writes one transcript, last touched an hour ago, whose turn the
// transcript shows as still running: Claude Code writes the turn only when it
// ends.
func liveHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	project := filepath.Join(home, "projects", "project")
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"cc-1\",\"cwd\":\"/project\",\"timestamp\":%q,\"message\":{\"content\":\"Wire up payments\"}}\n", stamp)
	if err := os.WriteFile(filepath.Join(project, "cc-1.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeLive(t *testing.T, home string, pid int, body string) {
	t.Helper()
	data := fmt.Sprintf(`{"pid":%d,"sessionId":"cc-1",%s}`, pid, body)
	if err := os.WriteFile(filepath.Join(home, "sessions", fmt.Sprintf("%d.json", pid)), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func scanOne(t *testing.T, home string) session.Session {
	t.Helper()
	result := New(home).Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != 1 {
		t.Fatalf("scan: %+v", result)
	}
	return result.Sessions[0]
}

func TestClaudeLiveStatus(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name, body string
		status     session.Status
		evidence   string
	}{
		{"waiting", fmt.Sprintf(`"status":"waiting","waitingFor":"input needed\u001b[31m","statusUpdatedAt":%d`, now), session.Waiting, "Claude Code: waiting, input needed"},
		{"busy", fmt.Sprintf(`"status":"busy","statusUpdatedAt":%d`, now), session.Working, "Claude Code: running a turn"},
		{"idle", fmt.Sprintf(`"status":"idle","statusUpdatedAt":%d`, now), session.Idle, "Claude Code: open, no turn running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := liveHome(t)
			// This test's own process stands in for a running Claude Code.
			writeLive(t, home, os.Getpid(), tc.body)
			s := scanOne(t, home)
			if s.Status != tc.status || s.Evidence != tc.evidence {
				t.Fatalf("got %s %q, want %s %q", s.Status, s.Evidence, tc.status, tc.evidence)
			}
			if time.Since(s.UpdatedAt) > time.Minute {
				t.Fatalf("updated at %v, want the live status time", s.UpdatedAt)
			}
		})
	}
}

// A file left by a Claude Code that crashed proves nothing: the transcript's
// own, now stale, signal stands.
func TestClaudeLiveStatusIgnoresExitedProcess(t *testing.T) {
	home := liveHome(t)
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot start a short-lived process:", err)
	}
	writeLive(t, home, cmd.Process.Pid, `"status":"waiting","waitingFor":"input needed"`)
	if s := scanOne(t, home); s.Status != session.Unknown {
		t.Fatalf("got %s %q, want unknown", s.Status, s.Evidence)
	}
}

// An unreadable live-state file is a warning, not a failed scan; the .key
// file beside it is never read.
func TestClaudeLiveStatusUnreadableFile(t *testing.T) {
	home := liveHome(t)
	if err := os.WriteFile(filepath.Join(home, "sessions", "1.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "1.0123.key"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	result := New(home).Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != 1 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "1.json") {
		t.Fatalf("scan: %+v", result)
	}
}

// A status file left by a crash must not count as live when its PID now
// belongs to a process that started after the file was written.
func TestClaudeLiveStatusIgnoresReusedPID(t *testing.T) {
	home := liveHome(t)
	writeLive(t, home, os.Getpid(), `"status":"busy"`)
	path := filepath.Join(home, "sessions", fmt.Sprintf("%d.json", os.Getpid()))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// The process is reported as only a minute old: younger than the file.
	was := processAge
	processAge = func(int) (time.Duration, bool) { return time.Minute, true }
	defer func() { processAge = was }()
	if s := scanOne(t, home); s.Status != session.Unknown {
		t.Fatalf("a reused PID counted as live: %s %q", s.Status, s.Evidence)
	}
	// A process older than the file wrote it.
	processAge = func(int) (time.Duration, bool) { return 2 * time.Hour, true }
	if s := scanOne(t, home); s.Status != session.Working {
		t.Fatalf("the real process was ignored: %s %q", s.Status, s.Evidence)
	}
}

func TestParseElapsed(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"05:07":      5*time.Minute + 7*time.Second,
		"01:05:07":   time.Hour + 5*time.Minute + 7*time.Second,
		"2-01:05:07": 48*time.Hour + time.Hour + 5*time.Minute + 7*time.Second,
	} {
		if got, ok := parseElapsed(in); !ok || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := parseElapsed("junk"); ok {
		t.Error("junk must not parse")
	}
}

func TestClaudeLiveStatusRefusesAnOversizedFile(t *testing.T) {
	home := liveHome(t)
	big := make([]byte, provider.MaxFileSize+1)
	if err := os.WriteFile(filepath.Join(home, "sessions", "9.json"), big, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLive(home); err == nil {
		t.Fatal("an oversized status file must be refused")
	}
}

// The status file names the process running a session, so its tmux pane can
// be found even when Claude Code was started without --resume (REQ-OPN-002).
// A file for another session, or one left by an exited process, gives none.
func TestSessionProcessComesFromTheStatusFile(t *testing.T) {
	home := liveHome(t)
	writeLive(t, home, os.Getpid(), `"status":"busy"`)
	a := New(home)
	if pid, ok := a.SessionProcess(session.Session{ID: "cc-1"}); !ok || pid != os.Getpid() {
		t.Fatalf("got %d %v, want the process in the status file", pid, ok)
	}
	if _, ok := a.SessionProcess(session.Session{ID: "cc-2"}); ok {
		t.Fatal("a status file for another session was used")
	}

	gone := liveHome(t)
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot start a short-lived process:", err)
	}
	writeLive(t, gone, cmd.Process.Pid, `"status":"busy"`)
	if _, ok := New(gone).SessionProcess(session.Session{ID: "cc-1"}); ok {
		t.Fatal("an exited process was reported as running the session")
	}
}

// When a status file's process cannot be checked, the session falls back to
// its transcript, and the refresh says why instead of hiding it.
func TestUncheckableProcessIsAWarning(t *testing.T) {
	home := liveHome(t)
	writeLive(t, home, os.Getpid(), `"status":"busy"`)
	was := pidExists
	pidExists = func(int) (bool, error) { return false, fmt.Errorf("cannot determine Claude process state: denied") }
	defer func() { pidExists = was }()
	result := New(home).Scan(context.Background(), time.Now())
	if len(result.Sessions) != 1 || result.Sessions[0].Status == session.Working {
		t.Fatalf("an uncheckable process was taken as running: %+v", result.Sessions)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "Claude Code session cc-1: cannot determine Claude process state: denied") {
		t.Fatalf("no warning for the uncheckable process: %q", result.Warnings)
	}
}
