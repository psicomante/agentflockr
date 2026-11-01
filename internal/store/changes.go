package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DeleteSession is called only after the agent deleted its own copy. The key
// stays recorded so an older scan, from this or another process, cannot put
// the session back.
func (s *Store) DeleteSession(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO deleted_sessions(key) VALUES(?) ON CONFLICT(key) DO NOTHING", key); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE key=?", key); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveSession forgets a session and excludes it from future discovery,
// without claiming that the agent's own history was deleted.
func (s *Store) RemoveSession(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO removed_sessions(key) SELECT key FROM sessions WHERE key=? ON CONFLICT(key) DO NOTHING", key)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("the session is no longer in the index")
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE key=?", key); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveProject keeps the project's identity and aliases as exclusion data,
// so new sessions at any of its paths stay out. Its session keys stay
// excluded even if an agent later reports them in another folder.
func (s *Store) RemoveProject(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO removed_projects(project_id) SELECT id FROM projects WHERE id=? ON CONFLICT(project_id) DO NOTHING", id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("the project is no longer in the index")
	}
	if _, err := tx.Exec("INSERT INTO removed_sessions(key) SELECT key FROM sessions WHERE project_id=? ON CONFLICT(key) DO NOTHING", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE project_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// Reconnect moves a project to a new folder in the index only. Every old path
// keeps resolving to the project, so an agent that still reports the old
// folder does not split it again. History already indexed at the destination
// joins the project.
func (s *Store) Reconnect(id, newPath string) error {
	if !filepath.IsAbs(newPath) {
		return fmt.Errorf("use an absolute folder path")
	}
	newPath = filepath.Clean(newPath)
	info, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("new folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the new location is not a folder")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow("SELECT path FROM projects WHERE id=?", id).Scan(&old); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if err := checkProjectIncluded(tx, id); err != nil {
		return err
	}
	if old == newPath {
		return nil
	}
	for _, alias := range pathAliases(newPath) {
		target, err := findProject(tx, []string{alias})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if target != "" && target != id {
			if err := checkProjectIncluded(tx, target); err != nil {
				return err
			}
			// Merge the destination's history and its path aliases.
			if _, err = tx.Exec("UPDATE sessions SET project_id=? WHERE project_id=?", id, target); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE aliases SET project_id=? WHERE project_id=?", id, target); err != nil {
				return err
			}
			if _, err = tx.Exec("DELETE FROM projects WHERE id=?", target); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("INSERT INTO aliases(path,project_id) VALUES(?,?) ON CONFLICT(path) DO UPDATE SET project_id=excluded.project_id", alias, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE projects SET path=? WHERE id=?", newPath, id); err != nil {
		return err
	}
	return tx.Commit()
}

// projectRemoved reports whether a project was removed from agentflockr.
// Every write that could bring one back asks it, so the rule lives here.
func projectRemoved(tx *sql.Tx, id string) (bool, error) {
	var removed bool
	err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM removed_projects WHERE project_id=?)", id).Scan(&removed)
	return removed, err
}

func checkProjectIncluded(tx *sql.Tx, id string) error {
	removed, err := projectRemoved(tx, id)
	if err != nil {
		return err
	}
	if removed {
		return fmt.Errorf("that project was removed from agentflockr")
	}
	return nil
}
