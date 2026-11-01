package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

// An index written by the first public build must open with its history
// intact and gain the exclusion tables.
func TestVersionOneIndexUpgradesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	payload, _ := json.Marshal(session.Session{ID: "kept", Provider: session.Codex, Directory: dir, Title: "From version 1"})
	for _, statement := range []string{
		migrations[0],
		"PRAGMA user_version=1",
		"INSERT INTO projects(id,path) VALUES('p1','" + dir + "')",
		"INSERT INTO aliases(path,project_id) VALUES('" + dir + "','p1')",
		"INSERT INTO sessions(key,project_id,payload) VALUES('codex:kept','p1','" + string(payload) + "')",
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version %d, %v", version, err)
	}
	items, err := db.Load()
	if err != nil || len(items) != 1 || items[0].Title != "From version 1" || items[0].ProjectID != "p1" {
		t.Fatalf("history lost in the upgrade: %+v, %v", items, err)
	}
	if err := db.RemoveSession("codex:kept"); err != nil {
		t.Fatalf("exclusions unavailable after the upgrade: %v", err)
	}
}

func TestReconnectPersistsAndMergesRediscoveredHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	if err := os.Mkdir(newPath, 0700); err != nil {
		t.Fatal(err)
	}
	sessions := []session.Session{
		{ID: "same-id", Provider: session.Codex, Directory: old, OriginalDirectory: old, Status: session.Working},
		{ID: "same-id", Provider: session.Claude, Directory: newPath, OriginalDirectory: newPath},
	}
	if err := db.Upsert(sessions); err != nil {
		t.Fatal(err)
	}
	before, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, s := range before {
		if s.Provider == session.Codex {
			id = s.ProjectID
		}
	}
	if err := db.Reconnect(id, newPath); err != nil {
		t.Fatal(err)
	}
	// An old transcript must not move the project back on the next scan.
	if err := db.Upsert(sessions[:1]); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("provider-qualified IDs lost history: %d", len(after))
	}
	for _, s := range after {
		if s.ProjectID != id || s.Directory != newPath {
			t.Fatalf("lost the move: %+v", s)
		}
	}
	if err := db.Reconnect(id, filepath.Join(dir, "absent")); err == nil {
		t.Fatal("accepted a missing destination")
	}
}

func TestReconnectKeepsIdentityWhenProviderResolvesSymlink(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	realPath := filepath.Join(root, "destination")
	aliasPath := filepath.Join(root, "shortcut")
	if err := os.Mkdir(realPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, aliasPath); err != nil {
		t.Fatal(err)
	}
	item := session.Session{ID: "a", Provider: session.OpenCode, Directory: "/missing/project"}
	if err := db.Upsert([]session.Session{item}); err != nil {
		t.Fatal(err)
	}
	items, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	id := items[0].ProjectID
	if err := db.Reconnect(id, aliasPath); err != nil {
		t.Fatal(err)
	}
	item.Directory = realPath
	if err := db.Upsert([]session.Session{item}); err != nil {
		t.Fatal(err)
	}
	items, err = db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ProjectID != id || items[0].Directory != aliasPath {
		t.Fatalf("path normalization broke the move: %+v", items)
	}
}

func TestDeletedSessionCannotBeReinsertedByStaleScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items := []session.Session{{ID: "same", Provider: session.Codex, Directory: t.TempDir()}, {ID: "same", Provider: session.Claude, Directory: t.TempDir()}}
	if err := db.Upsert(items); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(items[0].Key()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Upsert(items); err != nil {
		t.Fatal(err)
	}
	left, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Provider != session.Claude {
		t.Fatalf("deletion was not persistent and provider-scoped: %+v", left)
	}
}

func TestLocalRemovalPersistsAndSuppressesDiscovery(t *testing.T) {
	for _, project := range []bool{false, true} {
		name := "session"
		if project {
			name = "project"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			old, destination, other := t.TempDir(), t.TempDir(), t.TempDir()
			items := []session.Session{
				{ID: "same", Provider: session.Codex, Directory: old},
				{ID: "same", Provider: session.Claude, Directory: destination},
				{ID: "keep", Provider: session.Codex, Directory: other},
			}
			if err := db.Upsert(items); err != nil {
				t.Fatal(err)
			}
			cached, err := db.Load()
			if err != nil {
				t.Fatal(err)
			}
			var id string
			for _, s := range cached {
				if s.Key() == items[0].Key() {
					id = s.ProjectID
				}
			}
			if err := db.Reconnect(id, destination); err != nil {
				t.Fatal(err)
			}
			if project {
				err = db.RemoveProject(id)
			} else {
				err = db.RemoveSession(items[0].Key())
			}
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			// New sessions at either known path must stay excluded for a project.
			items = append(items,
				session.Session{ID: "new-old", Provider: session.OpenCode, Directory: old},
				session.Session{ID: "new-destination", Provider: session.OpenCode, Directory: destination})
			if project {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(destination, alias); err != nil {
					t.Fatal(err)
				}
				items = append(items, session.Session{ID: "symlink", Provider: session.Claude, Directory: alias})
				// Removed session keys stay excluded after the agent moves them.
				items[0].Directory = other
			}
			if err := db.Upsert(items); err != nil {
				t.Fatal(err)
			}
			left, err := db.Load()
			if err != nil {
				t.Fatal(err)
			}
			want := 4
			if project {
				want = 1
			}
			if len(left) != want {
				t.Fatalf("got %d sessions, want %d: %+v", len(left), want, left)
			}
			for _, s := range left {
				if s.Key() == items[0].Key() || (project && s.ID != "keep") {
					t.Fatalf("removed history restored: %+v", s)
				}
			}
			if project {
				// Moving another project there must not silently undo the removal.
				if err := db.Reconnect(left[0].ProjectID, destination); err == nil {
					t.Fatal("merged an excluded project")
				}
			}
		})
	}
}

func TestSessionMovedIntoRemovedProjectDoesNotRetainOldCache(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items := []session.Session{
		{ID: "a", Provider: session.Codex, Directory: t.TempDir()},
		{ID: "b", Provider: session.Codex, Directory: t.TempDir()},
	}
	if err := db.Upsert(items); err != nil {
		t.Fatal(err)
	}
	cached, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveProject(cached[0].ProjectID); err != nil {
		t.Fatal(err)
	}
	items[1].Directory = items[0].Directory
	if err := db.Upsert(items[1:]); err != nil {
		t.Fatal(err)
	}
	left, err := db.Load()
	if err != nil || len(left) != 0 {
		t.Fatalf("moved session retained stale visible cache: %+v, %v", left, err)
	}
}

func TestRemovingUnknownTargetsFails(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.RemoveSession("codex:missing") == nil || db.RemoveProject("missing") == nil {
		t.Fatal("unknown targets accepted")
	}
}
