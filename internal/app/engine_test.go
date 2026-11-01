package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

func scanning(scan func(context.Context, time.Time) provider.Result) provider.Registry {
	return provider.Registry{&providertest.Fake{Provider: session.Codex, ScanFunc: scan}}
}

func TestProviderFailurePreservesHistoryWithoutStaleLiveState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	item := session.Session{ID: "test", Provider: session.Codex, Directory: dir, OriginalDirectory: dir, Status: session.Working}
	engine := Engine{Store: db, Providers: scanning(func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Codex, Sessions: []session.Session{item}}
	})}
	snap := engine.Scan(context.Background())
	if len(snap.Sessions) != 1 || snap.Sessions[0].Cached || snap.Sessions[0].Status != session.Working {
		t.Fatalf("fresh scan: %+v", snap)
	}
	engine.Providers = scanning(func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Codex, Err: fmt.Errorf("unavailable")}
	})
	snap = engine.Scan(context.Background())
	if len(snap.Sessions) != 1 || !snap.Sessions[0].Cached || snap.Sessions[0].Status != session.Unknown || len(snap.Warnings) != 1 {
		t.Fatalf("failure discarded history or misrepresented status: %+v", snap)
	}
}

func TestScanFlagsMissingFolders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gone := filepath.Join(t.TempDir(), "moved-away")
	engine := Engine{Store: db, Providers: scanning(func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Codex, Sessions: []session.Session{{ID: "gone", Provider: session.Codex, Directory: gone}}}
	})}
	snap := engine.Scan(context.Background())
	if len(snap.Sessions) != 1 || !snap.Sessions[0].Missing {
		t.Fatalf("missing folder not flagged: %+v", snap)
	}
}

func TestSortPutsAttentionFirst(t *testing.T) {
	now := time.Now()
	items := []session.Session{
		{ID: "old-done", Status: session.Completed, UpdatedAt: now.Add(-time.Hour)},
		{ID: "working", Status: session.Working, UpdatedAt: now},
		{ID: "new-done", Status: session.Completed, UpdatedAt: now},
		{ID: "waiting", Status: session.Waiting, UpdatedAt: now.Add(-time.Hour)},
	}
	Sort(items)
	var got []string
	for _, s := range items {
		got = append(got, s.ID)
	}
	if fmt.Sprint(got) != "[waiting working new-done old-done]" {
		t.Fatalf("order = %v", got)
	}
}

// noTmux answers as if tmux were not running.
func noTmux(context.Context, string, ...string) ([]byte, error) {
	return nil, fmt.Errorf("no server running")
}

func TestOpenResumesThroughTheAdapter(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{Providers: provider.Registry{&providertest.Fake{Provider: session.Codex}}, Run: noTmux}
	s := session.Session{ID: "one", Provider: session.Codex, Directory: dir, OriginalDirectory: dir}
	command, err := e.Open(context.Background(), s)
	if err != nil || command.Name != "true" || command.Directory != dir {
		t.Fatalf("open: %+v, %v", command, err)
	}
	s.Provider = session.Claude
	if _, err := e.Open(context.Background(), s); err == nil {
		t.Fatal("opened a session for an agent that is not configured")
	}
	missing := session.Session{ID: "one", Provider: session.Codex, Directory: filepath.Join(dir, "gone")}
	if _, err := e.Open(context.Background(), missing); err == nil {
		t.Fatal("opened a session whose folder is gone")
	}
	e.Demo = true
	if _, err := e.Open(context.Background(), session.Session{ID: "one", Provider: session.Codex, Directory: dir}); err == nil {
		t.Fatal("demo mode launched an agent")
	}
}

// matching recognizes one exact argv as the session's process.
type matching struct {
	*providertest.Fake
	argv string
}

func (m matching) MatchesProcess(args []string, _ session.Session) bool {
	return fmt.Sprint(args) == m.argv
}

func TestOpenPrefersTheRunningTmuxPane(t *testing.T) {
	t.Setenv("TMUX", "")
	dir := t.TempDir()
	var selected []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps":
			return []byte("100 1 zsh\n101 100 codex resume one\n"), nil
		case args[0] == "list-panes":
			return []byte("%3\t$2\t100\n"), nil
		}
		selected = append(selected, args[0]+" "+args[2])
		return nil, nil
	}
	adapter := matching{Fake: &providertest.Fake{Provider: session.Codex}, argv: "[codex resume one]"}
	e := &Engine{Providers: provider.Registry{adapter}, Run: run}
	command, err := e.Open(context.Background(), session.Session{ID: "one", Provider: session.Codex, Directory: dir, OriginalDirectory: dir})
	if err != nil || command.Name != "tmux" || fmt.Sprint(command.Args) != "[attach-session -t $2]" {
		t.Fatalf("open: %+v, %v", command, err)
	}
	if fmt.Sprint(selected) != "[select-window %3 select-pane %3]" {
		t.Fatalf("the pane was not brought forward: %v", selected)
	}
}

// knownProcess is an agent that knows which process runs its session, as
// Claude Code does from its status files.
type knownProcess struct {
	*providertest.Fake
	pid int
}

func (k knownProcess) SessionProcess(session.Session) (int, bool) { return k.pid, true }

// A session started without naming itself (plain `claude`) has no argument to
// match; the process its agent reports still leads to its pane (REQ-OPN-002).
func TestOpenFindsThePaneByTheSessionProcess(t *testing.T) {
	t.Setenv("TMUX", "")
	dir := t.TempDir()
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps":
			return []byte("100 1 zsh\n101 100 claude\n"), nil
		case args[0] == "list-panes":
			return []byte("%3\t$2\t100\n"), nil
		}
		return nil, nil
	}
	adapter := knownProcess{Fake: &providertest.Fake{Provider: session.Claude}, pid: 101}
	e := &Engine{Providers: provider.Registry{adapter}, Run: run}
	command, err := e.Open(context.Background(), session.Session{ID: "cc-1", Provider: session.Claude, Directory: dir, OriginalDirectory: dir})
	if err != nil || command.Name != "tmux" || fmt.Sprint(command.Args) != "[attach-session -t $2]" {
		t.Fatalf("open: %+v, %v; want the pane running process 101", command, err)
	}
}

func TestDemoNeverTouchesTheIndex(t *testing.T) {
	engine := Engine{Demo: true}
	if snap := engine.Scan(context.Background()); len(snap.Sessions) == 0 {
		t.Fatal("demo must show sample sessions")
	}
	if snap, err := engine.Cached(); err != nil || len(snap.Sessions) == 0 {
		t.Fatalf("demo cached = %d sessions, %v", len(snap.Sessions), err)
	}
	if err := engine.SetView("projects"); err != nil {
		t.Fatalf("demo view changes stay in memory: %v", err)
	}
}

// The ID becomes part of an agent request, so a hostile one is refused
// before the agent is asked.
func TestPreviewRefusesInvalidIDs(t *testing.T) {
	asked := false
	engine := Engine{Providers: provider.Registry{providertest.WithPreview(&providertest.Fake{Provider: session.OpenCode}, func(context.Context, string) (string, error) {
		asked = true
		return "", nil
	})}}
	for _, id := range []string{"..", "--all", "a/b", ""} {
		if _, err := engine.Preview(context.Background(), session.Session{Provider: session.OpenCode, ID: id}); err == nil {
			t.Errorf("preview of %q was not refused", id)
		}
	}
	if asked {
		t.Fatal("the agent was asked for an invalid ID")
	}
}

// A project reached through a symlink is the same folder, so the session has
// not moved and its running pane must still be used.
func TestOpenTreatsASymlinkedFolderAsNotMoved(t *testing.T) {
	t.Setenv("TMUX", "")
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps":
			return []byte("100 1 zsh\n101 100 codex resume one\n"), nil
		case args[0] == "list-panes":
			return []byte("%3\t$2\t100\n"), nil
		}
		return nil, nil
	}
	adapter := matching{Fake: &providertest.Fake{Provider: session.Codex}, argv: "[codex resume one]"}
	e := &Engine{Providers: provider.Registry{adapter}, Run: run}
	command, err := e.Open(context.Background(), session.Session{ID: "one", Provider: session.Codex, Directory: dir, OriginalDirectory: alias})
	if err != nil || command.Name != "tmux" {
		t.Fatalf("a symlinked folder must not skip the tmux pane: %+v, %v", command, err)
	}
}

// A pane that cannot be selected must stop opening; resuming would start a
// second agent on a session that is already running.
func TestOpenStopsWhenTheTmuxPaneCannotBeSelected(t *testing.T) {
	t.Setenv("TMUX", "")
	dir := t.TempDir()
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps":
			return []byte("100 1 zsh\n101 100 codex resume one\n"), nil
		case args[0] == "list-panes":
			return []byte("%3\t$2\t100\n"), nil
		}
		return nil, fmt.Errorf("select failed")
	}
	adapter := matching{Fake: &providertest.Fake{Provider: session.Codex}, argv: "[codex resume one]"}
	e := &Engine{Providers: provider.Registry{adapter}, Run: run}
	_, err := e.Open(context.Background(), session.Session{ID: "one", Provider: session.Codex, Directory: dir, OriginalDirectory: dir})
	if err == nil || !strings.Contains(err.Error(), "could not switch to it") {
		t.Fatalf("expected the pane error, got %v", err)
	}
}
