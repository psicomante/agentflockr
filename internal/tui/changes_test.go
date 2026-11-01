package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

// indexed builds a model over a real temporary index, filled by one scan of
// the given sessions. del answers native deletions.
func indexed(t *testing.T, sessions []session.Session, del func(context.Context, session.Session) error) (*Model, *app.Engine) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	scan := func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Codex, Sessions: sessions}
	}
	engine := &app.Engine{Store: db, Providers: provider.Registry{
		&providertest.Fake{Provider: session.Codex, ScanFunc: scan, DeleteFunc: del},
		&providertest.Fake{Provider: session.Claude, DeleteFunc: del},
	}}
	snapshot := engine.Scan(context.Background())
	m := New(context.Background(), engine, Options{Refresh: time.Second}, snapshot)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, engine
}

// run executes a command and feeds its message back, as the program would.
func run(m *Model, cmd tea.Cmd) {
	if cmd != nil {
		m.Update(cmd())
	}
}

func twoSessions(t *testing.T) []session.Session {
	dir := t.TempDir()
	now := time.Now()
	return []session.Session{
		{ID: "first", Provider: session.Codex, Directory: dir, OriginalDirectory: dir, Title: "First task", Status: session.Waiting, UpdatedAt: now},
		{ID: "second", Provider: session.Codex, Directory: dir, OriginalDirectory: dir, Title: "Second task", Status: session.Completed, UpdatedAt: now.Add(-time.Minute)},
	}
}

func TestRemoveAsksFirstAndSurvivesRefreshes(t *testing.T) {
	m, engine := indexed(t, twoSessions(t), nil)
	m.Update(key("x"))
	if m.mode != confirmRemove || !strings.Contains(screen(m), "keeps its history and files") {
		t.Fatalf("x must ask first:\n%s", screen(m))
	}
	m.Update(key("esc"))
	if m.mode != browsing || len(m.items) != 2 {
		t.Fatal("esc must cancel without changes")
	}
	stale := engine.Scan(context.Background())
	m.Update(key("x"))
	_, cmd := m.Update(key("y"))
	if !strings.Contains(screen(m), "Removing…") {
		t.Fatal("the dialog must show the removal in progress")
	}
	run(m, cmd)
	if m.mode != browsing || fmt.Sprint(ids(m)) != "[second]" || !strings.Contains(screen(m), "Removed from agentflockr") {
		t.Fatalf("after removal: mode %v rows %v\n%s", m.mode, ids(m), screen(m))
	}
	// A refresh that started before the removal must not bring it back.
	if _, cmd := m.Update(scanMsg(stale)); cmd == nil || fmt.Sprint(ids(m)) != "[second]" {
		t.Fatalf("a stale refresh restored the session: %v", ids(m))
	}
	m.Update(scanMsg(engine.Scan(context.Background())))
	if fmt.Sprint(ids(m)) != "[second]" {
		t.Fatalf("the next real scan restored the session: %v", ids(m))
	}
}

func TestRemoveProjectFromTheProjectsView(t *testing.T) {
	m, engine := indexed(t, twoSessions(t), nil)
	m.Update(key("tab"))
	m.Update(key("x"))
	if !strings.Contains(screen(m), "its 2 sessions") {
		t.Fatalf("the dialog must say how many sessions go:\n%s", screen(m))
	}
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if len(m.items) != 0 || !strings.Contains(screen(m), "Project removed") {
		t.Fatalf("project not removed: %v", ids(m))
	}
	if snap := engine.Scan(context.Background()); len(snap.Sessions) != 0 {
		t.Fatalf("the project came back on the next scan: %+v", snap.Sessions)
	}
}

func TestDeleteIsForSessionsAndShowsWhatGoes(t *testing.T) {
	var deleted []string
	m, _ := indexed(t, twoSessions(t), func(_ context.Context, s session.Session) error {
		deleted = append(deleted, s.ID)
		return nil
	})
	m.Update(key("tab"))
	m.Update(key("d"))
	if m.mode != browsing || !strings.Contains(screen(m), "pick a session to delete") {
		t.Fatal("deleting a whole project must be refused")
	}
	m.Update(key("tab"))
	m.Update(key("d"))
	if m.mode != confirmDelete || !strings.Contains(screen(m), "This can't be undone.") {
		t.Fatalf("d must ask first:\n%s", screen(m))
	}
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if fmt.Sprint(deleted) != "[first]" || fmt.Sprint(ids(m)) != "[second]" || !strings.Contains(screen(m), "Session deleted.") {
		t.Fatalf("deleted %v, rows %v", deleted, ids(m))
	}
}

func TestFailedDeleteStaysInTheDialog(t *testing.T) {
	m, _ := indexed(t, twoSessions(t), func(context.Context, session.Session) error {
		return fmt.Errorf("close this running session first")
	})
	m.Update(key("d"))
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if m.mode != confirmDelete || len(m.items) != 2 || !strings.Contains(screen(m), "Could not delete: close this running session first") {
		t.Fatalf("a failure must keep the dialog open and explain:\n%s", screen(m))
	}
	m.Update(key("esc"))
	if m.mode != browsing {
		t.Fatal("esc must close the dialog after a failure")
	}
}

func TestMoveByTypingAPath(t *testing.T) {
	sessions := twoSessions(t)
	destination := t.TempDir()
	m, engine := indexed(t, sessions, nil)
	m.Update(key("m"))
	if m.mode != moving || m.path.Value() != sessions[0].Directory {
		t.Fatalf("m must open the move dialog on the current folder: %q", m.path.Value())
	}
	m.path.SetValue(destination)
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if m.mode != browsing || !strings.Contains(screen(m), "Project moved") {
		t.Fatalf("after the move:\n%s", screen(m))
	}
	for _, it := range m.items {
		if it.session.Directory != destination {
			t.Fatalf("not moved: %+v", it.session)
		}
	}
	// The agents still report the old folder; the move must hold.
	m.Update(scanMsg(engine.Scan(context.Background())))
	if m.items[0].session.Directory != destination {
		t.Fatal("a scan undid the move")
	}
}

func TestMoveByBrowsingFolders(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := indexed(t, twoSessions(t), nil)
	m.Update(key("m"))
	m.path.SetValue(filepath.Join(root, "gone", "deeper"))
	m.Update(key("tab"))
	if !m.folders.active || m.folders.path != root || fmt.Sprint(m.folders.names) != "[alpha beta]" {
		t.Fatalf("browsing must start at the nearest existing folder, without hidden ones: %+v", m.folders)
	}
	m.Update(key("down"))
	m.Update(key("enter"))
	if m.folders.path != filepath.Join(root, "beta") {
		t.Fatalf("enter must open the folder: %s", m.folders.path)
	}
	m.Update(key("tab"))
	if m.folders.active || m.path.Value() != filepath.Join(root, "beta") {
		t.Fatalf("tab must use the browsed folder: %q", m.path.Value())
	}
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if m.items[0].session.Directory != filepath.Join(root, "beta") {
		t.Fatalf("moved to %s", m.items[0].session.Directory)
	}
}

func TestMoveRejectsAMissingFolder(t *testing.T) {
	m, _ := indexed(t, twoSessions(t), nil)
	m.Update(key("m"))
	m.path.SetValue(filepath.Join(t.TempDir(), "absent"))
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if m.mode != moving || !strings.Contains(screen(m), "Could not move") {
		t.Fatalf("a missing destination must be refused in the dialog:\n%s", screen(m))
	}
}

func TestDemoSessionsCannotChange(t *testing.T) {
	m := New(context.Background(), &app.Engine{Demo: true, Providers: registry()}, Options{Refresh: time.Second}, app.Demo())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	for _, k := range []string{"x", "d", "m"} {
		m.Update(key(k))
		if m.mode != browsing || !strings.Contains(screen(m), "Demo sessions can't be changed.") {
			t.Fatalf("%s opened a dialog in demo mode", k)
		}
	}
}

// A move updates the folder on screen in place: reloading the index would
// show every status as unknown until the next refresh.
func TestMoveKeepsLiveStatus(t *testing.T) {
	sessions := twoSessions(t)
	destination := t.TempDir()
	m, _ := indexed(t, sessions, nil)
	m.Update(key("m"))
	m.path.SetValue(destination + "/")
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if m.mode != browsing || !strings.Contains(screen(m), "Project moved") {
		t.Fatalf("after the move:\n%s", screen(m))
	}
	for _, s := range m.snapshot.Sessions {
		if s.Directory != destination || s.Cached {
			t.Fatalf("after the move: %+v", s)
		}
		if s.ID == "first" && s.Status != session.Waiting {
			t.Fatalf("the waiting session shows %s after the move", s.Status)
		}
	}
	if m.minGeneration != 1 {
		t.Fatalf("the move's generation was not recorded: %d", m.minGeneration)
	}
}

// Counts follow their number, through plural: one session reads "1 session",
// in the context line and in a project's row and details alike.
func TestCountsAgreeWithTheirNumber(t *testing.T) {
	m, _ := indexed(t, twoSessions(t)[:1], nil)
	m.Update(key("tab"))
	out := screen(m)
	if strings.Contains(out, "1 sessions") || strings.Contains(out, "1 projects") || strings.Count(out, "1 session") < 2 {
		t.Fatalf("the projects view's counts:\n%s", out)
	}
	m.Update(key("tab"))
	if out := screen(m); strings.Contains(out, "1 sessions") || !strings.Contains(out, "1 session") {
		t.Fatalf("the sessions count:\n%s", out)
	}
}
