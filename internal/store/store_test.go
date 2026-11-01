package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestLoadReturnsCachedHistoryGroupedByProject(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	items := []session.Session{
		{ID: "a", Provider: session.Codex, Directory: dir, Status: session.Working, Title: "First"},
		{ID: "b", Provider: session.Claude, Directory: dir, Status: session.Completed},
		{ID: "", Provider: session.Claude, Directory: dir},
		{ID: "relative", Provider: session.Claude, Directory: "project"},
	}
	if err := db.Upsert(items); err != nil {
		t.Fatal(err)
	}
	got, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ProjectID == "" || got[0].ProjectID != got[1].ProjectID {
		t.Fatalf("sessions in one folder must share a project: %+v", got)
	}
	for _, s := range got {
		if !s.Cached || s.Status != session.Unknown {
			t.Fatalf("cached history must not claim live status: %+v", s)
		}
		if s.ID == "a" && s.Title != "First" {
			t.Fatalf("payload lost: %+v", s)
		}
	}
}

func TestSymlinkedFolderKeepsOneProject(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Upsert([]session.Session{{ID: "via-link", Provider: session.Codex, Directory: link}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Upsert([]session.Session{{ID: "via-real", Provider: session.Codex, Directory: resolved}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ProjectID != got[1].ProjectID {
		t.Fatalf("a folder and its symlink must be one project: %+v", got)
	}
}

func TestPreferencesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if view, err := db.View(); err != nil || view != "" {
		t.Fatalf("fresh index view = %q, %v", view, err)
	}
	if err := db.SetView("projects"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if view, err := db.View(); err != nil || view != "projects" {
		t.Fatalf("view = %q, %v", view, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("an index from a newer schema must not be opened")
	}
}

// Two Stores on one file stand in for two processes sharing the index.
func TestConcurrentStoresWaitForEachOthersWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dir := t.TempDir()

	// a holds the write lock while b runs an Upsert, which reads before it
	// writes. A deferred transaction cannot wait to upgrade its read lock, so
	// SQLite would fail it at once with SQLITE_BUSY instead of honouring
	// busy_timeout.
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO preferences(key,value) VALUES('view','held')"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.Upsert([]session.Session{{ID: "blocked", Provider: session.Codex, Directory: dir}}) }()
	time.Sleep(100 * time.Millisecond) // Let b reach the lock before a lets go.
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("upsert did not wait for the other writer: %v", err)
	}

	const rounds = 20
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for w, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				item := session.Session{ID: fmt.Sprintf("%d-%d", w, i), Provider: session.Claude, Directory: dir}
				err := s.Upsert([]session.Session{item})
				if err == nil {
					err = s.SetView(item.ID)
				}
				if err != nil {
					errs <- fmt.Errorf("store %d round %d: %w", w, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	items, err := a.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + 2*rounds; len(items) != want {
		t.Fatalf("got %d sessions, want %d", len(items), want)
	}
}

// database/sql may replace a pooled connection at any time, so every new
// connection must get the same settings, not just the one Open used.
func TestFreshConnectionsKeepPragmas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxIdleConns(0) // Each statement now opens a new connection.
	for pragma, want := range map[string]int{"busy_timeout": 5000, "foreign_keys": 1} {
		var got int
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %d on a new connection, want %d", pragma, got, want)
		}
	}
}

// A refresh reports every session again. Only a changed session may cost a
// write, or the index is rewritten every few seconds.
func TestUpsertWritesOnlyChangedSessions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	item := session.Session{ID: "a", Provider: session.Codex, Directory: dir, Title: "first"}
	changes := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := s.Upsert([]session.Session{item}); err != nil {
		t.Fatal(err)
	}
	before := changes()
	if err := s.Upsert([]session.Session{item}); err != nil {
		t.Fatal(err)
	}
	if after := changes(); after != before {
		t.Fatalf("an unchanged session was written again: %d rows changed", after-before)
	}
	item.Title = "second"
	if err := s.Upsert([]session.Session{item}); err != nil {
		t.Fatal(err)
	}
	if after := changes(); after != before+1 {
		t.Fatalf("a changed session wrote %d rows, want 1", after-before)
	}
	loaded, err := s.Load()
	if err != nil || len(loaded) != 1 || loaded[0].Title != "second" {
		t.Fatalf("the change was not kept: %+v %v", loaded, err)
	}
}

func TestLoadSkipsAnUndecodableRow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	if err := db.Upsert([]session.Session{{ID: "a", Provider: session.Codex, Directory: dir}, {ID: "b", Provider: session.Codex, Directory: dir}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE sessions SET payload='not json' WHERE key=?`, session.Session{ID: "a", Provider: session.Codex}.Key()); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := db.LoadWithWarnings()
	if err != nil || len(got) != 1 || got[0].ID != "b" || len(warnings) != 1 {
		t.Fatalf("one bad row must not hide the others: %+v, %v, %v", got, warnings, err)
	}
}
