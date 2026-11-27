package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

// promptingAgent is a fake OpenCode that takes scheduled prompts and lists two
// saved connections.
type promptingAgent struct{ *providertest.Fake }

func (promptingAgent) Prompt(context.Context, session.Session, provider.ScheduledPrompt) error {
	return nil
}

func (promptingAgent) Connections(context.Context) ([]provider.Connection, error) {
	return []provider.Connection{
		{ID: "cred_work", Name: "Work (OAuth)", Integration: "anthropic", Active: true},
		{ID: "cred_home", Name: "Home (OAuth)", Integration: "anthropic"},
	}, nil
}

func (promptingAgent) PromptCatalog(context.Context, session.Session) (provider.Catalog, error) {
	return provider.Catalog{Models: []provider.Model{
		{ID: "anthropic/claude-opus", Name: "Claude Opus", Efforts: []string{"low", "high"}},
		{ID: "anthropic/claude-haiku", Name: "Claude Haiku"},
	}}, nil
}

// profileAgent is a fake Claude Code that takes scheduled prompts into its
// sessions and through its profiles.
type profileAgent struct{ startingAdapter }

type startingAdapter interface {
	provider.Adapter
	provider.Starter
}

func (profileAgent) Prompt(context.Context, session.Session, provider.ScheduledPrompt) error {
	return nil
}

func (profileAgent) PromptProfile(context.Context, string, string, provider.ScheduledPrompt) error {
	return nil
}

// drain runs cmd and the reads and saves it starts, feeding each result
// back. A cursor's blink never settles, so each command gets a second and
// whatever is not one of the form's own results is dropped.
func drain(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(time.Second):
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(m, c)
		}
		return
	}
	switch msg.(type) {
	case connectionsMsg, scheduleCatalogMsg, scheduleProfilesMsg, scheduleSavedMsg, schedulesMsg, viewSavedMsg:
		_, next := m.Update(msg)
		drain(m, next)
	}
}

func openSchedule(m *Model) {
	_, cmd := m.Update(key("p"))
	drain(m, cmd)
}

// scheduleModel is a dashboard on a real index with an OpenCode and a
// Claude Code that take scheduled prompts.
func scheduleModel(t *testing.T) (*Model, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	claude := profileAgent{providertest.WithStarter(&providertest.Fake{Provider: session.Claude}, &providertest.Starter{
		Profiles: []provider.Profile{{ID: "work", Name: "work", Detail: "~/.claude-work · claude"}},
		Catalogs: map[string]provider.Catalog{"work": {Models: []provider.Model{{ID: "opus", Efforts: []string{"high"}}}, DefaultEfforts: []string{"high"}}},
	}).(startingAdapter)}
	// Codex stands in for an agent whose default profile has no default
	// model, as an OpenCode connection has none.
	codex := profileAgent{providertest.WithStarter(&providertest.Fake{Provider: session.Codex}, &providertest.Starter{
		Profiles: []provider.Profile{{ID: "", Name: "Codex default", Detail: "~/.codex"}},
		Catalogs: map[string]provider.Catalog{"": {Models: []provider.Model{{ID: "gpt-x"}}, ModelRequired: true}},
	}).(startingAdapter)}
	engine := &app.Engine{Store: db, Providers: provider.Registry{claude, promptingAgent{&providertest.Fake{Provider: session.OpenCode}}, codex}}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: fixture(), UpdatedAt: now})
	m.now = func() time.Time { return now }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, db
}

func TestScheduledPromptFormSavesPromptIntervalAndConnection(t *testing.T) {
	m, db := scheduleModel(t)
	m.selectKey("opencode:idle")

	openSchedule(m)
	if m.mode != scheduling || len(m.schedule.connections) != 2 {
		t.Fatalf("form not open with its connections: mode %v %+v", m.mode, m.schedule.connections)
	}
	typeText(m, "keep the window open")
	m.Update(key("tab"))
	for range 2 {
		m.Update(key("backspace"))
	}
	typeText(m, "4h30m")
	m.Update(key("tab"))
	m.Update(press("right"))
	m.Update(press("right"))
	if !strings.Contains(m.render(), "Home (OAuth)") {
		t.Fatal("the chosen connection is not shown")
	}
	m.Update(key("tab"))
	m.Update(press("right"))
	m.Update(key("tab"))
	m.Update(press("right"))
	m.Update(press("right"))
	if out := m.render(); !strings.Contains(out, "Claude Opus") || !strings.Contains(out, "high") {
		t.Fatalf("the chosen model and effort are not shown:\n%s", out)
	}
	_, cmd := m.Update(key("enter"))
	drain(m, cmd)
	if m.mode != browsing {
		t.Fatalf("form still open: %s", m.schedule.err)
	}
	list, _, err := db.Schedules()
	if err != nil || len(list) != 1 {
		t.Fatalf("schedules: %+v %v", list, err)
	}
	sc := list[0]
	if sc.Session != "opencode:idle" || sc.Prompt != "keep the window open" || sc.Every != 270*time.Minute ||
		sc.Connection != "cred_home" || sc.ConnectionName != "Home (OAuth)" || sc.Model != "anthropic/claude-opus" || sc.Effort != "high" {
		t.Fatalf("saved: %+v", sc)
	}
	if !strings.Contains(m.render(), "Scheduled prompt · p to change") {
		t.Fatal("the details pane does not show the schedule")
	}

	openSchedule(m)
	if m.schedule.prompt.Value() != "keep the window open" || m.schedule.connection != 1 || m.schedule.model != "anthropic/claude-opus" {
		t.Fatalf("form not filled from the saved schedule: %q %d %q", m.schedule.prompt.Value(), m.schedule.connection, m.schedule.model)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	drain(m, cmd)
	if list, _, _ := db.Schedules(); len(list) != 0 || m.mode != browsing {
		t.Fatalf("not stopped: %+v", list)
	}
}

func TestScheduledPromptIsRefusedForAgentsWithoutIt(t *testing.T) {
	m := newModel(t, fixture())
	m.selectKey("codex:busy")
	send(m, key("p"))
	if m.mode != browsing || !strings.Contains(m.notice, "can't have scheduled prompts") {
		t.Fatalf("mode %v notice %q", m.mode, m.notice)
	}
}

func TestSchedulesViewPausesResumesEditsAndDeletes(t *testing.T) {
	m, db := scheduleModel(t)
	for _, sc := range []store.Schedule{
		{Session: "claude:done", Prompt: "continue", Every: 5 * time.Hour, Next: now.Add(time.Hour), Model: "opus"},
		{Agent: "claude", Profile: "work", ProfileName: "work", Prompt: "ping", Every: 5 * time.Hour, Next: now.Add(2 * time.Hour)},
	} {
		if err := db.SetSchedule(sc); err != nil {
			t.Fatal(err)
		}
	}
	_, cmd := m.Update(key("s"))
	drain(m, cmd)
	out := ansi.Strip(m.render())
	if !m.scheduled || len(m.schedList) != 2 || !strings.Contains(out, "Schedules") || !strings.Contains(out, "profile work") {
		t.Fatalf("the Schedules view did not open with both schedules:\n%s", out)
	}
	if view, _ := db.View(); view != "schedules" {
		t.Fatalf("view saved as %q", view)
	}

	// The first row runs soonest: the session's schedule.
	_, cmd = m.Update(key(" "))
	drain(m, cmd)
	if sc, _, _ := db.Schedule("claude:done"); !sc.Paused {
		t.Fatal("space did not pause the schedule")
	}
	if !strings.Contains(ansi.Strip(m.render()), "paused") {
		t.Fatal("the view does not show the pause")
	}
	// Paused schedules sort last, so the selection followed it there.
	if sc, _ := m.selectedSchedule(); sc.Key() != "claude:done" {
		t.Fatalf("selection moved to %s", sc.Key())
	}
	_, cmd = m.Update(key(" "))
	drain(m, cmd)
	if sc, _, _ := db.Schedule("claude:done"); sc.Paused {
		t.Fatal("space did not resume the schedule")
	}

	_, cmd = m.Update(key("enter"))
	drain(m, cmd)
	if m.mode != scheduling || m.schedule.prompt.Value() != "continue" || m.schedule.model != "opus" {
		t.Fatalf("enter did not open the saved schedule: mode %v %q", m.mode, m.schedule.prompt.Value())
	}
	m.Update(key("esc"))

	m.Update(key("d"))
	if m.mode != confirmUnschedule {
		t.Fatal("d did not ask before deleting")
	}
	m.Update(key("esc"))
	if list, _, _ := db.Schedules(); len(list) != 2 {
		t.Fatal("esc deleted the schedule")
	}
	m.Update(key("d"))
	_, cmd = m.Update(key("y"))
	drain(m, cmd)
	if list, _, _ := db.Schedules(); len(list) != 1 || !list[0].IsProfile() {
		t.Fatalf("not deleted: %+v", list)
	}

	_, cmd = m.Update(key("s"))
	drain(m, cmd)
	if m.scheduled || m.projects {
		t.Fatal("s did not go back to the sessions")
	}
}

func TestProfileScheduleIsMadeFromTheSchedulesView(t *testing.T) {
	m, db := scheduleModel(t)
	_, cmd := m.Update(key("s"))
	drain(m, cmd)
	_, cmd = m.Update(key("n"))
	drain(m, cmd)
	if m.mode != scheduling || m.schedule.target.Profile != "work" {
		t.Fatalf("the form did not open on the profile: mode %v %+v", m.mode, m.schedule.target)
	}
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Scheduled prompt on a profile") || !strings.Contains(out, "~/.claude-work") {
		t.Fatalf("the form does not show the profile:\n%s", out)
	}
	// The form opens on the prompt; the model is two fields further down.
	typeText(m, "ping")
	m.Update(key("tab"))
	m.Update(key("tab"))
	m.Update(press("right"))
	_, cmd = m.Update(key("enter"))
	drain(m, cmd)
	if m.mode != browsing {
		t.Fatalf("form still open: %s", m.schedule.err)
	}
	list, _, _ := db.Schedules()
	if len(list) != 1 || list[0].Key() != "profile:claude:work" || list[0].Prompt != "ping" || list[0].Model != "opus" {
		t.Fatalf("saved: %+v", list)
	}
}

var _ provider.ProfilePrompter = profileAgent{}

func TestProfileScheduleOffersEveryAgentThatTakesOne(t *testing.T) {
	m, db := scheduleModel(t)
	_, cmd := m.Update(key("s"))
	drain(m, cmd)
	_, cmd = m.Update(key("n"))
	drain(m, cmd)
	if len(m.schedule.agents) != 2 || m.schedule.target.Agent != session.Claude {
		t.Fatalf("agents offered: %v, chosen %s", m.schedule.agents, m.schedule.target.Agent)
	}
	typeText(m, "ping")
	m.Update(press("up"))
	m.Update(press("up"))
	_, cmd = m.Update(press("right"))
	drain(m, cmd)
	f := m.schedule
	if f.target.Agent != session.Codex || f.target.Profile != "" || f.profileState != loaded || f.model != "gpt-x" {
		t.Fatalf("after choosing Codex: agent %s profile %q state %v model %q", f.target.Agent, f.target.Profile, f.profileState, f.model)
	}
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Codex default") {
		t.Fatalf("the Codex profile is not shown:\n%s", out)
	}
	_, cmd = m.Update(key("enter"))
	drain(m, cmd)
	if m.mode != browsing {
		t.Fatalf("form still open: %s", m.schedule.err)
	}
	list, _, _ := db.Schedules()
	if len(list) != 1 || list[0].Key() != "profile:codex:" || list[0].Model != "gpt-x" || list[0].ProfileName != "Codex default" {
		t.Fatalf("saved: %+v", list)
	}
}
