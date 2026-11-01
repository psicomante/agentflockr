package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(key(string(r)))
	}
}

func fake(id string, p session.Provider, project string, status session.Status, age time.Duration) session.Session {
	return session.Session{ID: id, Provider: p, ProjectID: project, Directory: "/work/" + project, OriginalDirectory: "/work/" + project,
		Title: "Title " + id, Summary: "Summary " + id, Status: status, Evidence: "Evidence " + id, UpdatedAt: now.Add(-age)}
}

func fixture() []session.Session {
	items := []session.Session{
		fake("old", session.Codex, "api", session.Completed, 3*24*time.Hour),
		fake("done", session.Claude, "api", session.Completed, time.Hour),
		fake("idle", session.OpenCode, "web", session.Idle, 10*time.Minute),
		fake("busy", session.Codex, "web", session.Working, time.Minute),
		fake("failed", session.Codex, "api", session.Failed, 2*time.Minute),
		fake("waiting", session.Claude, "web", session.Waiting, 30*time.Minute),
	}
	app.Sort(items)
	return items
}

func registry() provider.Registry {
	return provider.Registry{
		&providertest.Fake{Provider: session.OpenCode},
		&providertest.Fake{Provider: session.Codex},
		&providertest.Fake{Provider: session.Claude},
	}
}

// newModel builds a model on a fixed clock. Its engine has no index, so
// tests feed snapshots directly instead of scanning.
func newModel(t *testing.T, sessions []session.Session) *Model {
	t.Helper()
	engine := &app.Engine{Providers: registry()}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: sessions, UpdatedAt: now})
	m.now = func() time.Time { return now }
	m.rebuild()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

func ids(m *Model) []string {
	var out []string
	for _, it := range m.items {
		out = append(out, it.session.ID)
	}
	return out
}

func selectedID(m *Model) string {
	it, _ := m.selected()
	return it.session.ID
}

func screen(m *Model) string { return ansi.Strip(m.render()) }

func TestSessionsGroupByAttention(t *testing.T) {
	m := newModel(t, fixture())
	if got := fmt.Sprint(ids(m)); got != "[waiting failed busy idle done old]" {
		t.Fatalf("order = %s", got)
	}
	var headings []string
	for _, l := range m.lines {
		if l.heading {
			headings = append(headings, fmt.Sprintf("%s %d", groupNames[l.group], l.count))
		}
	}
	if got := strings.Join(headings, ", "); got != "NEEDS YOU 2, WORKING 1, RECENT 2, EARLIER 1" {
		t.Fatalf("headings = %s", got)
	}
}

func TestCursorMovesBetweenSessionsNotHeadings(t *testing.T) {
	m := newModel(t, fixture())
	var visited []string
	for range ids(m) {
		visited = append(visited, selectedID(m))
		m.Update(key("j"))
	}
	if got := fmt.Sprint(visited); got != fmt.Sprint(ids(m)) {
		t.Fatalf("visited %s", got)
	}
	m.Update(key("j"))
	if selectedID(m) != "old" {
		t.Fatal("moving past the last row must stay on it")
	}
	m.Update(key("g"))
	if selectedID(m) != "waiting" {
		t.Fatal("g must return to the first row")
	}
}

func TestSpawnedSessionsStayOutOfTheList(t *testing.T) {
	sessions := fixture()
	child := fake("child", session.Codex, "api", session.Waiting, time.Minute)
	child.Parent, child.Spawned = "busy", true
	m := newModel(t, append(sessions, child))
	for _, id := range ids(m) {
		if id == "child" {
			t.Fatal("a sub-agent run must not be listed")
		}
	}
	if strings.Contains(screen(m), "3 need you") {
		t.Fatal("a sub-agent run must not count as needing the user")
	}
}

func TestSelectionFollowsTheSessionAcrossRefresh(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(key("j"))
	m.Update(key("j"))
	if selectedID(m) != "busy" {
		t.Fatalf("selected %s", selectedID(m))
	}
	// The session it was on now needs the user and moves to the top.
	sessions := fixture()
	for i := range sessions {
		if sessions[i].ID == "busy" {
			sessions[i].Status = session.Waiting
			sessions[i].UpdatedAt = now
		}
	}
	app.Sort(sessions)
	m.Update(scanMsg(app.Snapshot{Sessions: sessions, UpdatedAt: now}))
	if selectedID(m) != "busy" {
		t.Fatalf("selection jumped to %s", selectedID(m))
	}
}

func TestProjectsViewOpensAProjectAndComesBack(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(key("tab"))
	if !m.projects || len(m.items) != 2 {
		t.Fatalf("projects = %v, rows %v", m.projects, ids(m))
	}
	web := m.items[0]
	if web.session.ProjectID != "web" || web.sessions != 3 || web.working != 1 {
		t.Fatalf("most urgent project first, with its counts: %+v", web)
	}
	m.Update(key("j"))
	m.Update(key("enter"))
	if m.projects || m.filter.project != "api" || fmt.Sprint(ids(m)) != "[failed done old]" {
		t.Fatalf("drill-in: projects=%v filter=%+v rows=%v", m.projects, m.filter, ids(m))
	}
	if !strings.Contains(screen(m), "All agents › api") {
		t.Fatal("the context line must name the open project")
	}
	m.Update(key("esc"))
	if !m.projects || m.filter.project != "" || m.items[m.cursor].session.ProjectID != "api" {
		t.Fatal("esc must return to the projects list, still on the project")
	}
}

func TestSearchFiltersAsYouTypeAndEscReverts(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(key("/"))
	typeText(m, "summary busy")
	if fmt.Sprint(ids(m)) != "[busy]" {
		t.Fatalf("search rows = %v", ids(m))
	}
	m.Update(key("esc"))
	if m.filter.query != "" || len(m.items) != 6 {
		t.Fatal("esc must restore the previous search")
	}
	m.Update(key("/"))
	typeText(m, "/work/api")
	m.Update(key("enter"))
	if m.mode != browsing || fmt.Sprint(ids(m)) != "[failed done old]" {
		t.Fatalf("enter must keep the search: mode %v rows %v", m.mode, ids(m))
	}
	m.Update(key("esc"))
	if len(m.items) != 6 {
		t.Fatal("esc in the list must clear the search")
	}
}

func TestAgentFilterCyclesThroughProviders(t *testing.T) {
	m := newModel(t, fixture())
	var seen []string
	for range 4 {
		m.Update(key("a"))
		seen = append(seen, fmt.Sprintf("%s=%d", m.agentLabel(), len(m.items)))
	}
	if got := strings.Join(seen, " "); got != "opencode=1 codex=3 claude=2 All agents=6" {
		t.Fatalf("cycle = %s", got)
	}
}

func TestEnterOpensTheSessionAndComesBack(t *testing.T) {
	dir := t.TempDir()
	s := fake("cx-1", session.Codex, "api", session.Waiting, time.Minute)
	s.Directory = dir
	engine := &app.Engine{Providers: registry(), Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("no tmux")
	}}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: []session.Session{s}, UpdatedAt: now})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, cmd := m.Update(key("enter"))
	if cmd == nil || !strings.Contains(screen(m), "Opening in codex…") {
		t.Fatalf("enter must start opening the session:\n%s", screen(m))
	}
	if _, again := m.Update(key("enter")); again != nil {
		t.Fatal("a second enter must wait for the first open")
	}
	prepared, ok := cmd().(preparedMsg)
	if !ok || prepared.err != nil || prepared.command.Name != "true" || prepared.command.Directory != dir {
		t.Fatalf("prepared %+v", prepared)
	}
	if _, cmd := m.Update(tickMsg(now)); cmd == nil || m.scanning {
		t.Fatal("ticks must keep going without scanning while the agent has the terminal")
	}
	m.Update(openedMsg{})
	if m.opening != "" || !m.scanning || !strings.Contains(screen(m), "Back from codex.") {
		t.Fatalf("returning must say so and refresh:\n%s", screen(m))
	}
	m.Update(key("j"))
	if m.notice != "" {
		t.Fatal("the notice must clear on the next key")
	}
}

func TestOpenFailuresAreShownNotFatal(t *testing.T) {
	s := fake("cx-1", session.Codex, "api", session.Waiting, time.Minute)
	s.Directory = "/no/such/folder"
	engine := &app.Engine{Providers: registry(), Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("no tmux")
	}}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: []session.Session{s}, UpdatedAt: now})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.opening != "" || !strings.Contains(screen(m), "Could not open: the project folder no longer exists") {
		t.Fatalf("a failed open must explain itself:\n%s", screen(m))
	}
}

// A notice wider than the context line must still show, shortened, rather
// than vanish: a failed open names the folder, and real folders are long.
func TestALongNoticeIsShortenedNotDropped(t *testing.T) {
	s := fake("cx-1", session.Codex, "api", session.Waiting, time.Minute)
	s.Directory = "/Users/someone/work/clients/" + strings.Repeat("long-folder-name/", 6) + "api"
	engine := &app.Engine{Providers: registry(), Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("no tmux")
	}}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: []session.Session{s}, UpdatedAt: now})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if !strings.Contains(screen(m), "Could not open: the project folder") {
		t.Fatalf("a notice wider than the line must be shortened, not dropped:\n%s", screen(m))
	}
}

func TestDemoSessionsDoNotOpen(t *testing.T) {
	m := New(context.Background(), &app.Engine{Demo: true, Providers: registry()}, Options{Refresh: time.Second}, app.Demo())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if _, cmd := m.Update(key("enter")); cmd != nil || !strings.Contains(screen(m), "Demo sessions can't be opened.") {
		t.Fatal("demo mode must not launch agents")
	}
}

func TestMouseSelectsRowsAndTabs(t *testing.T) {
	m := newModel(t, fixture())
	l := m.layout()
	// Line 0 is the NEEDS YOU heading; line 1 the first session.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: l.listTop + 2})
	if selectedID(m) != "failed" {
		t.Fatalf("click selected %s", selectedID(m))
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: l.listTop})
	if selectedID(m) != "failed" {
		t.Fatal("clicking a heading must not move the selection")
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if selectedID(m) != "busy" {
		t.Fatalf("wheel moved to %s", selectedID(m))
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: projectsTabX + 1, Y: 0})
	if !m.projects {
		t.Fatal("clicking Projects must switch views")
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: sessionsTabX, Y: 0})
	if m.projects {
		t.Fatal("clicking Sessions must switch back")
	}
}

func TestPreviewLoadsForTheSelectedSession(t *testing.T) {
	sessions := []session.Session{fake("ses_a", session.OpenCode, "web", session.Working, time.Minute)}
	engine := &app.Engine{Providers: provider.Registry{providertest.WithPreview(&providertest.Fake{Provider: session.OpenCode}, func(_ context.Context, id string) (string, error) {
		return "Latest words from " + id, nil
	})}}
	m := New(context.Background(), engine, Options{Refresh: time.Second}, app.Snapshot{Sessions: sessions, UpdatedAt: now})
	m.now = func() time.Time { return now }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, cmd := m.Update(previewRequest{session: sessions[0]})
	if cmd == nil {
		t.Fatal("selecting a previewable session must request its activity")
	}
	if !strings.Contains(screen(m), "loading…") {
		t.Fatal("the details must say the activity is loading")
	}
	m.Update(cmd())
	if !strings.Contains(screen(m), "Latest words from ses_a") {
		t.Fatalf("preview not shown:\n%s", screen(m))
	}
}

func TestEmptyStatesSayWhatToDo(t *testing.T) {
	m := newModel(t, nil)
	m.scanning = false
	if !strings.Contains(screen(m), "No sessions found") {
		t.Fatalf("empty index:\n%s", screen(m))
	}
	m = newModel(t, fixture())
	m.Update(key("/"))
	typeText(m, "nothing like this")
	if !strings.Contains(screen(m), "Nothing matches") {
		t.Fatal("an empty search must say how to clear it")
	}
}

func TestHelpAndDiagnosticsOpenAndClose(t *testing.T) {
	m := newModel(t, fixture())
	m.snapshot.Warnings = []string{"codex: permission denied"}
	m.Update(key("?"))
	if !strings.Contains(screen(m), "Statuses") {
		t.Fatal("? must show the keys and statuses")
	}
	m.Update(key("esc"))
	m.Update(key("!"))
	if !strings.Contains(screen(m), "codex: permission denied") {
		t.Fatal("! must list scan problems")
	}
	m.Update(key("!"))
	if m.mode != browsing {
		t.Fatal("! must close diagnostics")
	}
}

// Every screen must fit the terminal exactly, whatever the text: long
// titles, long paths and wide characters included.
func TestEveryScreenFitsTheTerminal(t *testing.T) {
	sessions := fixture()
	long := fake("long", session.Claude, "a-very-long-project-name-that-keeps-going", session.Waiting, time.Second)
	long.Title = strings.Repeat("Rifinire il flusso di pagamento 支払いフロー ", 6)
	long.Summary = strings.Repeat("Una frase piuttosto lunga con /percorsi/molto/lunghi/che/non/si/spezzano ", 8)
	long.Directory = "/" + strings.Repeat("deep/", 40) + "project"
	sessions = append([]session.Session{long}, sessions...)
	for _, size := range [][2]int{{30, 8}, {50, 12}, {60, 20}, {80, 24}, {109, 30}, {110, 16}, {132, 34}, {220, 60}} {
		for _, keys := range [][]string{nil, {"tab"}, {"?"}, {"!"}, {"/", "x"}, {"x"}, {"d"}, {"m"}, {"m", "tab"}, {"tab", "x"}} {
			m := newModel(t, sessions)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for _, k := range keys {
				m.Update(key(k))
			}
			lines := strings.Split(m.render(), "\n")
			if len(lines) > size[1] {
				t.Fatalf("%dx%d %v: %d lines", size[0], size[1], keys, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Fatalf("%dx%d %v: line %d is %d wide: %q", size[0], size[1], keys, i, w, ansi.Strip(l))
				}
			}
		}
	}
}

func TestLongListsScrollWithTheSelection(t *testing.T) {
	var sessions []session.Session
	for i := range 60 {
		sessions = append(sessions, fake(fmt.Sprintf("s%02d", i), session.Codex, "api", session.Completed, time.Duration(i)*time.Minute))
	}
	m := newModel(t, sessions)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m.Update(key("G"))
	if !strings.Contains(screen(m), "Title s59") {
		t.Fatal("the last row must be on screen after G")
	}
	m.Update(key("g"))
	if !strings.Contains(screen(m), "RECENT") || !strings.Contains(screen(m), "Title s00") {
		t.Fatal("returning to the top must show the first heading again")
	}
}

// Folder names and session IDs come from agent data like titles do. No view
// may pass their escape sequences to the terminal: here a clipboard write
// (OSC 52) in a folder name and a screen clear in an ID.
func TestFolderNamesAndIDsCannotInjectTerminalCommands(t *testing.T) {
	hostile := fake("id\x1b[2Jx", session.Codex, "api", session.Waiting, time.Minute)
	hostile.Directory = "/work/evil\x1b]52;c;aGk=\x07name"
	hostile.OriginalDirectory = hostile.Directory
	check := func(name, out string) {
		t.Helper()
		for _, payload := range []string{"\x1b]52;", "\x07", "\x1b[2J"} {
			if strings.Contains(out, payload) {
				t.Errorf("%s: output contains %q", name, payload)
			}
		}
	}
	m := newModel(t, []session.Session{hostile})
	check("sessions view", m.render())
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	check("narrow screen", m.render())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(key("tab"))
	check("projects view", m.render())
	m.Update(key("x"))
	check("remove dialog", m.render())
	m.Update(key("esc"))
	m.Update(key("m"))
	check("move dialog", m.render())
}

// The recent-window setting moves the line between RECENT and EARLIER.
func TestRecentWindowIsASetting(t *testing.T) {
	old := fake("old", session.Codex, "api", session.Completed, 30*time.Hour)
	group := func(window time.Duration) group {
		engine := &app.Engine{Providers: registry()}
		m := New(context.Background(), engine, Options{Refresh: time.Second, RecentWindow: window}, app.Snapshot{Sessions: []session.Session{old}, UpdatedAt: now})
		m.now = func() time.Time { return now }
		m.rebuild()
		return m.items[0].group
	}
	if got := group(0); got != earlier {
		t.Errorf("default window: a 30-hour-old session is in group %d, want EARLIER", got)
	}
	if got := group(48 * time.Hour); got != recent {
		t.Errorf("48-hour window: a 30-hour-old session is in group %d, want RECENT", got)
	}
}
