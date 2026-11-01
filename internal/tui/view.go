package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/buildinfo"
	"github.com/psicomante/agentflockr/internal/session"
)

// Screen geometry, shared by rendering and mouse hit-testing. Every line
// keeps a one-column margin on both sides.
const (
	margin       = 1
	brand        = "agentflockr"
	sessionsTabX = margin + len(brand) + 3
	projectsTabX = sessionsTabX + len("Sessions") + 2
	minWidth     = 50
	minHeight    = 12
)

// layout places the list and the details. Wide screens show the details
// beside the list; narrower ones show a two-line brief below it.
type layout struct {
	listTop, listWidth, listHeight int
	detailWidth                    int
	brief                          bool
}

func (m *Model) layout() layout {
	content := max(0, m.width-2*margin)
	l := layout{listTop: 3, listWidth: content}
	body := m.height - 4 // header, context line, blank line, footer
	if m.height >= 20 {
		body-- // breathing room above the footer
	}
	if m.width >= 110 && m.height >= 16 {
		l.detailWidth = min(56, max(36, content/3))
		l.listWidth = content - l.detailWidth - 3
	} else if body >= 10 {
		l.brief = true
		body -= 3
	}
	l.listHeight = max(0, body)
	return l
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "agentflockr"
	if m.mode == browsing || m.mode == searching {
		// Mouse capture gives wheel scrolling and clicks; most terminals still
		// select text with Shift or Option held.
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *Model) render() string {
	p := paletteFor(m.dark)
	var rows []string
	switch {
	case m.width < minWidth || m.height < minHeight:
		text := fmt.Sprintf("Make the window larger\n(at least %d × %d).\n\nq quits", minWidth, minHeight)
		rows = strings.Split(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, text), "\n")
	case m.mode == helping:
		rows = m.overlay(m.helpBox(p))
	case m.mode == diagnosing:
		rows = m.overlay(m.diagnosticsBox(p))
	case m.mode == confirmRemove || m.mode == confirmDelete || m.mode == moving:
		rows = m.overlay(m.changeBox(p))
	case m.mode == startingNew:
		rows = m.overlay(m.startBox(p))
	default:
		rows = m.dashboard(p)
	}
	// Clip as a final guard against long paths and wide Unicode.
	if len(rows) > m.height {
		rows = rows[:max(0, m.height)]
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], max(0, m.width), "")
	}
	return strings.Join(rows, "\n")
}

func (m *Model) dashboard(p palette) []string {
	l := m.layout()
	pad := strings.Repeat(" ", margin)
	rows := []string{m.header(p), m.contextLine(p), ""}
	list := m.list(p, l)
	if l.detailWidth > 0 {
		details := m.details(p, l.detailWidth, l.listHeight)
		bar := m.scrollbar(p, l.listHeight)
		for i := range list {
			list[i] += " " + bar[i] + " " + details[i]
		}
	}
	for _, row := range list {
		rows = append(rows, pad+row)
	}
	if l.brief {
		rows = append(rows, "")
		for _, row := range m.brief(p, l.listWidth) {
			rows = append(rows, pad+row)
		}
	}
	for len(rows) < m.height-1 {
		rows = append(rows, "")
	}
	return append(rows, m.footer(p))
}

// spread places left and right on one line of the full width, dropping the
// right part when both do not fit.
func (m *Model) spread(left, right string) string {
	room := m.width - 2*margin
	gap := room - ansi.StringWidth(left) - ansi.StringWidth(right)
	pad := strings.Repeat(" ", margin)
	if gap < 2 {
		return pad + ansi.Truncate(left, room, "…")
	}
	return pad + left + strings.Repeat(" ", gap) + right
}

func (m *Model) header(p palette) string {
	plain := paint{p: p}
	tab := func(name string, active bool) string {
		if active {
			return lipgloss.NewStyle().Bold(true).Underline(true).Render(name)
		}
		return plain.fg(p.muted, name)
	}
	left := lipgloss.NewStyle().Foreground(p.accent).Bold(true).Render(brand) + "   " +
		tab("Sessions", !m.projects) + "  " + tab("Projects", m.projects)
	need, busy := 0, 0
	for _, s := range m.snapshot.Sessions {
		if !s.Root() {
			continue
		}
		switch s.Status {
		case session.Waiting, session.Failed:
			need++
		case session.Working:
			busy++
		}
	}
	var chips []string
	if m.engine.Demo {
		chips = append(chips, plain.fg(p.muted, "demo"))
	}
	if need > 0 {
		chips = append(chips, plain.fg(p.waiting, "● "+needYou(need)))
	}
	if busy > 0 {
		chips = append(chips, plain.fg(p.working, fmt.Sprintf("◐ %d working", busy)))
	}
	if need == 0 && busy == 0 {
		chips = append(chips, plain.fg(p.muted, "nothing needs you"))
	}
	return m.spread(left, strings.Join(chips, "   "))
}

func (m *Model) contextLine(p palette) string {
	plain := paint{p: p}
	crumbs := []string{m.agentLabel()}
	if m.filter.project != "" {
		for _, s := range m.snapshot.Sessions {
			if s.ProjectID == m.filter.project {
				crumbs = append(crumbs, projectName(s))
				break
			}
		}
	}
	left := plain.fg(p.muted, strings.Join(crumbs, " › "))
	if m.filter.query != "" && m.mode != searching {
		left += plain.fg(p.muted, "  /") + " " + session.Text(m.filter.query, 40)
	}
	switch {
	case m.filter.project != "":
		left += plain.fg(p.muted, "   esc back")
	case m.filter.active():
		left += plain.fg(p.muted, "   esc clears")
	}
	if m.notice != "" {
		notice := plain.fg(p.waiting, session.Text(m.notice, 200))
		room := m.width - 2*margin
		if ansi.StringWidth(left)+2+ansi.StringWidth(notice) > room {
			// The notice says what just happened, often why something failed;
			// it matters more than the breadcrumbs, which spread would keep
			// while dropping the notice. Show it alone, shortened to fit.
			return strings.Repeat(" ", margin) + ansi.Truncate(notice, room, "…")
		}
		return m.spread(left, notice)
	}
	noun := "session"
	if m.projects {
		noun = "project"
	}
	count := plural(len(m.items), noun)
	state := "updated " + m.ago(m.snapshot.UpdatedAt)
	if m.scanning || m.snapshot.UpdatedAt.IsZero() {
		state = "refreshing…"
	}
	right := plain.fg(p.muted, count+" · "+state)
	if n := len(m.snapshot.Warnings); n > 0 {
		notices := fmt.Sprintf("! %d notices", n)
		if n == 1 {
			notices = "! 1 notice"
		}
		right += "   " + plain.fg(p.waiting, notices)
	}
	return m.spread(left, right)
}

func (m *Model) list(p palette, l layout) []string {
	out := make([]string, 0, l.listHeight)
	if len(m.items) == 0 {
		for _, text := range wrap(m.emptyText(), l.listWidth-2, l.listHeight) {
			out = append(out, fit("  "+paint{p: p}.fg(p.muted, text), l.listWidth))
		}
	}
	for i := 0; len(m.items) > 0 && i < l.listHeight; i++ {
		n := m.offset + i
		switch {
		case n >= len(m.lines):
			out = append(out, strings.Repeat(" ", l.listWidth))
		case m.lines[n].heading:
			out = append(out, m.heading(p, m.lines[n], l.listWidth))
		default:
			it := m.lines[n].item
			out = append(out, m.row(p, m.items[it], l.listWidth, it == m.cursor))
		}
	}
	for len(out) < l.listHeight {
		out = append(out, strings.Repeat(" ", l.listWidth))
	}
	return out
}

func (m *Model) emptyText() string {
	switch {
	case m.filter.active():
		return "Nothing matches. Press esc to clear the filters."
	case len(m.snapshot.Sessions) == 0 && m.scanning:
		return "Looking for sessions…"
	case len(m.snapshot.Sessions) == 0:
		return "No sessions found. Start a session in your agent's CLI and it appears here. Press ! to see where agentflockr looks."
	}
	return "No sessions to show."
}

func (m *Model) heading(p palette, ln line, width int) string {
	title := lipgloss.NewStyle().Foreground(p.muted).Bold(true).Render(groupNames[ln.group])
	count := lipgloss.NewStyle().Foreground(p.faint).Render(fmt.Sprint(ln.count))
	return fit("  "+title+"  "+count, width)
}

func (m *Model) row(p palette, it item, width int, selected bool) string {
	t := paint{p: p, selected: selected}
	s := it.session
	glyph, label := mark(s.Status)
	color := p.status(s.Status)
	marker := t.plain(" ")
	if selected {
		marker = t.fg(p.accent, "▌")
	}
	title := t.plain
	if selected {
		title = t.bold
	}
	out := marker + t.plain(" ") + t.fg(color, glyph) + t.plain(" ") + t.fg(color, fit(label, 8)) + t.plain(" ")
	project := projectName(s)
	projectColor := p.muted
	if s.Missing {
		project = "! " + project
		projectColor = p.waiting
	}
	if it.project {
		// marker, glyph and label take 13 columns; the gaps and age take 9.
		name := min(24, max(10, width/4))
		counts := plural(it.sessions, "session")
		rest := max(0, width-13-name-2-12-2-1-4)
		nameText := t.plain(fit(project, name))
		if s.Missing {
			nameText = t.fg(projectColor, fit(project, name))
		}
		out += nameText + t.plain("  ") + t.fg(p.muted, fit(counts, 12)) + t.plain("  ") +
			t.fg(p.muted, fit(session.Text(s.Title, 200), rest))
	} else {
		agent := width >= 60
		name := min(20, max(8, width/5))
		fixed := 13 + 2 + 1 + 4
		if agent {
			fixed += 4
		}
		rest := max(0, width-fixed-name)
		if agent {
			out += t.fg(p.muted, fit(m.short(s.Provider), 2)) + t.plain("  ")
		}
		out += t.fg(projectColor, fit(project, name)) + t.plain("  ") + title(fit(session.Text(s.Title, 200), rest))
	}
	out += t.plain(" ") + t.fg(p.muted, lpad(m.age(it.updated), 4))
	// Pad with the row's own background so a selection spans the full width.
	return out + t.plain(strings.Repeat(" ", max(0, width-ansi.StringWidth(out))))
}

// scrollbar draws the divider between the list and the details, with a thumb
// when the list is longer than the screen.
func (m *Model) scrollbar(p palette, height int) []string {
	track := lipgloss.NewStyle().Foreground(p.faint).Render("│")
	thumb := lipgloss.NewStyle().Foreground(p.muted).Render("┃")
	bar := make([]string, height)
	total := len(m.lines)
	size, start := height, 0
	if total > height && height > 0 {
		size = max(1, height*height/total)
		start = min(height-size, m.offset*height/total)
		if m.offset+height >= total {
			start = height - size
		}
	}
	for i := range bar {
		bar[i] = track
		if total > height && i >= start && i < start+size {
			bar[i] = thumb
		}
	}
	return bar
}

func (m *Model) details(p palette, width, height int) []string {
	it, ok := m.selected()
	var lines []string
	switch {
	case !ok:
		lines = []string{lipgloss.NewStyle().Foreground(p.muted).Render("Select a session to see its details.")}
	case it.project:
		lines = m.projectDetails(p, it, width, height)
	default:
		lines = m.sessionDetails(p, it.session, width, height)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]
	for i := range lines {
		lines[i] = fit(lines[i], width)
	}
	return lines
}

func (m *Model) sessionDetails(p palette, s session.Session, width, height int) []string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	glyph, label := mark(s.Status)
	var top []string
	for _, l := range wrap(session.Text(s.Title, 200), width, 2) {
		top = append(top, lipgloss.NewStyle().Bold(true).Render(l))
	}
	top = append(top, lipgloss.NewStyle().Foreground(p.status(s.Status)).Render(glyph+" "+label)+
		muted.Render(" · "+m.title(s.Provider)+" · "+m.ago(s.UpdatedAt)), "")
	activity := m.activity(s)
	heading := "Latest activity"
	if preview, ok := m.currentPreview(s); ok {
		if preview.loading {
			heading += " · loading…"
		} else if preview.err != nil {
			heading += " · showing the indexed excerpt"
		}
	}
	if strings.TrimSpace(activity) == "" {
		activity = "No activity excerpt yet."
	}
	top = append(top, muted.Render(heading))

	folder := []string{shortPath(s.Directory)}
	if s.Missing {
		folder = append(folder, lipgloss.NewStyle().Foreground(p.waiting).Render("Folder not found · m to move"))
	}
	bottom := []string{"", muted.Render("Evidence")}
	for _, l := range wrap(session.Text(s.Evidence, 300), width, 2) {
		bottom = append(bottom, muted.Render(l))
	}
	bottom = append(bottom, "", muted.Render("Folder"))
	for _, l := range folder {
		bottom = append(bottom, wrap(l, width, 2)...)
	}
	bottom = append(bottom, "", muted.Render("Session"), muted.Render(fit(session.Text(string(s.Provider)+" · "+s.ID, 200), width)))
	if len(top)+len(bottom)+1 > height {
		// Short screens keep the facts and drop the section labels.
		bottom = []string{"", muted.Render(fit(session.Text(s.Evidence, 300), width)), shortPath(s.Directory)}
	}
	// Sections flow from the top; the activity excerpt takes the room the
	// facts below it leave, up to a readable paragraph.
	room := min(10, max(1, height-len(top)-len(bottom)))
	top = append(top, wrap(session.Text(activity, 600), width, room)...)
	return append(top, bottom...)
}

func (m *Model) projectDetails(p palette, it item, width, height int) []string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	s := it.session
	lines := []string{lipgloss.NewStyle().Bold(true).Render(fit(projectName(s), width)), muted.Render(fit(shortPath(s.Directory), width))}
	if s.Missing {
		lines = append(lines, lipgloss.NewStyle().Foreground(p.waiting).Render("Folder not found · m to move"))
	}
	need := 0
	for _, member := range it.members {
		if member.Status == session.Waiting || member.Status == session.Failed {
			need++
		}
	}
	summary := plural(it.sessions, "session")
	if need > 0 {
		summary += " · " + needYou(need)
	}
	if it.working > 0 {
		summary += fmt.Sprintf(" · %d working", it.working)
	}
	lines = append(lines, "", muted.Render(summary), "", muted.Render("Sessions"))
	for _, member := range it.members {
		if len(lines) >= height-1 && len(it.members) > 1 {
			lines = append(lines, muted.Render("…  enter shows them all"))
			break
		}
		glyph, _ := mark(member.Status)
		title := fit(session.Text(member.Title, 200), max(1, width-7))
		lines = append(lines, lipgloss.NewStyle().Foreground(p.status(member.Status)).Render(glyph)+" "+title+" "+muted.Render(lpad(m.age(member.UpdatedAt), 4)))
	}
	return lines
}

// brief is the two-line summary of the selection on screens too narrow for
// the details pane.
func (m *Model) brief(p palette, width int) []string {
	it, ok := m.selected()
	if !ok {
		return []string{"", ""}
	}
	muted := lipgloss.NewStyle().Foreground(p.muted)
	s := it.session
	if it.project {
		first := lipgloss.NewStyle().Bold(true).Render(projectName(s)) + muted.Render("  "+shortPath(s.Directory))
		return []string{fit(first, width), muted.Render(fit("Latest: "+session.Text(s.Title, 200), width))}
	}
	activity := m.activity(s)
	first := lipgloss.NewStyle().Bold(true).Render(session.Text(s.Title, 200)) + muted.Render("  "+m.title(s.Provider)+" · "+shortPath(s.Directory))
	return []string{fit(first, width), muted.Render(fit(session.Text(activity, 600), width))}
}

func (m *Model) footer(p palette) string {
	plain := paint{p: p}
	room := m.width - 2*margin
	version := buildinfo.Version
	if release, _, found := strings.Cut(version, "+"); found && ansi.StringWidth(version) > room/4 {
		version = release
	}
	right := plain.fg(p.muted, version)
	if m.mode == searching {
		hint := plain.fg(p.muted, "↵ keep   esc cancel")
		// Size a copy, so rendering leaves the model unchanged.
		input := m.input
		input.SetWidth(max(10, min(60, room-ansi.StringWidth(version)-ansi.StringWidth("↵ keep   esc cancel")-8)))
		return m.spread(input.View()+"   "+hint, right)
	}
	keys := [][2]string{{"↑↓", "move"}, {"↵", "open"}, {"tab", "projects"}, {"/", "search"}, {"a", "agent"}, {"?", "help"}, {"q", "quit"}}
	switch {
	case m.filter.project != "":
		keys = [][2]string{{"↑↓", "move"}, {"↵", "open"}, {"n", "new"}, {"esc", "back"}, {"/", "search"}, {"a", "agent"}, {"?", "help"}, {"q", "quit"}}
	case m.projects:
		keys = [][2]string{{"↑↓", "move"}, {"↵", "open"}, {"n", "new"}, {"tab", "sessions"}, {"/", "search"}, {"a", "agent"}, {"?", "help"}, {"q", "quit"}}
	}
	left := hints(p, keys)
	// Drop hints from the middle, keeping navigation, help and quit.
	for len(keys) > 3 && ansi.StringWidth(left)+ansi.StringWidth(version)+2 > room {
		keys = append(keys[:len(keys)-3], keys[len(keys)-2:]...)
		left = hints(p, keys)
	}
	return m.spread(left, right)
}

// hints renders key and action pairs, such as "↵ open   / search".
func hints(p palette, keys [][2]string) string {
	var parts []string
	for _, k := range keys {
		parts = append(parts, k[0]+" "+lipgloss.NewStyle().Foreground(p.muted).Render(k[1]))
	}
	return strings.Join(parts, "   ")
}

func (m *Model) overlay(box string) []string {
	return strings.Split(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box), "\n")
}

func (m *Model) box(p palette, title string, titleColor color.Color, body []string, footer string) string {
	width := min(72, m.width-4)
	content := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(title) + "\n\n" + strings.Join(body, "\n") + "\n\n" + footer
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.faint).
		Padding(1, 2).Width(width).MaxHeight(m.height).Render(content)
}

func (m *Model) helpBox(p palette) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	keys := [][2]string{
		{"↑ ↓  j k", "Move"},
		{"PgUp PgDn", "Page through the list"},
		{"g  G", "First or last row"},
		{"Tab", "Switch between sessions and projects"},
		{"Enter", "Open the session in its agent, or a project"},
		{"/", "Search titles, activity, folders and IDs"},
		{"a", "Show one agent at a time"},
		{"Esc", "Clear filters, or leave a project"},
		{"n", "Start a new session in this project"},
		{"m", "Move the project to a new folder"},
		{"x", "Remove from agentflockr; the agent keeps it"},
		{"d", "Delete the session from its agent"},
		{"r", "Refresh now"},
		{"!", "Where agentflockr looks for sessions"},
		{"q", "Quit"},
	}
	var body []string
	for _, k := range keys {
		body = append(body, fit(k[0], 12)+muted.Render(k[1]))
	}
	body = append(body, "", lipgloss.NewStyle().Bold(true).Render("Statuses"))
	statuses := []struct {
		status session.Status
		text   string
	}{
		{session.Waiting, "A permission, question or input request is pending"},
		{session.Failed, "The last turn reported an error"},
		{session.Working, "A turn is running"},
		{session.Completed, "The last turn finished"},
		{session.Idle, "No turn is running"},
		{session.Unknown, "No reliable evidence yet, or cached history"},
	}
	for _, s := range statuses {
		glyph, label := mark(s.status)
		body = append(body, lipgloss.NewStyle().Foreground(p.status(s.status)).Render(fit(glyph+" "+label, 12))+muted.Render(s.text))
	}
	return m.box(p, "Keys", p.accent, body, lipgloss.NewStyle().Foreground(p.muted).Render("esc to close"))
}

func (m *Model) diagnosticsBox(p palette) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	var body []string
	if m.engine.Demo {
		body = append(body, "Demo mode shows sample sessions. No agent data is read.")
		return m.box(p, "Diagnostics", p.accent, body, lipgloss.NewStyle().Foreground(p.muted).Render("esc to close"))
	}
	for _, a := range m.engine.Providers {
		body = append(body, a.Meta().Source)
	}
	body = append(body, "")
	if len(m.snapshot.Warnings) == 0 {
		body = append(body, muted.Render("The last scan reported no problems."))
	}
	for _, w := range m.snapshot.Warnings {
		body = append(body, lipgloss.NewStyle().Foreground(p.waiting).Render("! ")+session.Text(w, 300))
	}
	return m.box(p, "Diagnostics", p.accent, body, lipgloss.NewStyle().Foreground(p.muted).Render("esc to close"))
}

func needYou(n int) string {
	if n == 1 {
		return "1 needs you"
	}
	return fmt.Sprintf("%d need you", n)
}

func (m *Model) short(id session.Provider) string {
	if a := m.engine.Providers.Get(id); a != nil {
		return a.Meta().Short
	}
	return strings.ToUpper(string(id)[:min(2, len(id))])
}

func (m *Model) title(id session.Provider) string {
	if a := m.engine.Providers.Get(id); a != nil {
		return a.Meta().Title
	}
	return string(id)
}

// age is the compact form for list columns: now, 5m, 3h, 2d.
func (m *Model) age(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := m.now().Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m *Model) ago(t time.Time) string {
	switch a := m.age(t); a {
	case "now":
		return "just now"
	case "—":
		return "never"
	default:
		return a + " ago"
	}
}

func shortPath(path string) string {
	path = session.Text(path, 4096)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// fit truncates or pads plain or styled text to exactly w columns.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

func lpad(s string, w int) string {
	s = ansi.Truncate(s, w, "")
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + s
}

// wrap breaks plain text at word boundaries, hard-wrapping long words such
// as paths, and ends with an ellipsis when it needs more than limit lines.
func wrap(s string, w, limit int) []string {
	if w <= 0 || limit <= 0 {
		return nil
	}
	s = ansi.Hardwrap(ansi.Wordwrap(s, w, ""), w, true)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = ansi.Truncate(lines[limit-1], w-1, "") + "…"
	}
	return lines
}
