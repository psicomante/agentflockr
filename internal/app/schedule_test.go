package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

// prompter is a fake agent that takes scheduled prompts.
type prompter struct {
	*providertest.Fake
	mu   sync.Mutex
	sent []provider.ScheduledPrompt
}

func (p *prompter) Prompt(_ context.Context, _ session.Session, sp provider.ScheduledPrompt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, sp)
	return nil
}

// promptModels is the fake agent's catalogue: opus takes efforts, haiku none.
var promptModels = provider.Catalog{Models: []provider.Model{
	{ID: "opus", Name: "Opus", Efforts: []string{"low", "high"}},
	{ID: "haiku", Name: "Haiku"},
}}

func (p *prompter) PromptCatalog(context.Context, session.Session) (provider.Catalog, error) {
	return promptModels, nil
}

func (p *prompter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

func scheduleEngine(t *testing.T, status session.Status) (*Engine, *prompter, session.Session) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	item := session.Session{ID: "s1", Provider: session.Claude, Directory: dir, OriginalDirectory: dir, Status: status}
	p := &prompter{Fake: &providertest.Fake{Provider: session.Claude, ScanFunc: func(context.Context, time.Time) provider.Result {
		return provider.Result{Provider: session.Claude, Sessions: []session.Session{item}}
	}}}
	return &Engine{Store: db, Providers: provider.Registry{p}}, p, item
}

func runDue(e *Engine) {
	var runs sync.WaitGroup
	e.dueSchedules(context.Background(), &runs)
	runs.Wait()
}

func TestScheduledPromptRunsOnceEachInterval(t *testing.T) {
	e, p, item := scheduleEngine(t, session.Idle)
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	if err := e.SetSchedule(context.Background(), ScheduleTarget{Session: &item}, ScheduleInput{Prompt: "ping", Every: 5 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	runDue(e)
	if p.count() != 0 {
		t.Fatal("ran before any scan said the session is idle")
	}
	e.Scan(context.Background())
	runDue(e)
	runDue(e)
	if p.count() != 1 || p.sent[0].Text != "ping" {
		t.Fatalf("first run: %+v", p.sent)
	}
	now = now.Add(5 * time.Hour)
	runDue(e)
	if p.count() != 2 {
		t.Fatalf("second run: %+v", p.sent)
	}
	list, _ := e.Schedules()
	if len(list) != 1 || !list[0].Last.Equal(now) || list[0].LastError != "" {
		t.Fatalf("run not recorded: %+v", list)
	}
}

func TestScheduledPromptWaitsForARunningTurn(t *testing.T) {
	e, p, item := scheduleEngine(t, session.Working)
	if err := e.SetSchedule(context.Background(), ScheduleTarget{Session: &item}, ScheduleInput{Prompt: "ping", Every: time.Hour}); err != nil {
		t.Fatal(err)
	}
	e.Scan(context.Background())
	runDue(e)
	if p.count() != 0 {
		t.Fatal("sent into a running turn")
	}
}

func TestSetScheduleChecksItsInput(t *testing.T) {
	e, _, item := scheduleEngine(t, session.Idle)
	ctx, target := context.Background(), ScheduleTarget{Session: &item}
	for name, in := range map[string]ScheduleInput{
		"empty prompt":             {Prompt: " ", Every: time.Hour},
		"short interval":           {Prompt: "ping", Every: time.Minute},
		"claude with a connection": {Prompt: "ping", Every: time.Hour, Connection: "cred_1", ConnectionName: "Work"},
		"a model not offered":      {Prompt: "ping", Every: time.Hour, Model: "gpt"},
		"effort without the model": {Prompt: "ping", Every: time.Hour, Model: "haiku", Effort: "max"},
	} {
		if e.SetSchedule(ctx, target, in) == nil {
			t.Errorf("%s was saved", name)
		}
	}
	codex := session.Session{ID: "c1", Provider: session.Codex}
	if e.SetSchedule(ctx, ScheduleTarget{Session: &codex}, ScheduleInput{Prompt: "ping", Every: time.Hour}) == nil {
		t.Error("saved a schedule for an agent that takes none")
	}
}

// profilePrompter is a fake agent that also runs prompts through profiles.
type profilePrompter struct {
	*prompter
	dirs     []string
	profiles []string
}

func (p *profilePrompter) StartProfiles(context.Context) ([]provider.Profile, error) {
	return []provider.Profile{{ID: "work", Name: "work"}, {ID: "broken", Name: "broken", Problem: "its home folder does not exist"}}, nil
}

func (p *profilePrompter) StartCatalog(context.Context, string) (provider.Catalog, error) {
	return promptModels, nil
}

func (p *profilePrompter) Start(context.Context, provider.NewSession) (provider.Command, error) {
	return provider.Command{}, provider.ErrUnsupported
}

func (p *profilePrompter) PromptProfile(_ context.Context, profile, dir string, sp provider.ScheduledPrompt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, sp)
	p.dirs = append(p.dirs, dir)
	p.profiles = append(p.profiles, profile)
	return nil
}

func TestScheduledPromptCarriesItsModelAndEffort(t *testing.T) {
	e, p, item := scheduleEngine(t, session.Idle)
	in := ScheduleInput{Prompt: "ping", Every: time.Hour, Model: "opus", Effort: "high"}
	if err := e.SetSchedule(context.Background(), ScheduleTarget{Session: &item}, in); err != nil {
		t.Fatal(err)
	}
	e.Scan(context.Background())
	runDue(e)
	if p.count() != 1 || p.sent[0].Model != "opus" || p.sent[0].Effort != "high" {
		t.Fatalf("sent: %+v", p.sent)
	}
}

func TestPausedScheduleWaitsUntilResumed(t *testing.T) {
	e, p, item := scheduleEngine(t, session.Idle)
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	if err := e.SetSchedule(context.Background(), ScheduleTarget{Session: &item}, ScheduleInput{Prompt: "ping", Every: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := e.PauseSchedule(item.Key(), true); err != nil {
		t.Fatal(err)
	}
	e.Scan(context.Background())
	now = now.Add(3 * time.Hour)
	runDue(e)
	if p.count() != 0 {
		t.Fatal("a paused schedule was sent")
	}
	if err := e.PauseSchedule(item.Key(), false); err != nil {
		t.Fatal(err)
	}
	runDue(e)
	if p.count() != 1 {
		t.Fatalf("a resumed schedule whose run had passed was not sent: %d", p.count())
	}
	if e.PauseSchedule("claude:gone", true) == nil {
		t.Fatal("paused a schedule that does not exist")
	}
}

func TestEditingKeepsTheLastRunAndThePause(t *testing.T) {
	e, _, item := scheduleEngine(t, session.Idle)
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	ctx, target := context.Background(), ScheduleTarget{Session: &item}
	if err := e.SetSchedule(ctx, target, ScheduleInput{Prompt: "ping", Every: 5 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	e.Scan(ctx)
	runDue(e)
	if err := e.PauseSchedule(item.Key(), true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if err := e.SetSchedule(ctx, target, ScheduleInput{Prompt: "pong", Every: 2 * time.Hour, Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.Schedules()
	sc := list[0]
	if !sc.Paused || sc.Prompt != "pong" || sc.Model != "haiku" || sc.Last.IsZero() || !sc.Next.Equal(sc.Last.Add(2*time.Hour)) {
		t.Fatalf("edited: %+v", sc)
	}
}

func TestProfileScheduleRunsOutsideAnySession(t *testing.T) {
	e, base, _ := scheduleEngine(t, session.Idle)
	p := &profilePrompter{prompter: base}
	e.Providers = provider.Registry{p}
	e.ScheduleDir = filepath.Join(t.TempDir(), "scheduled-prompts")
	ctx := context.Background()
	target := ScheduleTarget{Agent: session.Claude, Profile: "work", ProfileName: "work"}
	if err := e.SetSchedule(ctx, target, ScheduleInput{Prompt: "ping", Every: 5 * time.Hour, Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]ScheduleTarget{
		"unlisted profile": {Agent: session.Claude, Profile: "/etc"},
		"broken profile":   {Agent: session.Claude, Profile: "broken"},
		"other agent":      {Agent: session.Codex, Profile: "work"},
	} {
		if e.SetSchedule(ctx, bad, ScheduleInput{Prompt: "ping", Every: time.Hour}) == nil {
			t.Errorf("%s was saved", name)
		}
	}
	e.Scan(ctx)
	runDue(e)
	if len(p.profiles) != 1 || p.profiles[0] != "work" || p.dirs[0] != e.ScheduleDir || p.sent[0].Model != "opus" {
		t.Fatalf("profile run: %+v %+v %+v", p.profiles, p.dirs, p.sent)
	}
	if info, err := os.Stat(e.ScheduleDir); err != nil || !info.IsDir() {
		t.Fatalf("the run folder was not made: %v", err)
	}
	list, _ := e.Schedules()
	if len(list) != 1 || list[0].Key() != "profile:claude:work" || list[0].LastError != "" || list[0].Last.IsZero() {
		t.Fatalf("schedules: %+v", list)
	}
}
