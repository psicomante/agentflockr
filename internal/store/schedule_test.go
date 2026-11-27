package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A dashboard and a serve share the index: of two stores on one file, only
// one claims a due run, and the next run moves one interval on.
func TestClaimScheduleOncePerRunAcrossStores(t *testing.T) {
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
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	if err := a.SetSchedule(Schedule{Session: "claude:1", Prompt: "hi", Every: 5 * time.Hour, Next: now}); err != nil {
		t.Fatal(err)
	}
	_, first, err := a.ClaimSchedule("claude:1", now)
	if err != nil || !first {
		t.Fatalf("first claim: %v %v", first, err)
	}
	if _, second, err := b.ClaimSchedule("claude:1", now.Add(time.Minute)); err != nil || second {
		t.Fatalf("a second store claimed the same run: %v %v", second, err)
	}
	list, warnings, err := b.Schedules()
	if err != nil || len(warnings) != 0 || len(list) != 1 || !list[0].Next.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("schedules: %+v %v %v", list, warnings, err)
	}
	if _, due, _ := b.ClaimSchedule("claude:1", now.Add(5*time.Hour)); !due {
		t.Fatal("the next run was not due after one interval")
	}
}

// A run's result is recorded unless the schedule was replaced meanwhile.
func TestFinishScheduleKeepsAReplacedSchedule(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	s.SetSchedule(Schedule{Session: "opencode:ses_1", Prompt: "old", Every: time.Hour, Next: now})
	if err := s.FinishSchedule("opencode:ses_1", "old", now, errors.New("refused")); err != nil {
		t.Fatal(err)
	}
	list, _, _ := s.Schedules()
	if len(list) != 1 || list[0].LastError != "refused" || !list[0].Last.Equal(now) {
		t.Fatalf("result not recorded: %+v", list)
	}
	s.SetSchedule(Schedule{Session: "opencode:ses_1", Prompt: "new", Every: time.Hour, Next: now})
	s.FinishSchedule("opencode:ses_1", "old", now, nil)
	list, _, _ = s.Schedules()
	if list[0].Prompt != "new" || !list[0].Last.IsZero() {
		t.Fatalf("an old run overwrote the new schedule: %+v", list)
	}
	if err := s.DeleteSchedule("opencode:ses_1"); err != nil {
		t.Fatal(err)
	}
	if list, _, _ = s.Schedules(); len(list) != 0 {
		t.Fatalf("schedule not deleted: %+v", list)
	}
}

// A paused schedule is never claimed, and a profile schedule is keyed by
// its agent and profile.
func TestPausedAndProfileSchedules(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	profile := Schedule{Agent: "claude", Profile: "/home/me/.claude-work", Prompt: "hi", Every: time.Hour, Next: now}
	if err := db.SetSchedule(profile); err != nil {
		t.Fatal(err)
	}
	if found, err := db.PauseSchedule(profile.Key(), true); err != nil || !found {
		t.Fatalf("pause: %v %v", found, err)
	}
	if _, due, _ := db.ClaimSchedule(profile.Key(), now.Add(time.Hour)); due {
		t.Fatal("claimed a paused schedule")
	}
	db.PauseSchedule(profile.Key(), false)
	if _, due, _ := db.ClaimSchedule(profile.Key(), now.Add(time.Hour)); !due {
		t.Fatal("a resumed schedule was not due")
	}
	got, ok, err := db.Schedule("profile:claude:/home/me/.claude-work")
	if err != nil || !ok || !got.IsProfile() || got.Paused {
		t.Fatalf("schedule: %+v %v %v", got, ok, err)
	}
	if found, _ := db.PauseSchedule("claude:none", true); found {
		t.Fatal("paused a schedule that does not exist")
	}
	if db.SetSchedule(Schedule{Session: "claude:1", Agent: "claude", Profile: "work", Prompt: "hi", Every: time.Hour}) == nil {
		t.Fatal("saved a schedule with both a session and a profile")
	}
}
