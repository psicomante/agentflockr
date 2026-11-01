package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
)

// press sends a named key, or a single character.
func press(name string) tea.KeyPressMsg {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight}
	if code, ok := codes[name]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return key(name)
}

// settle runs the reads a form starts, feeding each result back, until the
// form waits for nothing more. It must not run a start: that hands over the
// terminal.
func settle(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

// send delivers one message and runs what it starts, as settle does.
func send(m *Model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	settle(m, cmd)
}

// startingModel builds the dashboard with starting agents: Claude Code and
// Codex, plus OpenCode, which cannot start sessions. Each project's folder
// exists, so a start reaches the agent.
func startingModel(t *testing.T, demo bool) (*Model, *providertest.Starter, *providertest.Starter) {
	t.Helper()
	claude := &providertest.Starter{
		Profiles: []provider.Profile{{ID: "work", Name: "work", Detail: "~/.claude-work · claude-work"}},
		Catalogs: map[string]provider.Catalog{"work": {
			Models:         []provider.Model{{ID: "opus", Efforts: []string{"low", "high"}}, {ID: "haiku"}},
			DefaultEfforts: []string{"low", "high"},
		}},
	}
	codex := &providertest.Starter{
		Profiles: []provider.Profile{{ID: "", Name: "Codex default"}},
		Catalogs: map[string]provider.Catalog{"": {Models: []provider.Model{{ID: "gpt-x", Efforts: []string{"medium"}}}}},
	}
	registry := provider.Registry{
		providertest.WithStarter(&providertest.Fake{Provider: session.Claude}, claude),
		providertest.WithStarter(&providertest.Fake{Provider: session.Codex}, codex),
		&providertest.Fake{Provider: session.OpenCode},
	}
	dir := t.TempDir()
	sessions := fixture()
	for i := range sessions {
		if sessions[i].ProjectID == "api" {
			sessions[i].Directory = dir
			sessions[i].OriginalDirectory = dir
		}
	}
	engine := &app.Engine{Demo: demo, Providers: registry}
	snapshot := app.Snapshot{Sessions: sessions, UpdatedAt: now}
	if demo {
		snapshot = app.Demo()
	}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, snapshot)
	m.now = func() time.Time { return now }
	m.rebuild()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, claude, codex
}

// projectFolder is the folder the index records for a project's sessions.
func projectFolder(m *Model, id string) string {
	for _, s := range m.snapshot.Sessions {
		if s.ProjectID == id {
			return s.Directory
		}
	}
	return ""
}

// selectApiProject opens the Projects view and selects the api project.
func selectApiProject(t *testing.T, m *Model) {
	t.Helper()
	m.Update(key("tab"))
	if !m.projects {
		t.Fatal("the projects view did not open")
	}
	for i, it := range m.items {
		if it.project && it.session.ProjectID == "api" {
			m.cursor = i
			return
		}
	}
	t.Fatal("no api project row")
}

func TestNewSessionStartsInTheProjectAndComesBack(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	selectApiProject(t, m)
	send(m, key("n"))
	if m.mode != startingNew || !strings.Contains(screen(m), "Starts claude (work) with its default model at the default effort.") {
		t.Fatalf("the form did not open with the defaults:\n%s", screen(m))
	}
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatalf("enter did not start:\n%s", screen(m))
	}
	prepared, ok := cmd().(preparedMsg)
	if !ok || prepared.err != nil || prepared.command.Name != "true" {
		t.Fatalf("prepared %+v", prepared)
	}
	if _, cmd := m.Update(prepared); cmd == nil || m.mode != browsing {
		t.Fatalf("the agent must take the terminal once prepared; mode %v", m.mode)
	}
	started := claude.Started()
	if len(started) != 1 || started[0].Profile != "work" || started[0].Model != "" || started[0].Effort != "" {
		t.Fatalf("started %+v", started)
	}
	if started[0].Directory != projectFolder(m, "api") {
		t.Fatalf("started in %q, want the project folder", started[0].Directory)
	}
	m.Update(openedMsg{})
	if m.opening != "" || !strings.Contains(screen(m), "Back from claude.") {
		t.Fatalf("returning must say so:\n%s", screen(m))
	}
	if it, ok := m.selected(); !ok || !it.project || it.session.ProjectID != "api" {
		t.Fatal("the dashboard must come back on the same project")
	}
}

func TestNewSessionNeedsAProject(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	m.Update(key("n"))
	if m.mode != browsing || !strings.Contains(screen(m), "Choose a project in the Projects view to start a new session.") {
		t.Fatalf("n outside a project:\n%s", screen(m))
	}
	if len(claude.Calls()) != 0 {
		t.Fatalf("a read happened without a project: %v", claude.Calls())
	}
}

func TestNewSessionInDemoStartsNothing(t *testing.T) {
	m, claude, codex := startingModel(t, true)
	m.Update(key("n"))
	if m.mode != browsing || !strings.Contains(screen(m), "New sessions can't be started in demo mode.") {
		t.Fatalf("demo notice missing:\n%s", screen(m))
	}
	if len(claude.Calls()) != 0 || len(codex.Calls()) != 0 {
		t.Fatalf("demo mode read profiles or models: %v %v", claude.Calls(), codex.Calls())
	}
}

func TestNewSessionRevalidatesOnAgentChange(t *testing.T) {
	m, claude, codex := startingModel(t, false)
	selectApiProject(t, m)
	send(m, key("n"))
	// Model field, then opus; effort field, then high.
	send(m, press("down"))
	send(m, press("down"))
	send(m, press("right"))
	send(m, press("down"))
	send(m, press("right"))
	send(m, press("right"))
	if m.start.model != "opus" || m.start.effort != "high" {
		t.Fatalf("chose %q %q", m.start.model, m.start.effort)
	}
	// Back to the agent field and on to Codex, which does not offer opus.
	for i := 0; i < 3; i++ {
		send(m, press("up"))
	}
	send(m, press("right"))
	if m.start.agentID() != session.Codex {
		t.Fatalf("agent is %s", m.start.agentID())
	}
	if m.start.model != "opus" || m.start.effort != "high" {
		t.Fatalf("a choice was replaced: %q %q", m.start.model, m.start.effort)
	}
	if !strings.Contains(screen(m), "opus") || !strings.Contains(screen(m), "not available here") {
		t.Fatalf("the unavailable choice must be shown:\n%s", screen(m))
	}
	if _, cmd := m.Update(key("enter")); cmd != nil || !strings.Contains(m.start.err, "opus is not available here") {
		t.Fatalf("enter must refuse and say which choice to change: %q", m.start.err)
	}
	if len(claude.Started())+len(codex.Started()) != 0 {
		t.Fatal("an unavailable choice was started")
	}
}

func TestNewSessionCancelStartsNothing(t *testing.T) {
	m, claude, codex := startingModel(t, false)
	selectApiProject(t, m)
	send(m, key("n"))
	m.Update(press("esc"))
	if m.mode != browsing {
		t.Fatal("esc must close the form")
	}
	// A read that finishes after the form closed is ignored.
	stale := startProfilesMsg{agent: session.Claude, seq: m.startSeq, profiles: []provider.Profile{{ID: "late"}}}
	m.Update(stale)
	if len(m.start.profiles) != 0 || m.mode != browsing {
		t.Fatal("a read from a closed form changed the dashboard")
	}
	if len(claude.Started())+len(codex.Started()) != 0 {
		t.Fatal("cancel started a session")
	}
}

func TestNewSessionStartsOnce(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	selectApiProject(t, m)
	send(m, key("n"))
	if _, cmd := m.Update(key("enter")); cmd == nil {
		t.Fatal("first enter must start")
	}
	if _, again := m.Update(key("enter")); again != nil {
		t.Fatal("a second enter while starting must do nothing")
	}
	if _, moved := m.Update(press("down")); moved != nil || m.start.field != fieldAgent {
		t.Fatal("the form must not change while the agent starts")
	}
	if len(claude.Started()) != 0 {
		t.Fatal("the start ran before its command was prepared")
	}
}

func TestStartFailureNamesTheFolder(t *testing.T) {
	m, _, _ := startingModel(t, false)
	selectApiProject(t, m)
	// The project's folder moves away after the form has loaded.
	for i := range m.snapshot.Sessions {
		if m.snapshot.Sessions[i].ProjectID == "api" {
			m.snapshot.Sessions[i].Directory = "/no/such/folder"
		}
	}
	m.rebuild()
	send(m, key("n"))
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	want := "Could not start: the project folder no longer exists: /no/such/folder. Press m to choose its new folder."
	if m.mode != browsing || m.opening != "" || !strings.Contains(screen(m), want) {
		t.Fatalf("a failed start must close the form and say why:\n%s", screen(m))
	}
}

// REQ-OPN-008: when the models cannot be read, the form says why and offers
// only Default, and Default starts. A model kept from another agent cannot
// be checked, so it is refused until the person moves back to Default.
func TestNewSessionStartsOnDefaultsWhenModelsCannotBeRead(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	claude.CatalogErr = errors.New("models unavailable")
	selectApiProject(t, m)
	send(m, key("n"))
	if !strings.Contains(screen(m), "Could not read models") {
		t.Fatalf("the form must say why the models are missing:\n%s", screen(m))
	}

	// Choose Codex's model, then go back to Claude Code, keeping it.
	send(m, press("right"))
	send(m, press("down"))
	send(m, press("down"))
	send(m, press("right"))
	if m.start.model != "gpt-x" {
		t.Fatalf("model %q, want gpt-x", m.start.model)
	}
	send(m, press("up"))
	send(m, press("up"))
	send(m, press("right"))
	if _, cmd := m.Update(key("enter")); cmd != nil {
		t.Fatal("a model the form could not check was started")
	}

	send(m, press("down"))
	send(m, press("down"))
	send(m, press("right"))
	if m.start.model != "" {
		t.Fatalf("model %q, want Default", m.start.model)
	}
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatalf("Default did not start:\n%s", screen(m))
	}
	if prepared, ok := cmd().(preparedMsg); !ok || prepared.err != nil {
		t.Fatalf("prepared %+v", prepared)
	}
	if started := claude.Started(); len(started) != 1 || started[0].Model != "" || started[0].Effort != "" {
		t.Fatalf("started %+v, want the defaults", started)
	}
}

// A start the engine refuses ran nothing, so the form stays open with the
// reason and every choice, ready to change one and try again.
func TestRefusedStartKeepsTheForm(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	claude.StartErr = errors.New("that profile is no longer offered")
	selectApiProject(t, m)
	send(m, key("n"))
	send(m, press("down"))
	send(m, press("down"))
	send(m, press("right"))
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.mode != startingNew || m.opening != "" || m.start.model != "opus" {
		t.Fatalf("the form must stay open with its choices: mode %v, model %q", m.mode, m.start.model)
	}
	if !strings.Contains(screen(m), "Could not start: that profile is no longer offered") {
		t.Fatalf("the form must say why:\n%s", screen(m))
	}
}

// When the agent created a session before its command failed, the notice
// says the session exists and is kept.
func TestFailedStartMentionsTheCreatedSession(t *testing.T) {
	m, _, _ := startingModel(t, false)
	m.opening = "OpenCode"
	m.Update(openedMsg{err: errors.New("exit status 1"), created: "ses_new1"})
	if !strings.Contains(screen(m), "OpenCode exited: exit status 1. It had created session ses_new1, which is kept.") {
		t.Fatalf("the notice must name the created session:\n%s", screen(m))
	}
}

// A model's ID comes from the agent, so it is shown only once made safe.
func TestModelIDsAreShownSafely(t *testing.T) {
	m, _, _ := startingModel(t, false)
	f := &startForm{
		model:   "x\x1b[31mred",
		catalog: provider.Catalog{Models: []provider.Model{{ID: "x\x1b[31mred", Name: "Red"}}},
	}
	if out := m.modelValue(paletteFor(true), f); strings.Contains(out, "\x1b[31m") {
		t.Fatalf("an escape sequence from a model ID reached the screen: %q", out)
	}
}

// A profile with no default model (an OpenCode connection) offers no
// "Default": the form starts on its first model and never shows a choice
// that Enter would refuse. A model the form picked is dropped again when the
// person moves to another profile; it was never their choice.
func TestRequiredModelIsPreselected(t *testing.T) {
	m, claude, _ := startingModel(t, false)
	claude.Profiles = append(claude.Profiles, provider.Profile{ID: "conn", Name: "conn"})
	claude.Catalogs["conn"] = provider.Catalog{ModelRequired: true, Models: []provider.Model{{ID: "m1"}, {ID: "m2"}}}
	selectApiProject(t, m)
	send(m, key("n"))
	send(m, press("down"))
	send(m, press("right")) // profile: conn
	if m.start.model != "m1" {
		t.Fatalf("model %q, want the connection's first model", m.start.model)
	}
	if values := m.start.modelValues(); !reflect.DeepEqual(values, []string{"m1", "m2"}) {
		t.Fatalf("model choices %q, want no Default", values)
	}
	send(m, press("left")) // profile: work
	if m.start.model != "" {
		t.Fatalf("model %q kept on another profile, want Default", m.start.model)
	}
	send(m, press("right")) // conn again
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatalf("enter did not start:\n%s", screen(m))
	}
	if prepared, ok := cmd().(preparedMsg); !ok || prepared.err != nil {
		t.Fatalf("prepared %+v", prepared)
	}
	if started := claude.Started(); len(started) != 1 || started[0].Profile != "conn" || started[0].Model != "m1" {
		t.Fatalf("started %+v", started)
	}
}
