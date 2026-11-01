package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/session"
)

// Results of the three index changes. Each carries the engine generation it
// produced, so a refresh that started earlier cannot undo it on screen.
type removedMsg struct {
	key, project string
	generation   uint64
	err          error
}
type deletedMsg struct {
	key        string
	generation uint64
	err        error
}
type movedMsg struct {
	project, path string
	generation    uint64
	err           error
}

// folderBrowser lists the subfolders of path while choosing a new folder.
type folderBrowser struct {
	active bool
	path   string
	names  []string
	cursor int
}

// beginChange opens a confirmation dialog for the selected row, unless
// another change is still running.
func (m *Model) beginChange(next mode) tea.Cmd {
	it, ok := m.selected()
	if !ok || m.busy != "" || m.opening != "" {
		return nil
	}
	if m.engine.Demo {
		m.notice = "Demo sessions can't be changed."
		return nil
	}
	if next == confirmDelete && it.project {
		m.notice = "Open the project and pick a session to delete."
		return nil
	}
	m.target, m.dialogErr, m.mode = it, "", next
	if next != moving {
		return nil
	}
	m.folders = folderBrowser{}
	// Size the field before filling it, so a long path scrolls in one line.
	m.path.SetWidth(max(10, m.dialogWidth()-2))
	m.path.SetValue(it.session.Directory)
	m.path.CursorEnd()
	return m.path.Focus()
}

// dialogWidth is the text width inside a dialog box.
func (m *Model) dialogWidth() int { return min(72, m.width-4) - 6 }

// tail keeps the end of a long path, which names the folder, and marks the
// cut at the start.
func tail(path string, w int) string {
	if n := ansi.StringWidth(path); n > w && w > 1 {
		return "…" + ansi.TruncateLeft(path, n-(w-1), "")
	}
	return path
}

// updateConfirm answers the remove and delete dialogs.
func (m *Model) updateConfirm(msg tea.KeyPressMsg) tea.Cmd {
	if m.busy != "" {
		return nil
	}
	switch msg.String() {
	case "esc", "n", "q":
		m.mode = browsing
		return nil
	case "y":
	default:
		return nil
	}
	target := m.target
	m.dialogErr = ""
	if m.mode == confirmDelete {
		m.busy = "Deleting…"
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
			defer cancel()
			key := target.session.Key()
			generation, err := m.engine.Delete(ctx, key)
			return deletedMsg{key: key, generation: generation, err: err}
		}
	}
	m.busy = "Removing…"
	return func() tea.Msg {
		if target.project {
			id := target.session.ProjectID
			generation, err := m.engine.RemoveProject(id)
			return removedMsg{project: id, generation: generation, err: err}
		}
		key := target.session.Key()
		generation, err := m.engine.RemoveSession(key)
		return removedMsg{key: key, generation: generation, err: err}
	}
}

func (m *Model) finishRemove(msg removedMsg) tea.Cmd {
	m.busy = ""
	if msg.err != nil {
		m.dialogErr = "Could not remove: " + msg.err.Error()
		return nil
	}
	m.mode = browsing
	// A project removal also covers sessions hidden by the current filters.
	m.forget(msg.generation, func(s session.Session) bool {
		return (msg.key != "" && s.Key() == msg.key) || (msg.project != "" && s.ProjectID == msg.project)
	})
	m.notice = "Removed from agentflockr. The agent keeps its history."
	if msg.project != "" {
		m.notice = "Project removed from agentflockr. The agents keep their history."
	}
	return m.rescan()
}

func (m *Model) finishDelete(msg deletedMsg) tea.Cmd {
	m.busy = ""
	if msg.err != nil {
		m.dialogErr = "Could not delete: " + msg.err.Error()
		return nil
	}
	m.mode = browsing
	m.forget(msg.generation, func(s session.Session) bool { return s.Key() == msg.key })
	m.notice = "Session deleted."
	return m.rescan()
}

// forget drops rows after a completed change. It filters what is on screen
// instead of reloading, so live status and the selection's neighbourhood
// survive until the next refresh.
func (m *Model) forget(generation uint64, gone func(session.Session) bool) {
	m.minGeneration = max(m.minGeneration, generation)
	kept := make([]session.Session, 0, len(m.snapshot.Sessions))
	for _, s := range m.snapshot.Sessions {
		if gone(s) {
			delete(m.previews, s.Key())
			continue
		}
		kept = append(kept, s)
	}
	m.snapshot.Sessions = kept
	m.rebuild()
}

func (m *Model) updateMove(msg tea.KeyPressMsg) tea.Cmd {
	if m.busy != "" {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.mode = browsing
		m.path.Blur()
		return nil
	case "tab":
		if m.folders.active {
			m.folders.active = false
			m.path.SetValue(m.folders.path)
			m.path.CursorEnd()
			return m.path.Focus()
		}
		m.browse(expandHome(m.path.Value()), true)
		return nil
	}
	if m.folders.active {
		switch msg.String() {
		case "down", "j":
			m.folders.cursor = min(m.folders.cursor+1, len(m.folders.names)-1)
		case "up", "k":
			m.folders.cursor = max(0, m.folders.cursor-1)
		case "left", "h", "backspace":
			m.browse(filepath.Dir(m.folders.path), false)
		case "right", "l", "enter":
			if len(m.folders.names) > 0 {
				m.browse(filepath.Join(m.folders.path, m.folders.names[m.folders.cursor]), false)
			}
		}
		return nil
	}
	if msg.String() == "enter" {
		path, id := expandHome(strings.TrimSpace(m.path.Value())), m.target.session.ProjectID
		m.busy, m.dialogErr = "Moving…", ""
		engine := m.engine
		return func() tea.Msg {
			generation, err := engine.Reconnect(id, path)
			return movedMsg{project: id, path: path, generation: generation, err: err}
		}
	}
	var cmd tea.Cmd
	m.path, cmd = m.path.Update(msg)
	return cmd
}

func (m *Model) finishMove(msg movedMsg) tea.Cmd {
	m.busy = ""
	if msg.err != nil {
		m.dialogErr = "Could not move: " + msg.err.Error()
		return nil
	}
	m.mode = browsing
	m.path.Blur()
	m.relocate(msg.generation, msg.project, filepath.Clean(msg.path))
	m.notice = "Project moved. Its sessions open in the new folder."
	return m.rescan()
}

// relocate shows a completed move without reloading the index, which would
// show every status as unknown until the next refresh. The project's
// sessions, and those already in the new folder, which the move merged into
// it, take the new folder; the refresh that follows brings the rest.
func (m *Model) relocate(generation uint64, project, path string) {
	m.minGeneration = max(m.minGeneration, generation)
	for i := range m.snapshot.Sessions {
		s := &m.snapshot.Sessions[i]
		if s.ProjectID == project || session.SameDirectory(s.Directory, path) {
			s.ProjectID, s.Directory, s.Missing = project, path, false
		}
	}
	m.rebuild()
}

// browse lists the folders inside path. A moved project's old folder may be
// gone, so the first listing starts from its nearest surviving parent.
func (m *Model) browse(path string, nearest bool) {
	path, err := filepath.Abs(path)
	if err == nil && nearest {
		for {
			info, statErr := os.Stat(path)
			if (statErr == nil && info.IsDir()) || (statErr != nil && !os.IsNotExist(statErr)) || filepath.Dir(path) == path {
				break
			}
			path = filepath.Dir(path)
		}
	}
	var entries []os.DirEntry
	if err == nil {
		entries, err = os.ReadDir(path)
	}
	if err != nil {
		m.dialogErr = "Can't list that folder: " + err.Error()
		return
	}
	var names []string
	for _, entry := range entries {
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(path, entry.Name()))
			isDir = err == nil && info.IsDir()
		}
		if isDir && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	m.folders = folderBrowser{active: true, path: path, names: names}
	m.path.Blur()
	m.dialogErr = ""
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// changeBox renders the open dialog: remove, delete or move.
func (m *Model) changeBox(p palette) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	bold := lipgloss.NewStyle().Bold(true)
	inner := m.dialogWidth()
	s := m.target.session
	where := func(prefix string) string {
		return muted.Render(prefix + tail(shortPath(s.Directory), inner-ansi.StringWidth(prefix)))
	}
	var title, footer string
	var body []string
	switch m.mode {
	case confirmRemove:
		title = "Remove from agentflockr"
		footer = hints(p, [][2]string{{"y", "remove"}, {"esc", "cancel"}})
		if m.target.project {
			body = []string{bold.Render(projectName(s)), where(""), "",
				"agentflockr stops listing this project and its " + plural(m.target.sessions, "session") +
					", including sessions started there later. The agents keep their history and files."}
		} else {
			body = []string{bold.Render(session.Text(s.Title, 200)), where(m.title(s.Provider) + " · "), "",
				"agentflockr stops listing this session, even when " + m.title(s.Provider) + " updates it. " +
					m.title(s.Provider) + " keeps its history and files."}
		}
	case confirmDelete:
		title = "Delete session"
		footer = hints(p, [][2]string{{"y", "delete"}, {"esc", "cancel"}})
		scope := "Permanently deletes this conversation."
		if a := m.engine.Providers.Get(s.Provider); a != nil && a.Meta().DeleteScope != "" {
			scope = a.Meta().DeleteScope
		}
		body = []string{bold.Render(session.Text(s.Title, 200)), where(m.title(s.Provider) + " · "), "",
			scope + " This can't be undone."}
	case moving:
		title = "Move project"
		footer = hints(p, [][2]string{{"↵", "move"}, {"tab", "browse"}, {"esc", "cancel"}})
		body = []string{bold.Render(projectName(s)), where("Now in ")}
		if s.Missing {
			body[1] += lipgloss.NewStyle().Foreground(p.waiting).Render("  · folder not found")
		}
		body = append(body, "", "New folder")
		if m.folders.active {
			footer = hints(p, [][2]string{{"↑↓", "select"}, {"→", "open"}, {"←", "up"}, {"tab", "use this folder"}, {"esc", "cancel"}})
			body = append(body, lipgloss.NewStyle().Foreground(p.accent).Render(tail(shortPath(m.folders.path)+"/", inner)))
			page := max(3, min(12, m.height-20))
			start := max(0, m.folders.cursor-page+1)
			if len(m.folders.names) == 0 {
				body = append(body, muted.Render("  No folders here."))
			}
			for i := start; i < min(len(m.folders.names), start+page); i++ {
				name := fit("  "+session.Text(m.folders.names[i], 200)+"/", inner)
				if i == m.folders.cursor {
					name = lipgloss.NewStyle().Background(p.selection).Render(fit("› "+session.Text(m.folders.names[i], 200)+"/", inner))
				}
				body = append(body, name)
			}
		} else {
			input := m.path
			input.SetWidth(max(10, inner-2))
			body = append(body, input.View())
		}
		body = append(body, "", muted.Render("Sessions open in the new folder from now on. History already listed there joins this project."))
	}
	if m.dialogErr != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(p.waiting).Render(session.Text(m.dialogErr, 400)))
	}
	if m.busy != "" {
		footer = muted.Render(m.busy)
	}
	color := p.accent
	if m.mode == confirmDelete {
		color = p.failed
	}
	return m.box(p, title, color, body, footer)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
