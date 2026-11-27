package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// schedulePrefix starts the preference key of each scheduled prompt; the
// session's key follows it. Schedules live in the preferences table, which
// every schema has, so they need no migration of their own.
const schedulePrefix = "scheduled-prompt:"

// Schedule is a prompt the person wrote, sent at a fixed interval into one
// session, or through one agent profile outside any session.
type Schedule struct {
	// Session is the session's key, or "" for a profile schedule.
	Session string `json:"session,omitempty"`
	// Agent and Profile name the profile a profile schedule runs through,
	// and ProfileName how the form showed it. All empty for a session.
	Agent       string        `json:"agent,omitempty"`
	Profile     string        `json:"profile,omitempty"`
	ProfileName string        `json:"profile_name,omitempty"`
	Prompt      string        `json:"prompt"`
	Every       time.Duration `json:"every"`
	// Connection is the OpenCode credential ID to switch to for each run, and
	// ConnectionName how the form named it. Both are empty for other agents.
	Connection     string `json:"connection,omitempty"`
	ConnectionName string `json:"connection_name,omitempty"`
	// Model and Effort are used for each run only; empty leaves the choice
	// to the session or profile.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Paused schedules keep everything but are never claimed.
	Paused bool      `json:"paused,omitempty"`
	Next   time.Time `json:"next"`
	Last   time.Time `json:"last,omitzero"`
	// LastError is why the last run failed, or "" when it succeeded.
	LastError string `json:"last_error,omitempty"`
}

// profileKey starts the key of a profile schedule; a session key starts with
// its agent's ID, which is never "profile".
const profileKey = "profile:"

// Key identifies the schedule: the session's key, or "profile:AGENT:ID".
// The ID may be empty: Codex's and OpenCode's default profiles have none.
func (sc Schedule) Key() string {
	if sc.Session != "" {
		return sc.Session
	}
	if sc.Agent == "" {
		return ""
	}
	return profileKey + sc.Agent + ":" + sc.Profile
}

// IsProfile reports whether the schedule runs through a profile rather than
// into a session.
func (sc Schedule) IsProfile() bool { return sc.Session == "" }

// Schedules returns every scheduled prompt, ordered by session key. A row
// that cannot be read is skipped and named in the warnings.
func (s *Store) Schedules() ([]Schedule, []string, error) {
	rows, err := s.db.Query("SELECT key, value FROM preferences WHERE substr(key, 1, ?) = ?", len(schedulePrefix), schedulePrefix)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Schedule
	var warnings []string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, nil, err
		}
		var sc Schedule
		if err := json.Unmarshal([]byte(value), &sc); err != nil || sc.Key() == "" || sc.Key() != strings.TrimPrefix(key, schedulePrefix) {
			warnings = append(warnings, "skipped an unreadable scheduled prompt")
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, warnings, rows.Err()
}

// Schedule returns the schedule with the key, and false when there is none.
func (s *Store) Schedule(key string) (Schedule, bool, error) {
	value, err := s.preference(schedulePrefix + key)
	if err != nil || value == "" {
		return Schedule{}, false, err
	}
	var sc Schedule
	if err := json.Unmarshal([]byte(value), &sc); err != nil || sc.Key() != key {
		return Schedule{}, false, fmt.Errorf("read scheduled prompt: unreadable")
	}
	return sc, true, nil
}

// SetSchedule saves sc, replacing the earlier schedule with the same key.
func (s *Store) SetSchedule(sc Schedule) error {
	if sc.Key() == "" || sc.Every <= 0 {
		return fmt.Errorf("a scheduled prompt needs a session or profile and an interval")
	}
	if sc.Session != "" && (sc.Agent != "" || sc.Profile != "") {
		// A session schedule's key is the session's, so a profile would be
		// silently ignored.
		return fmt.Errorf("a scheduled prompt goes into a session or through a profile, not both")
	}
	value, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return s.setPreference(schedulePrefix+sc.Key(), string(value))
}

// DeleteSchedule forgets the scheduled prompt with the key, if there is one.
func (s *Store) DeleteSchedule(key string) error {
	_, err := s.db.Exec("DELETE FROM preferences WHERE key=?", schedulePrefix+key)
	return err
}

// PauseSchedule pauses or resumes the schedule with the key. A resumed
// schedule whose next run has passed is due at once. It reports false when
// there is no such schedule.
func (s *Store) PauseSchedule(key string, paused bool) (bool, error) {
	found := false
	err := s.updateSchedule(key, func(sc *Schedule) bool {
		found = true
		sc.Paused = paused
		return true
	})
	return found, err
}

// ClaimSchedule reserves the run of the schedule with the key that is due
// at now: it moves the next run one interval past now and returns the
// schedule as it was. It returns false when the schedule is gone, paused or
// not due. The read and the write
// share one write transaction, so of a dashboard and a serve sharing the
// index, only one claims each run.
func (s *Store) ClaimSchedule(key string, now time.Time) (Schedule, bool, error) {
	var claimed Schedule
	ok := false
	err := s.updateSchedule(key, func(sc *Schedule) bool {
		if sc.Paused || sc.Next.After(now) {
			return false
		}
		claimed, ok = *sc, true
		sc.Next = now.Add(sc.Every)
		return true
	})
	return claimed, ok, err
}

// FinishSchedule records how a run that started at the schedule's claimed
// time ended. A schedule replaced or deleted meanwhile is left alone.
func (s *Store) FinishSchedule(key, prompt string, at time.Time, runErr error) error {
	return s.updateSchedule(key, func(sc *Schedule) bool {
		if sc.Prompt != prompt {
			return false
		}
		sc.Last, sc.LastError = at, ""
		if runErr != nil {
			sc.LastError = runErr.Error()
		}
		return true
	})
}

// updateSchedule applies change to the schedule with the key inside one
// write transaction, and saves it when change returns true.
func (s *Store) updateSchedule(key string, change func(*Schedule) bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key = schedulePrefix + key
	var value string
	err = tx.QueryRow("SELECT value FROM preferences WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var sc Schedule
	if err := json.Unmarshal([]byte(value), &sc); err != nil {
		return fmt.Errorf("read scheduled prompt: %w", err)
	}
	if !change(&sc) {
		return nil
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE preferences SET value=? WHERE key=?", string(data), key); err != nil {
		return err
	}
	return tx.Commit()
}
