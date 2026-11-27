package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/session"
)

// The Schedules view lists every scheduled prompt, into a session or through
// a profile, and pauses, resumes, edits and deletes them. It is the third
// view beside Sessions and Projects, reached with s or its tab.

// sortSchedules orders the view: running schedules by their next run, then
// paused ones, each group by key so the order is stable.
func sortSchedules(list []app.Schedule) []app.Schedule {
	out := append([]app.Schedule(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Paused != b.Paused {
			return !a.Paused
		}
		if !a.Paused && !a.Next.Equal(b.Next) {
			return a.Next.Before(b.Next)
		}
		return a.Key() < b.Key()
	})
	return out
}

func (m *Model) selectedSchedule() (app.Schedule, bool) {
	if len(m.schedList) == 0 {
		return app.Schedule{}, false
	}
	return m.schedList[min(max(m.schedCursor, 0), len(m.schedList)-1)], true
}

// listedSession finds the session with the key in the last refresh.
func (m *Model) listedSession(key string) (session.Session, bool) {
	for _, s := range m.snapshot.Sessions {
		if s.Key() == key {
			return s, true
		}
	}
	return session.Session{}, false
}

// setScheduled shows or leaves the Schedules view. Leaving it goes back to
// Sessions or Projects, whichever was shown before.
func (m *Model) setScheduled(on bool) tea.Cmd {
	m.scheduled = on
	view := "sessions"
	switch {
	case on:
		view = "schedules"
	case m.projects:
		view = "projects"
	}
	engine := m.engine
	cmds := []tea.Cmd{func() tea.Msg { return viewSavedMsg{err: engine.SetView(view)} }}
	if on {
		cmds = append(cmds, m.loadSchedules())
	} else {
		cmds = append(cmds, m.preview())
	}
	return tea.Batch(cmds...)
}

func (m *Model) ensureScheduleVisible() {
	height := m.layout().listHeight
	if height <= 0 || len(m.schedList) == 0 {
		m.schedOffset = 0
		return
	}
	if m.schedCursor < m.schedOffset {
		m.schedOffset = m.schedCursor
	}
	if m.schedCursor >= m.schedOffset+height {
		m.schedOffset = m.schedCursor - height + 1
	}
	m.schedOffset = min(max(m.schedOffset, 0), max(0, len(m.schedList)-height))
}

func (m *Model) moveSchedule(delta int) {
	if len(m.schedList) == 0 {
		return
	}
	m.schedCursor = min(max(m.schedCursor+delta, 0), len(m.schedList)-1)
	m.ensureScheduleVisible()
}

// updateSchedules answers the keys of the Schedules view.
func (m *Model) updateSchedules(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	switch msg.String() {
	case "q":
		return tea.Quit
	case "j", "down":
		m.moveSchedule(1)
	case "k", "up":
		m.moveSchedule(-1)
	case "pgdown", "ctrl+d":
		m.moveSchedule(max(1, m.layout().listHeight-1))
	case "pgup", "ctrl+u":
		m.moveSchedule(-max(1, m.layout().listHeight-1))
	case "g", "home":
		m.moveSchedule(-len(m.schedList))
	case "G", "end":
		m.moveSchedule(len(m.schedList))
	case "s", "tab", "esc":
		return m.setScheduled(false)
	case "n":
		return m.beginProfileSchedule()
	case "enter", "e":
		if sc, ok := m.selectedSchedule(); ok && m.busy == "" {
			return m.editSchedule(sc)
		}
	case "space", " ":
		sc, ok := m.selectedSchedule()
		if !ok || m.busy != "" {
			return nil
		}
		m.busy = "Saving…"
		engine, key, paused := m.engine, sc.Key(), !sc.Paused
		notice := "Scheduled prompt paused."
		if !paused {
			notice = "Scheduled prompt resumed."
		}
		return func() tea.Msg {
			return scheduleSavedMsg{key: key, notice: notice, err: engine.PauseSchedule(key, paused)}
		}
	case "d", "delete":
		if sc, ok := m.selectedSchedule(); ok && m.busy == "" {
			m.schedTarget = sc
			m.mode = confirmUnschedule
		}
	case "?":
		m.mode = helping
	case "!":
		m.mode = diagnosing
	case "r", "ctrl+r":
		return tea.Batch(m.rescan(), m.loadSchedules())
	}
	return nil
}

// updateUnschedule answers the delete confirmation.
func (m *Model) updateUnschedule(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "y", "enter":
		m.mode = browsing
		m.busy = "Deleting…"
		engine, key := m.engine, m.schedTarget.Key()
		return func() tea.Msg {
			return scheduleSavedMsg{key: key, notice: "Scheduled prompt deleted.", err: engine.DeleteSchedule(key)}
		}
	case "esc", "n", "q":
		m.mode = browsing
	}
	return nil
}

func (m *Model) unscheduleBox(p palette) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	sc := m.schedTarget
	_, title := m.scheduleLabel(sc)
	body := []string{
		lipgloss.NewStyle().Bold(true).Render(fit(title, m.dialogWidth())),
		muted.Render(fit(session.Text(sc.Prompt, 400), m.dialogWidth())), "",
		"The prompt is no longer sent. A run in progress finishes. Space pauses it instead.",
	}
	return m.box(p, "Delete scheduled prompt", p.waiting, body, hints(p, [][2]string{{"y", "delete"}, {"esc", "keep"}}))
}

// clickSchedule selects the schedule row under the pointer.
func (m *Model) clickSchedule(y int) {
	l := m.layout()
	if y < l.listTop || y >= l.listTop+l.listHeight {
		return
	}
	if n := m.schedOffset + y - l.listTop; n < len(m.schedList) {
		m.schedCursor = n
		m.ensureScheduleVisible()
	}
}

// scheduleLabel names a schedule's target: a short name for the list
// column, and a title.
func (m *Model) scheduleLabel(sc app.Schedule) (string, string) {
	if sc.IsProfile() {
		name := sc.ProfileName
		if name == "" {
			name = sc.Profile
		}
		if name == "" {
			name = "default"
		}
		return "profile", session.Text(m.title(session.Provider(sc.Agent))+" profile "+name, 200)
	}
	if s, ok := m.listedSession(sc.Session); ok {
		return projectName(s), session.Text(s.Title, 200)
	}
	return "—", "Session no longer listed: " + session.Text(sc.Session, 200)
}

// scheduleState is a schedule's glyph, label and colour in the list.
func scheduleState(p palette, sc app.Schedule) (string, string, color.Color) {
	switch {
	case sc.Paused:
		return "‖", "paused", p.muted
	case sc.LastError != "":
		return "!", "failing", p.waiting
	}
	return "▶", "active", p.accent
}

// until is the compact time to a schedule's next run: due, 5m, 3h, 2d.
func (m *Model) until(t time.Time) string {
	d := t.Sub(m.now())
	switch {
	case d < time.Minute:
		return "due"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m *Model) scheduleRows(p palette, l layout) []string {
	out := make([]string, 0, l.listHeight)
	if len(m.schedList) == 0 {
		text := "No scheduled prompts. Press p on a session to send it one at an interval, or n to send one through an agent's profile."
		if m.engine.Demo {
			text = "Demo mode has no scheduled prompts."
		}
		for _, line := range wrap(text, l.listWidth-2, l.listHeight) {
			out = append(out, fit("  "+paint{p: p}.fg(p.muted, line), l.listWidth))
		}
	}
	for i := 0; len(m.schedList) > 0 && i < l.listHeight; i++ {
		n := m.schedOffset + i
		if n >= len(m.schedList) {
			break
		}
		out = append(out, m.scheduleRow(p, m.schedList[n], l.listWidth, n == m.schedCursor))
	}
	for len(out) < l.listHeight {
		out = append(out, strings.Repeat(" ", l.listWidth))
	}
	return out
}

func (m *Model) scheduleRow(p palette, sc app.Schedule, width int, selected bool) string {
	t := paint{p: p, selected: selected}
	glyph, label, tone := scheduleState(p, sc)
	marker := t.plain(" ")
	title := t.plain
	if selected {
		marker, title = t.fg(p.accent, "▌"), t.bold
	}
	agent := session.Provider(sc.Agent)
	if !sc.IsProfile() {
		agent, _, _ = session.SplitKey(sc.Session)
	}
	name, text := m.scheduleLabel(sc)
	when := "every " + shortDuration(sc.Every)
	next := "—"
	if !sc.Paused {
		next = m.until(sc.Next)
	}
	// marker, glyph and label take 13 columns, the agent 4, the interval 12
	// and the next run 5.
	nameWidth := min(16, max(8, width/6))
	rest := max(0, width-13-4-nameWidth-2-12-5)
	out := marker + t.plain(" ") + t.fg(tone, glyph) + t.plain(" ") + t.fg(tone, fit(label, 8)) + t.plain(" ") +
		t.fg(p.muted, fit(m.short(agent), 2)) + t.plain("  ") + t.fg(p.muted, fit(name, nameWidth)) + t.plain("  ") +
		title(fit(text, rest)) + t.fg(p.muted, fit(" "+when, 12)) + t.fg(p.muted, lpad(next, 5))
	return out + t.plain(strings.Repeat(" ", max(0, width-ansi.StringWidth(out))))
}

func (m *Model) scheduleDetails(p palette, width, height int) []string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	sc, ok := m.selectedSchedule()
	if !ok {
		return []string{muted.Render("Select a scheduled prompt to see it.")}
	}
	glyph, label, tone := scheduleState(p, sc)
	_, title := m.scheduleLabel(sc)
	agent := session.Provider(sc.Agent)
	if !sc.IsProfile() {
		agent, _, _ = session.SplitKey(sc.Session)
	}
	var lines []string
	for _, l := range wrap(title, width, 2) {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render(l))
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(tone).Render(glyph+" "+label)+muted.Render(" · "+m.title(agent)), "")

	runs := "Every " + shortDuration(sc.Every) + " · next " + sc.Next.Local().Format("Jan 2 15:04")
	if sc.Paused {
		runs = "Every " + shortDuration(sc.Every) + " · paused"
	}
	choice := scheduleChoice(sc)
	if choice == "" {
		choice = "the session's or profile's own"
	}
	lines = append(lines, muted.Render("Runs"), fit(runs, width), fit("Model "+choice, width))
	if sc.ConnectionName != "" {
		lines = append(lines, fit("Connection "+session.Text(sc.ConnectionName, 100), width))
	}
	lines = append(lines, "", muted.Render("Last run"))
	switch {
	case sc.Last.IsZero():
		lines = append(lines, "Not sent yet")
	case sc.LastError != "":
		for _, l := range wrap("Failed "+m.ago(sc.Last)+": "+session.Text(sc.LastError, 300), width, 3) {
			lines = append(lines, lipgloss.NewStyle().Foreground(p.waiting).Render(l))
		}
	default:
		lines = append(lines, "Sent "+m.ago(sc.Last))
	}
	where := "In agentflockr's own folder; the agent keeps no session"
	if sc.IsProfile() && session.Provider(sc.Agent) == session.OpenCode {
		where = "In agentflockr's own folder; each run's OpenCode session is deleted after its turn"
	}
	if !sc.IsProfile() {
		where = "Into the session"
		if s, ok := m.listedSession(sc.Session); ok {
			where += " in " + shortPath(s.Directory)
		}
	}
	lines = append(lines, "", muted.Render("Where"))
	lines = append(lines, wrap(where, width, 2)...)
	bottom := []string{"", muted.Render("↵ edit · space pause · d delete")}
	room := max(1, height-len(lines)-len(bottom)-2)
	lines = append(lines, "", muted.Render("Prompt"))
	lines = append(lines, wrap(session.Text(sc.Prompt, 2000), width, min(8, room))...)
	return append(lines, bottom...)
}

// scheduleBrief is the two-line summary on screens too narrow for the
// details pane.
func (m *Model) scheduleBrief(p palette, width int) []string {
	sc, ok := m.selectedSchedule()
	if !ok {
		return []string{"", ""}
	}
	muted := lipgloss.NewStyle().Foreground(p.muted)
	_, title := m.scheduleLabel(sc)
	return []string{
		fit(lipgloss.NewStyle().Bold(true).Render(title)+muted.Render("  "+scheduleStatus(sc, m.ago)), width),
		muted.Render(fit(session.Text(sc.Prompt, 400), width)),
	}
}
