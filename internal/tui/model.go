// Package tui is the terminal dashboard: one list of every agent session,
// grouped by what needs attention, with the selected session's details.
package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

type mode int

const (
	browsing mode = iota
	searching
	helping
	diagnosing
	confirmRemove
	confirmDelete
	moving
	startingNew
)

type scanMsg app.Snapshot
type tickMsg time.Time
type viewSavedMsg struct{ err error }

// preparedMsg carries the command that opens a session; openedMsg arrives
// when that command exits and the dashboard is back on screen.
type preparedMsg struct {
	command provider.Command
	err     error
	// starting marks a new session; its failures read "Could not start:".
	starting bool
}

// openedMsg carries, in created, a session the agent created for a start
// before its command ran, so a failure can say it is kept.
type openedMsg struct {
	err     error
	created string
}

type previewRequest struct{ session session.Session }
type previewMsg struct {
	key     string
	updated time.Time
	text    string
	err     error
}
type previewEntry struct {
	updated time.Time
	text    string
	loading bool
	err     error
}

// Options are the dashboard's settings. A zero field takes its default.
type Options struct {
	// Refresh is how often to look for changes.
	Refresh time.Duration
	// RecentWindow separates RECENT from EARLIER; zero means
	// DefaultRecentWindow.
	RecentWindow time.Duration
}

type Model struct {
	ctx      context.Context
	engine   *app.Engine
	interval time.Duration
	recent   time.Duration
	now      func() time.Time

	snapshot app.Snapshot
	items    []item
	lines    []line

	width, height int
	cursor        int // index into items
	offset        int // first list line on screen
	projects      bool
	filter        filter
	agent         int // 0 for all agents, else 1 + index into engine.Providers
	searchPrev    string
	mode          mode
	input         textinput.Model
	notice        string
	scanning      bool
	opening       string // the agent being opened, while it has the terminal
	viaTmux       bool   // the open moved to an existing tmux pane
	previews      map[string]previewEntry
	dark          bool

	// The remove, delete and move dialogs.
	target        item   // the row the dialog acts on
	dialogErr     string // why the last attempt failed
	busy          string // the change in progress, such as "Deleting…"
	path          textinput.Model
	folders       folderBrowser
	minGeneration uint64 // refreshes older than this predate a completed change

	// The New session form, and the sequence of its reads. The sequence is
	// kept across forms, so a reply from a closed form never matches a new one.
	start    startForm
	startSeq uint64
}

func New(ctx context.Context, engine *app.Engine, options Options, snapshot app.Snapshot) *Model {
	if options.RecentWindow <= 0 {
		options.RecentWindow = DefaultRecentWindow
	}
	m := &Model{ctx: ctx, engine: engine, interval: options.Refresh, recent: options.RecentWindow, now: time.Now, snapshot: snapshot,
		width: 100, height: 30, previews: map[string]previewEntry{}, dark: true}
	m.input = textinput.New()
	m.input.CharLimit = 200
	m.input.Prompt = "/ "
	m.input.Placeholder = "title, folder or session ID"
	m.input.SetVirtualCursor(true)
	m.path = textinput.New()
	m.path.CharLimit = 4096
	m.path.Prompt = ""
	m.path.Placeholder = "/absolute/path/to/project"
	m.path.SetVirtualCursor(true)
	m.styleInput()
	view, err := engine.View()
	if err != nil {
		m.notice = "Could not read the saved view: " + err.Error()
	}
	m.projects = view == "projects"
	m.rebuild()
	return m
}

func (m *Model) Init() tea.Cmd {
	m.scanning = true
	return tea.Batch(tea.RequestBackgroundColor, m.scan(), m.tick(), m.preview())
}

func (m *Model) scan() tea.Cmd { return func() tea.Msg { return scanMsg(m.engine.Scan(m.ctx)) } }

func (m *Model) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) rescan() tea.Cmd {
	if m.scanning {
		return nil
	}
	m.scanning = true
	return m.scan()
}

// rebuild recomputes the rows and keeps the selection on the same session or
// project when it is still listed.
func (m *Model) rebuild() {
	previous, had := m.selected()
	m.items = buildItems(m.snapshot.Sessions, m.filter, m.projects, m.now(), m.recent)
	m.lines = buildLines(m.items)
	m.cursor = min(m.cursor, max(0, len(m.items)-1))
	if had {
		m.selectKey(previous.key())
	}
	m.ensureVisible()
}

func (m *Model) selectKey(key string) {
	for i, it := range m.items {
		if it.key() == key {
			m.cursor = i
			return
		}
	}
}

func (m *Model) selected() (item, bool) {
	if len(m.items) == 0 {
		return item{}, false
	}
	return m.items[min(max(m.cursor, 0), len(m.items)-1)], true
}

// ensureVisible scrolls the list so the selected row, and the heading above
// the first row of a group, stay on screen.
func (m *Model) ensureVisible() {
	height := m.layout().listHeight
	if height <= 0 || len(m.lines) == 0 {
		m.offset = 0
		return
	}
	target := lineOf(m.lines, m.cursor)
	top := target
	if target > 0 && m.lines[target-1].heading {
		top = target - 1
	}
	if top < m.offset {
		m.offset = top
	}
	if target >= m.offset+height {
		m.offset = target - height + 1
	}
	m.offset = min(max(m.offset, 0), max(0, len(m.lines)-height))
}

func (m *Model) move(delta int) tea.Cmd {
	if len(m.items) == 0 {
		return nil
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.items)-1)
	m.ensureVisible()
	return m.preview()
}

func (m *Model) setProjects(projects bool) tea.Cmd {
	m.projects = projects
	m.filter.project = ""
	m.cursor, m.offset = 0, 0
	m.rebuild()
	view := "sessions"
	if projects {
		view = "projects"
	}
	return tea.Batch(func() tea.Msg { return viewSavedMsg{err: m.engine.SetView(view)} }, m.preview())
}

func (m *Model) cycleAgent() {
	m.agent = (m.agent + 1) % (len(m.engine.Providers) + 1)
	m.filter.provider = ""
	if m.agent > 0 {
		m.filter.provider = m.engine.Providers[m.agent-1].ID()
	}
	m.cursor = 0
	m.rebuild()
}

// currentPreview returns the preview loaded for s at its current update
// time. A preview from an older update is out of date and not returned.
func (m *Model) currentPreview(s session.Session) (previewEntry, bool) {
	preview, ok := m.previews[s.Key()]
	return preview, ok && preview.updated.Equal(s.UpdatedAt)
}

// activity is the latest activity to show for s: the current preview's text
// when there is one, else the summary from the index.
func (m *Model) activity(s session.Session) string {
	if preview, ok := m.currentPreview(s); ok && preview.text != "" {
		return preview.text
	}
	return s.Summary
}

// preview asks for the selected session's latest activity after a short
// pause, so scrolling through the list does not start a request per row.
func (m *Model) preview() tea.Cmd {
	it, ok := m.selected()
	if !ok || it.project || !m.engine.CanPreview(it.session) {
		return nil
	}
	s := it.session
	if _, ok := m.currentPreview(s); ok {
		return nil
	}
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return previewRequest{session: s} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.styleInput()
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil
	case scanMsg:
		m.scanning = false
		if msg.Generation < m.minGeneration {
			// The refresh began before a completed change and may bring back
			// what it removed. Replace it with one that sees the change.
			return m, m.rescan()
		}
		m.snapshot = app.Snapshot(msg)
		m.rebuild()
		return m, m.preview()
	case startProfilesMsg:
		return m, m.applyStartProfiles(msg)
	case startCatalogMsg:
		return m, m.applyStartCatalog(msg)
	case removedMsg:
		return m, m.finishRemove(msg)
	case deletedMsg:
		return m, m.finishDelete(msg)
	case movedMsg:
		return m, m.finishMove(msg)
	case tickMsg:
		if m.opening != "" || m.busy != "" {
			// The agent has the terminal; look again when it hands it back.
			return m, m.tick()
		}
		return m, tea.Batch(m.rescan(), m.tick())
	case preparedMsg:
		if msg.starting && msg.err != nil && !errors.Is(msg.err, provider.ErrMissingFolder) {
			// Nothing ran, so the form stays open with the reason and every
			// choice, ready to change one and try again.
			m.opening = ""
			m.notice = ""
			m.start.err = "Could not start: " + session.Text(msg.err.Error(), 300)
			return m, nil
		}
		if msg.starting {
			// The agent takes the terminal, or the folder is gone and the
			// person must leave the form to move the project.
			m.mode = browsing
			m.start = startForm{}
		}
		if msg.err != nil {
			m.opening = ""
			if msg.starting {
				m.notice = startFailed(msg.err)
				return m, nil
			}
			m.notice = "Could not open: " + msg.err.Error()
			if it, ok := m.selected(); ok && it.session.Missing {
				m.notice += ". Press m to choose its new folder."
			}
			return m, nil
		}
		m.viaTmux = msg.command.Name == "tmux"
		created := msg.command.Created
		return m, tea.ExecProcess(msg.command.Exec(), func(err error) tea.Msg { return openedMsg{err: err, created: created} })
	case openedMsg:
		agent := m.opening
		m.opening = ""
		m.notice = "Back from " + agent + "."
		if m.viaTmux {
			// Inside tmux the switch returns at once; the agent keeps running.
			m.notice = "Switched to the tmux pane running this " + agent + " session."
		}
		if msg.err != nil {
			m.notice = agent + " exited: " + msg.err.Error()
			if msg.created != "" {
				m.notice += ". It had created session " + session.Text(msg.created, 100) + ", which is kept."
			}
		}
		return m, m.rescan()
	case viewSavedMsg:
		if msg.err != nil {
			m.notice = "Could not save the view: " + msg.err.Error()
		}
		return m, nil
	case previewRequest:
		s := msg.session
		current, ok := m.selected()
		if !ok || current.project || current.session.Key() != s.Key() || !current.session.UpdatedAt.Equal(s.UpdatedAt) {
			return m, nil
		}
		if _, ok := m.currentPreview(s); ok {
			return m, nil
		}
		m.previews[s.Key()] = previewEntry{updated: s.UpdatedAt, loading: true}
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			defer cancel()
			text, err := m.engine.Preview(ctx, s)
			return previewMsg{key: s.Key(), updated: s.UpdatedAt, text: text, err: err}
		}
	case previewMsg:
		if previous, ok := m.previews[msg.key]; ok && previous.updated.Equal(msg.updated) {
			m.previews[msg.key] = previewEntry{updated: msg.updated, text: msg.text, err: msg.err}
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.mode != browsing && m.mode != searching {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseWheelDown:
			return m, m.move(1)
		case tea.MouseWheelUp:
			return m, m.move(-1)
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || (m.mode != browsing && m.mode != searching) {
			return m, nil
		}
		return m, m.click(msg.X, msg.Y)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case searching:
			return m, m.updateSearch(msg)
		case confirmRemove, confirmDelete:
			return m, m.updateConfirm(msg)
		case moving:
			return m, m.updateMove(msg)
		case startingNew:
			return m, m.updateStart(msg)
		case helping, diagnosing:
			switch msg.String() {
			case "esc", "q", "?", "!", "enter":
				m.mode = browsing
			}
			return m, nil
		}
		return m, m.updateBrowse(msg)
	}
	// Pastes and cursor blinks go to whichever field is being edited.
	var cmd tea.Cmd
	switch {
	case m.mode == searching:
		m.input, cmd = m.input.Update(msg)
	case m.mode == moving && !m.folders.active:
		m.path, cmd = m.path.Update(msg)
	}
	return m, cmd
}

func (m *Model) updateBrowse(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	switch msg.String() {
	case "q":
		return tea.Quit
	case "j", "down":
		return m.move(1)
	case "k", "up":
		return m.move(-1)
	case "pgdown", "ctrl+d":
		return m.move(max(1, m.layout().listHeight-1))
	case "pgup", "ctrl+u":
		return m.move(-max(1, m.layout().listHeight-1))
	case "g", "home":
		return m.move(-len(m.items))
	case "G", "end":
		return m.move(len(m.items))
	case "tab":
		return m.setProjects(!m.projects)
	case "enter":
		it, ok := m.selected()
		if !ok {
			return nil
		}
		if it.project {
			m.projects = false
			m.filter.project = it.session.ProjectID
			m.cursor, m.offset = 0, 0
			m.rebuild()
			return m.preview()
		}
		return m.open(it.session)
	case "esc":
		if m.filter.project != "" {
			// Leave the project for the projects list, still on that project.
			id := m.filter.project
			m.filter.project = ""
			m.projects = true
			m.rebuild()
			m.selectKey("project:" + id)
			m.ensureVisible()
			return m.preview()
		}
		m.filter.query, m.filter.provider, m.agent = "", "", 0
		m.rebuild()
		return m.preview()
	case "/":
		m.mode = searching
		m.searchPrev = m.filter.query
		m.input.SetValue(m.filter.query)
		m.input.CursorEnd()
		return m.input.Focus()
	case "a":
		m.cycleAgent()
		return m.preview()
	case "m":
		return m.beginChange(moving)
	case "n":
		return m.beginStart()
	case "x":
		return m.beginChange(confirmRemove)
	case "d", "delete":
		return m.beginChange(confirmDelete)
	case "?":
		m.mode = helping
	case "!":
		m.mode = diagnosing
	case "r", "ctrl+r":
		return m.rescan()
	}
	return nil
}

// open hands the terminal to the session's agent: its running tmux pane when
// there is one, otherwise the agent's own resume.
func (m *Model) open(s session.Session) tea.Cmd {
	if m.engine.Demo {
		m.notice = "Demo sessions can't be opened."
		return nil
	}
	if m.opening != "" {
		return nil
	}
	m.opening = m.title(s.Provider)
	m.notice = "Opening in " + m.opening + "…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
		defer cancel()
		command, err := m.engine.Open(ctx, s)
		return preparedMsg{command: command, err: err}
	}
}

func (m *Model) updateSearch(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filter.query = m.searchPrev
		m.mode = browsing
		m.input.Blur()
		m.rebuild()
		return m.preview()
	case "enter":
		m.mode = browsing
		m.input.Blur()
		return nil
	case "up", "down":
		// Arrows keep moving through the results while typing.
		if msg.String() == "up" {
			return m.move(-1)
		}
		return m.move(1)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Filter as the user types; Enter keeps the result, Esc reverts it.
	if query := strings.TrimSpace(m.input.Value()); query != m.filter.query {
		m.filter.query = query
		m.cursor = 0
		m.rebuild()
	}
	return tea.Batch(cmd, m.preview())
}

// click selects the list row or switches to the tab under the pointer.
func (m *Model) click(x, y int) tea.Cmd {
	l := m.layout()
	if y == 0 {
		switch {
		case x >= sessionsTabX && x < sessionsTabX+len("Sessions") && m.projects:
			return m.setProjects(false)
		case x >= projectsTabX && x < projectsTabX+len("Projects") && !m.projects:
			return m.setProjects(true)
		}
		return nil
	}
	if y < l.listTop || y >= l.listTop+l.listHeight || x >= margin+l.listWidth {
		return nil
	}
	n := m.offset + y - l.listTop
	if n >= len(m.lines) || m.lines[n].heading {
		return nil
	}
	m.cursor = m.lines[n].item
	m.ensureVisible()
	return m.preview()
}

func (m *Model) agentLabel() string {
	if m.agent == 0 {
		return "All agents"
	}
	return m.engine.Providers[m.agent-1].Meta().Title
}
