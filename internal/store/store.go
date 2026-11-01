// Package store keeps the local index of discovered sessions, grouped into
// projects, so history appears at startup before providers finish scanning.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/psicomante/agentflockr/internal/session"
	_ "modernc.org/sqlite"
)

// migrations[i] moves the index from schema version i to i+1. A released step
// is never edited; a change to the index is a new step at the end.
var migrations = []string{
	// 1: projects with their path aliases, indexed sessions, preferences.
	`CREATE TABLE projects (id TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE);
CREATE TABLE aliases (path TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id));
CREATE TABLE sessions (key TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), payload TEXT NOT NULL);
CREATE INDEX sessions_project ON sessions(project_id);
CREATE TABLE preferences (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	// 2: exclusions. A deleted or removed session, and every session of a
	// removed project, stays out of the index when an agent reports it again.
	`CREATE TABLE deleted_sessions (key TEXT PRIMARY KEY);
CREATE TABLE removed_sessions (key TEXT PRIMARY KEY);
CREATE TABLE removed_projects (project_id TEXT PRIMARY KEY REFERENCES projects(id));`,
}

// schemaVersion is stored in PRAGMA user_version. Open refuses an index
// written by a newer schema rather than guessing at its layout.
var schemaVersion = len(migrations)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	// The driver splits its options off at the first '?', so such a path
	// would open a different file without them.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("index path must not contain '?': %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Create privately before SQLite opens it, including on the first startup.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	// Other processes may share this index. DSN pragmas run on every pooled
	// connection, not just the first. Write transactions begin IMMEDIATE: a
	// deferred one that reads before writing cannot wait for another process's
	// write lock, so SQLite fails it with SQLITE_BUSY despite busy_timeout.
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch {
	case version == schemaVersion:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("the index was written by a newer agentflockr (schema %d); update agentflockr", version)
	}
	for _, step := range migrations[version:] {
		if _, err := tx.Exec(step); err != nil {
			return fmt.Errorf("upgrade the index from schema %d: %w", version, err)
		}
	}
	// PRAGMA takes no bound parameters; the value is a constant.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

// View returns the saved dashboard view ("sessions" or "projects"), or ""
// when none has been saved.
func (s *Store) View() (string, error)     { return s.preference("view") }
func (s *Store) SetView(view string) error { return s.setPreference("view", view) }

func (s *Store) preference(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM preferences WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) setPreference(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO preferences(key,value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Upsert records sessions under the project that owns their directory,
// creating the project on first sight. Sessions without an ID or an absolute
// directory cannot be grouped and are skipped, as are excluded ones.
func (s *Store) Upsert(sessions []session.Session) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range sessions {
		if item.ID == "" || !filepath.IsAbs(item.Directory) {
			continue
		}
		var excluded bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM deleted_sessions WHERE key=?
UNION ALL SELECT 1 FROM removed_sessions WHERE key=?)`, item.Key(), item.Key()).Scan(&excluded); err != nil {
			return err
		}
		if excluded {
			continue
		}
		path := filepath.Clean(item.Directory)
		paths := pathAliases(path)
		id, err := findProject(tx, paths)
		if errors.Is(err, sql.ErrNoRows) {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return err
			}
			id = hex.EncodeToString(random[:])
			if _, err := tx.Exec("INSERT INTO projects(id,path) VALUES(?,?)", id, path); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		removed, err := projectRemoved(tx, id)
		if err != nil {
			return err
		}
		if removed {
			// A previously indexed session may have moved into an excluded
			// project. Drop its old entry too, and keep it excluded.
			if _, err := tx.Exec("INSERT INTO removed_sessions(key) VALUES(?) ON CONFLICT(key) DO NOTHING", item.Key()); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM sessions WHERE key=?", item.Key()); err != nil {
				return err
			}
			continue
		}
		for _, alias := range paths {
			if _, err := tx.Exec("INSERT INTO aliases(path,project_id) VALUES(?,?) ON CONFLICT(path) DO NOTHING", alias, id); err != nil {
				return err
			}
		}
		item.ProjectID = id
		payload, err := json.Marshal(item)
		if err != nil {
			return err
		}
		// Agents report every session on every refresh, most of them
		// unchanged. The payload includes the project, so an equal payload
		// means there is nothing to write.
		if _, err := tx.Exec(`INSERT INTO sessions(key,project_id,payload) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET project_id=excluded.project_id,payload=excluded.payload
WHERE sessions.payload IS NOT excluded.payload`, item.Key(), id, string(payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load returns every indexed session as cached history: its status is
// unknown until a provider scan observes it again.
func (s *Store) Load() ([]session.Session, error) {
	items, _, err := s.LoadWithWarnings()
	return items, err
}

// LoadWithWarnings is Load, and also reports each row it had to skip because
// its payload could not be decoded. One damaged row must not hide the rest of
// the index.
func (s *Store) LoadWithWarnings() ([]session.Session, []string, error) {
	rows, err := s.db.Query("SELECT s.payload,p.id,p.path FROM sessions s JOIN projects p ON p.id=s.project_id ORDER BY s.key")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []session.Session
	var warnings []string
	for rows.Next() {
		var payload, id, path string
		if err := rows.Scan(&payload, &id, &path); err != nil {
			return nil, nil, err
		}
		var item session.Session
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped a cached session that could not be decoded: %v", err))
			continue
		}
		item.ProjectID, item.Directory = id, path
		item.Status, item.Cached = session.Unknown, true
		item.Evidence = "Cached history; awaiting a successful provider scan"
		out = append(out, item)
	}
	return out, warnings, rows.Err()
}

// pathAliases lists a directory and, when different, its resolved form, so a
// project reached through a symlink keeps one identity.
func pathAliases(path string) []string {
	paths := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
		paths = append(paths, real)
	}
	return paths
}

type queryRower interface {
	QueryRow(string, ...any) *sql.Row
}

func findProject(q queryRower, paths []string) (string, error) {
	for _, path := range paths {
		var id string
		err := q.QueryRow("SELECT project_id FROM aliases WHERE path=?", path).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", sql.ErrNoRows
}
