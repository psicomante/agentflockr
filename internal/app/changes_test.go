package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakes registers one fake adapter per provider. Only the first scans, so
// one result can mix providers; all share the deletion func.
func fakes(scan func(context.Context, time.Time) provider.Result, del func(context.Context, session.Session) error, providers ...session.Provider) provider.Registry {
	var r provider.Registry
	for i, p := range providers {
		f := &providertest.Fake{Provider: p, DeleteFunc: del}
		if i == 0 {
			f.ScanFunc = scan
		}
		r = append(r, f)
	}
	return r
}

func TestDeleteForgetsOnlyAfterTheAgentSucceeds(t *testing.T) {
	db := openStore(t)
	s := session.Session{ID: "ses_one", Provider: session.OpenCode, Directory: t.TempDir()}
	if err := db.Upsert([]session.Session{s}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: db, Providers: fakes(nil, func(context.Context, session.Session) error { return fmt.Errorf("provider refused") }, session.OpenCode)}
	if generation, err := e.Delete(context.Background(), s.Key()); err == nil || generation != 0 {
		t.Fatalf("the agent's failure was hidden, or changed the generation: %d %v", generation, err)
	}
	if items, _ := db.Load(); len(items) != 1 || e.Generation() != 0 {
		t.Fatal("a failed delete changed the index")
	}
	e.Providers = fakes(nil, func(_ context.Context, got session.Session) error {
		if got.Key() != s.Key() {
			t.Fatal("wrong native target")
		}
		return nil
	}, session.OpenCode)
	generation, err := e.Delete(context.Background(), s.Key())
	if err != nil {
		t.Fatal(err)
	}
	if generation != 1 || e.Generation() != 1 {
		t.Fatal("a delete must advance the generation")
	}
	e.Providers = fakes(func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: s.Provider, Sessions: []session.Session{s}}
	}, nil, session.OpenCode)
	if result := e.Scan(context.Background()); len(result.Sessions) != 0 || result.Generation != 1 {
		t.Fatalf("a later scan resurrected the deleted session: %+v", result)
	}
}

func TestDeleteRejectsUnknownTargetsAndDemo(t *testing.T) {
	e := Engine{Demo: true}
	if _, err := e.Delete(context.Background(), "opencode:ses_one"); err == nil {
		t.Fatal("demo deletion allowed")
	}
	e = Engine{Store: openStore(t), Providers: fakes(nil, func(context.Context, session.Session) error {
		t.Fatal("an unindexed session reached the agent")
		return nil
	}, session.OpenCode)}
	if _, err := e.Delete(context.Background(), "opencode:ses_missing"); err == nil {
		t.Fatal("unknown target accepted")
	}
}

func TestRemoveIsLocalAndSurvivesScans(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("original transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	items := []session.Session{
		{ID: "one", Provider: session.Claude, Directory: dir, Transcript: transcript},
		{ID: "two", Provider: session.Codex, Directory: dir},
	}
	e := Engine{Store: openStore(t), Providers: fakes(func(context.Context, time.Time) provider.Result {
		return provider.Result{Sessions: items}
	}, func(context.Context, session.Session) error {
		t.Fatal("local removal reached the agent")
		return nil
	}, session.Codex, session.Claude)}
	initial := e.Scan(context.Background())
	if len(initial.Sessions) != 2 {
		t.Fatalf("initial scan: %+v", initial)
	}
	if _, err := e.RemoveSession(items[0].Key()); err != nil {
		t.Fatal(err)
	}
	if got := e.Scan(context.Background()); len(got.Sessions) != 1 || got.Sessions[0].ID != "two" {
		t.Fatalf("session removal: %+v", got)
	}
	if _, err := e.RemoveProject(initial.Sessions[0].ProjectID); err != nil {
		t.Fatal(err)
	}
	if got := e.Scan(context.Background()); len(got.Sessions) != 0 || got.Generation != 2 {
		t.Fatalf("project removal: %+v", got)
	}
	if data, err := os.ReadFile(transcript); err != nil || string(data) != "original transcript" {
		t.Fatalf("the agent's transcript changed: %q, %v", data, err)
	}
}

func TestChangesAreRefusedInDemo(t *testing.T) {
	e := Engine{Demo: true}
	_, removeSession := e.RemoveSession("codex:one")
	_, removeProject := e.RemoveProject("project")
	_, reconnect := e.Reconnect("project", "/tmp")
	if removeSession == nil || removeProject == nil || reconnect == nil {
		t.Fatal("demo mode changed the index")
	}
}

// Each change reports the generation it produced, so the dashboard learns it
// without taking the engine's lock again, which could wait behind a whole
// refresh. A refused change leaves it as it was.
func TestChangesReturnTheirGeneration(t *testing.T) {
	db := openStore(t)
	dir := t.TempDir()
	if err := db.Upsert([]session.Session{{ID: "one", Provider: session.Codex, Directory: dir}, {ID: "two", Provider: session.Codex, Directory: dir}}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: db}
	if generation, err := e.RemoveSession("codex:one"); err != nil || generation != 1 {
		t.Fatalf("remove: %d %v, want generation 1", generation, err)
	}
	items, err := db.Load()
	if err != nil || len(items) != 1 {
		t.Fatalf("load: %v %v", items, err)
	}
	if generation, err := e.Reconnect(items[0].ProjectID, t.TempDir()); err != nil || generation != 2 {
		t.Fatalf("move: %d %v, want generation 2", generation, err)
	}
	if generation, err := e.Reconnect(items[0].ProjectID, "relative"); err == nil || generation != 2 {
		t.Fatalf("refused move: %d %v, want generation 2 and an error", generation, err)
	}
}

func TestMovedSessionsResumeInTheNewFolder(t *testing.T) {
	old, moved := t.TempDir(), t.TempDir()
	db := openStore(t)
	s := session.Session{ID: "one", Provider: session.Codex, Directory: old, OriginalDirectory: old}
	e := Engine{Store: db, Providers: fakes(func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Codex, Sessions: []session.Session{s}}
	}, nil, session.Codex), Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("a moved session must not attach to a pane in its old folder")
		return nil, nil
	}}
	snap := e.Scan(context.Background())
	if _, err := e.Reconnect(snap.Sessions[0].ProjectID, moved); err != nil {
		t.Fatal(err)
	}
	snap = e.Scan(context.Background())
	got := snap.Sessions[0]
	if got.Directory != moved || got.OriginalDirectory != old {
		t.Fatalf("after the move: %+v", got)
	}
	command, err := e.Open(context.Background(), got)
	if err != nil || command.Directory != moved {
		t.Fatalf("open: %+v, %v", command, err)
	}
}

// A refresh reads the agents without the engine lock, so a change made while
// an agent is slow finishes at once; the refresh still honours it, and its
// snapshot counts it.
func TestChangesDoNotWaitForASlowRefresh(t *testing.T) {
	dir := t.TempDir()
	items := []session.Session{
		{ID: "one", Provider: session.Codex, Directory: dir},
		{ID: "two", Provider: session.Codex, Directory: dir},
		{ID: "three", Provider: session.Codex, Directory: dir},
	}
	var slow atomic.Bool
	reading, release := make(chan struct{}), make(chan struct{})
	e := Engine{Store: openStore(t), Providers: fakes(func(context.Context, time.Time) provider.Result {
		if slow.Load() {
			close(reading)
			<-release
		}
		return provider.Result{Sessions: items}
	}, func(context.Context, session.Session) error { return nil }, session.Codex)}
	e.Scan(context.Background())

	slow.Store(true)
	done := make(chan Snapshot)
	go func() { done <- e.Scan(context.Background()) }()
	<-reading
	// The agent is still being read. Each change must finish without it.
	changed := make(chan uint64)
	go func() {
		g1, err1 := e.RemoveSession(items[0].Key())
		g2, err2 := e.Delete(context.Background(), items[1].Key())
		if err1 != nil || err2 != nil || g2 != g1+1 {
			t.Errorf("changes failed: %d %v, %d %v", g1, err1, g2, err2)
		}
		changed <- g2
	}()
	var generation uint64
	select {
	case generation = <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("a change waited for the agents to be read")
	}
	close(release)
	snap := <-done
	if snap.Generation != generation {
		t.Fatalf("the refresh's snapshot has generation %d, want %d: it must count changes made while it read", snap.Generation, generation)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "three" {
		t.Fatalf("a removed or deleted session came back: %+v", snap.Sessions)
	}
}

// Refreshes run one at a time, because each adapter's reader keeps a cache
// that only one scan may use.
func TestRefreshesDoNotScanAnAgentAtOnce(t *testing.T) {
	var running, most atomic.Int32
	e := Engine{Store: openStore(t), Providers: fakes(func(context.Context, time.Time) provider.Result {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return provider.Result{}
	}, nil, session.Codex)}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { e.Scan(context.Background()) })
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Fatalf("%d scans read the agent at once", most.Load())
	}
}
