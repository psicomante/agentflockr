package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/session"
)

// SettingKind is how a setting is edited in the Settings view.
type SettingKind int

const (
	SettingTheme    SettingKind = iota // the theme picker, previewing as it moves
	SettingDuration                    // text such as "5s" or "10m"
	SettingAgents                      // which agents to watch, in order
	SettingFolder                      // a folder path; "~/" allowed
	SettingText                        // other text, checked when saved
	SettingFixed                       // shown, never edited
)

// SettingApplies says when a saved value takes effect.
type SettingApplies int

const (
	AppliesNow       SettingApplies = iota // the dashboard uses it at once
	AppliesNextStart                       // at agentflockr's next start
	AppliesNextServe                       // when agentflockr serve next starts
)

// Setting is one row of the Settings view (REQ-DSH-019). internal/cli
// builds the rows, so the dashboard never reads the settings file or the
// environment. Values are text, as a person types them; agents are IDs
// joined by commas.
type Setting struct {
	Key      string // the file's key, such as "codex.stale-after"
	About    string // one or two sentences for the details pane
	Kind     SettingKind
	Applies  SettingApplies
	File     string // the file's value; "" when the file leaves it out
	Default  string // the built-in value, or how it is found
	InEffect string // the value this run uses; "" means the default
	// Source is the flag or variable that set InEffect over the file, such
	// as "--refresh" or "CODEX_HOME"; "" when the file or default holds.
	Source string
	// Pending is true when the file's value changed since this run started
	// and waits for the next start, or for serve's.
	Pending bool
	Choices []string // SettingAgents: every agent ID, in the default order
	// ChoiceNames are the Choices' names, as shown; an agent not watched
	// this run has no adapter for the dashboard to ask.
	ChoiceNames []string
	// How says where to change a SettingFixed row.
	How string
}

// The Settings view lists the settings internal/cli gives, explains the
// selected one, and changes them through an editor per kind of value. It is
// the fourth view, reached with "," from the other three, and leaving it
// shows whichever was there before, since settingsShown only covers them.

// settingEditor is the open editor: a text field, the agents checklist or
// the theme picker. Nothing in it is kept once it closes.
type settingEditor struct {
	row     Setting
	input   textinput.Model
	agents  []string        // SettingAgents: IDs in the order shown
	checked map[string]bool // SettingAgents: the agents to watch
	cursor  int             // the agents or themes row
	before  string          // SettingTheme: the theme on screen when it opened
	saving  bool
	err     string
}

// settingSavedMsg reports a save from the Settings view.
type settingSavedMsg struct {
	key   string
	reset bool
	rows  []Setting
	err   error
}

func (m *Model) selectedSetting() (Setting, bool) {
	if len(m.setRows) == 0 {
		return Setting{}, false
	}
	return m.setRows[min(max(m.setCursor, 0), len(m.setRows)-1)], true
}

// showSettings shows or leaves the Settings view. It is not saved as the
// remembered view: the next start opens on the lists.
func (m *Model) showSettings(on bool) {
	m.settingsShown = on
	if on {
		m.setCursor, m.setOffset = 0, 0
	}
}

func (m *Model) ensureSettingVisible() {
	height := m.layout().listHeight
	if height <= 0 {
		m.setOffset = 0
		return
	}
	if m.setCursor < m.setOffset {
		m.setOffset = m.setCursor
	}
	if m.setCursor >= m.setOffset+height {
		m.setOffset = m.setCursor - height + 1
	}
	m.setOffset = min(max(m.setOffset, 0), max(0, len(m.setRows)-height))
}

func (m *Model) moveSetting(delta int) {
	if len(m.setRows) == 0 {
		return
	}
	m.setCursor = min(max(m.setCursor+delta, 0), len(m.setRows)-1)
	m.ensureSettingVisible()
}

// clickSetting selects the row under the pointer.
func (m *Model) clickSetting(y int) {
	l := m.layout()
	if y < l.listTop || y >= l.listTop+l.listHeight {
		return
	}
	if n := m.setOffset + y - l.listTop; n < len(m.setRows) {
		m.setCursor = n
	}
}

// updateSettings answers the keys of the Settings view.
func (m *Model) updateSettings(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	page := max(1, m.layout().listHeight-1)
	switch msg.String() {
	case "q":
		return tea.Quit
	case "j", "down":
		m.moveSetting(1)
	case "k", "up":
		m.moveSetting(-1)
	case "pgdown", "ctrl+d":
		m.moveSetting(page)
	case "pgup", "ctrl+u":
		m.moveSetting(-page)
	case "g", "home":
		m.moveSetting(-len(m.setRows))
	case "G", "end":
		m.moveSetting(len(m.setRows))
	case ",", "tab", "esc":
		m.showSettings(false)
	case "enter", "e":
		if row, ok := m.selectedSetting(); ok {
			return m.editSetting(row)
		}
	case "x":
		row, ok := m.selectedSetting()
		switch {
		case !ok || row.Kind == SettingFixed:
		case m.saveSetting == nil:
			m.notice = "Demo mode saves nothing."
		case row.File == "":
			m.notice = row.Key + " already uses its default."
		default:
			m.editor = settingEditor{row: row}
			m.mode = confirmReset
		}
	case "?":
		m.mode = helping
	case "!":
		m.mode = diagnosing
	}
	return nil
}

// editSetting opens the row's editor.
func (m *Model) editSetting(row Setting) tea.Cmd {
	if row.Kind == SettingFixed {
		m.notice = row.How
		return nil
	}
	if m.saveSetting == nil && row.Applies != AppliesNow {
		m.notice = "Demo mode saves nothing."
		return nil
	}
	e := settingEditor{row: row}
	switch row.Kind {
	case SettingTheme:
		e.before = m.theme
		e.cursor = max(0, themeIndex(first(m.theme, DefaultTheme)))
	case SettingAgents:
		e.checked = map[string]bool{}
		watched := row.Choices
		if row.InEffect != "" {
			watched = strings.Split(row.InEffect, ",")
		}
		for _, id := range watched {
			e.agents, e.checked[id] = append(e.agents, id), true
		}
		for _, id := range row.Choices {
			if !e.checked[id] {
				e.agents = append(e.agents, id)
			}
		}
	default:
		e.input = textinput.New()
		e.input.Prompt = "› "
		e.input.CharLimit = 4096
		e.input.SetVirtualCursor(true)
		e.input.SetValue(first(row.File, row.InEffect))
		e.input.CursorEnd()
	}
	m.editor = e
	m.mode = editingSetting
	m.styleInput()
	if row.Kind == SettingTheme || row.Kind == SettingAgents {
		return nil
	}
	return m.editor.input.Focus()
}

// updateSettingEditor answers the keys of the open editor.
func (m *Model) updateSettingEditor(msg tea.KeyPressMsg) tea.Cmd {
	e := &m.editor
	if e.saving {
		return nil
	}
	key := msg.String()
	if key == "esc" {
		m.mode = browsing
		if e.row.Kind == SettingTheme {
			return m.useTheme(e.before)
		}
		return nil
	}
	switch e.row.Kind {
	case SettingTheme:
		switch key {
		case "down", "j":
			e.cursor = min(e.cursor+1, len(themes)-1)
			return m.useTheme(themes[e.cursor].ID)
		case "up", "k":
			e.cursor = max(e.cursor-1, 0)
			return m.useTheme(themes[e.cursor].ID)
		case "enter":
			id := themes[e.cursor].ID
			if id == first(e.row.File, e.row.Default) && m.saveSetting != nil {
				m.mode = browsing
				return nil
			}
			return m.submitSetting(id)
		}
	case SettingAgents:
		switch key {
		case "down", "j":
			e.cursor = min(e.cursor+1, len(e.agents)-1)
		case "up", "k":
			e.cursor = max(e.cursor-1, 0)
		case "space", " ":
			id := e.agents[e.cursor]
			e.checked[id] = !e.checked[id]
		case "J", "shift+down":
			if e.cursor < len(e.agents)-1 {
				e.agents[e.cursor], e.agents[e.cursor+1] = e.agents[e.cursor+1], e.agents[e.cursor]
				e.cursor++
			}
		case "K", "shift+up":
			if e.cursor > 0 {
				e.agents[e.cursor], e.agents[e.cursor-1] = e.agents[e.cursor-1], e.agents[e.cursor]
				e.cursor--
			}
		case "enter":
			var chosen []string
			for _, id := range e.agents {
				if e.checked[id] {
					chosen = append(chosen, id)
				}
			}
			if len(chosen) == 0 {
				e.err = "Choose at least one agent."
				return nil
			}
			return m.submitSetting(strings.Join(chosen, ","))
		}
	default:
		if key == "enter" {
			value := strings.TrimSpace(e.input.Value())
			if value == "" {
				e.err = "Type a value, or press esc and x to reset it."
				return nil
			}
			return m.submitSetting(value)
		}
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(msg)
		return cmd
	}
	return nil
}

// submitSetting saves the editor's value as a command. In demo mode a
// setting that applies at once changes for the run, and nothing is saved.
func (m *Model) submitSetting(value string) tea.Cmd {
	e := &m.editor
	if m.saveSetting == nil {
		m.mode = browsing
		row := e.row
		row.InEffect = value
		m.replaceRow(row)
		m.notice = row.Key + " set for this run. Demo mode saves nothing."
		return m.applySetting(row)
	}
	e.saving, e.err = true, ""
	save, key := m.saveSetting, e.row.Key
	return func() tea.Msg {
		rows, err := save(key, value, false)
		return settingSavedMsg{key: key, rows: rows, err: err}
	}
}

// updateReset answers the reset confirmation.
func (m *Model) updateReset(msg tea.KeyPressMsg) tea.Cmd {
	e := &m.editor
	if e.saving {
		return nil
	}
	switch msg.String() {
	case "y", "enter":
		e.saving, e.err = true, ""
		save, key := m.saveSetting, e.row.Key
		return func() tea.Msg {
			rows, err := save(key, "", true)
			return settingSavedMsg{key: key, reset: true, rows: rows, err: err}
		}
	case "esc", "n", "q":
		m.mode = browsing
	}
	return nil
}

// finishSetting closes the editor after a save, or keeps it open with the
// reason so Enter can try again.
func (m *Model) finishSetting(msg settingSavedMsg) tea.Cmd {
	e := &m.editor
	e.saving = false
	if msg.err != nil {
		verb := "save"
		if msg.reset {
			verb = "reset"
		}
		// Reasons from the settings file often start with the key, which
		// the message already names.
		reason := strings.TrimPrefix(msg.err.Error(), msg.key+": ")
		e.err = "Could not " + verb + " " + msg.key + ": " + session.Text(reason, 300) + "."
		return nil
	}
	m.mode = browsing
	m.setRows = msg.rows
	m.setCursor = min(m.setCursor, max(0, len(m.setRows)-1))
	row, _ := m.settingRow(msg.key)
	switch {
	case msg.reset:
		m.notice = row.Key + " reset to its default."
	case row.Source != "":
		m.notice = row.Key + " saved. " + row.Source + " still sets it for this run."
	case row.Kind == SettingTheme:
		m.notice = "Theme saved: " + themes[max(0, themeIndex(row.InEffect))].Name + "."
	case row.Applies == AppliesNextStart:
		m.notice = row.Key + " saved. It applies at the next start."
	case row.Applies == AppliesNextServe:
		m.notice = row.Key + " saved. It applies when serve next starts."
	default:
		m.notice = row.Key + " saved."
	}
	return m.applySetting(row)
}

func (m *Model) settingRow(key string) (Setting, bool) {
	for _, r := range m.setRows {
		if r.Key == key {
			return r, true
		}
	}
	return Setting{Key: key}, false
}

func (m *Model) replaceRow(row Setting) {
	for i, r := range m.setRows {
		if r.Key == row.Key {
			m.setRows[i] = row
		}
	}
}

// applySetting puts a setting that applies at once into use. A value a flag
// set stays as the flag says for this run.
func (m *Model) applySetting(row Setting) tea.Cmd {
	if row.Applies != AppliesNow || row.Source != "" {
		return nil
	}
	value := first(row.InEffect, row.Default)
	switch row.Key {
	case "theme":
		return m.useTheme(value)
	case "refresh":
		if d, err := time.ParseDuration(value); err == nil {
			m.interval = d
		}
	case "recent-window":
		if d, err := time.ParseDuration(value); err == nil {
			m.recent = d
			m.rebuild()
		}
	}
	return nil
}

// settingValue is a row's value as the list shows it.
func (m *Model) settingValue(row Setting) string {
	value := first(row.InEffect, row.Default)
	switch row.Kind {
	case SettingTheme:
		if i := themeIndex(value); i >= 0 {
			return themes[i].Name
		}
	case SettingAgents:
		if row.InEffect == "" {
			return "every agent"
		}
		var names []string
		for _, id := range strings.Split(row.InEffect, ",") {
			names = append(names, m.choiceName(row, id))
		}
		return strings.Join(names, ", ")
	}
	return session.Text(value, 300)
}

// choiceName names an agent offered by an agents row.
func (m *Model) choiceName(row Setting, id string) string {
	for i, c := range row.Choices {
		if c == id && i < len(row.ChoiceNames) {
			return row.ChoiceNames[i]
		}
	}
	return m.title(session.Provider(id))
}

// settingMarker is the short note beside a row's value.
func settingMarker(row Setting) string {
	switch {
	case row.Source != "":
		return "set by " + row.Source
	case row.Pending && row.Applies == AppliesNextServe:
		return "next serve"
	case row.Pending:
		return "next start"
	}
	return ""
}

func (m *Model) settingRows(p palette, l layout) []string {
	out := make([]string, 0, l.listHeight)
	if len(m.setRows) == 0 {
		out = append(out, fit("  "+paint{p: p}.fg(p.muted, "No settings to show."), l.listWidth))
	}
	// The longest key, codex.stale-after or claude.stale-after, is 18
	// columns; on narrow screens the key gives way before the value.
	keyWidth := min(20, max(10, l.listWidth*2/5))
	for i := 0; i < l.listHeight && m.setOffset+i < len(m.setRows); i++ {
		n := m.setOffset + i
		row := m.setRows[n]
		t := paint{p: p, selected: n == m.setCursor}
		marker, key := t.plain(" "), t.plain
		if t.selected {
			marker, key = t.fg(p.accent, "▌"), t.bold
		}
		note := settingMarker(row)
		valueWidth := max(0, l.listWidth-2-keyWidth-ansi.StringWidth(note)-2)
		value := t.plain(fit(m.settingValue(row), valueWidth))
		if row.Kind == SettingFixed {
			value = t.fg(p.muted, fit(m.settingValue(row), valueWidth))
		}
		line := marker + t.plain(" ") + key(fit(row.Key, keyWidth-1)) + t.plain(" ") + value
		if note != "" {
			line += t.plain("  ") + t.fg(p.muted, note)
		}
		out = append(out, line+t.plain(strings.Repeat(" ", max(0, l.listWidth-ansi.StringWidth(line)))))
	}
	for len(out) < l.listHeight {
		out = append(out, strings.Repeat(" ", l.listWidth))
	}
	return out
}

func (m *Model) settingContext(p palette) string {
	plain := paint{p: p}
	room := m.width - 2*margin
	demo := ""
	if m.saveSetting == nil {
		demo = "   Demo mode saves nothing."
	}
	// The path keeps its end, which names the file, as the move dialog does.
	left := plain.fg(p.muted, tail(session.Text(m.setFile, 300), max(1, room-ansi.StringWidth(demo)))+demo)
	if m.notice == "" {
		return m.spread(left, "")
	}
	notice := plain.fg(p.waiting, session.Text(m.notice, 300))
	if ansi.StringWidth(left)+2+ansi.StringWidth(notice) > room {
		// What just happened matters more than the path; show it alone.
		return strings.Repeat(" ", margin) + ansi.Truncate(notice, room, "…")
	}
	return m.spread(left, notice)
}

// settingApplies says when a row's saved value applies, for the details.
func settingApplies(row Setting) string {
	switch row.Applies {
	case AppliesNow:
		return "at once"
	case AppliesNextServe:
		return "when serve next starts"
	}
	return "at the next start"
}

func (m *Model) settingDetails(p palette, width, height int) []string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	row, ok := m.selectedSetting()
	if !ok {
		return []string{muted.Render("No settings to show.")}
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render(fit(row.Key, width)), ""}
	lines = append(lines, wrap(row.About, width, 4)...)
	inFile := "not set"
	if row.File != "" {
		shown := row
		shown.InEffect = row.File
		inFile = m.settingValue(shown)
	}
	inEffect := m.settingValue(row)
	if row.Source != "" {
		inEffect += ", set by " + row.Source
	}
	defaults := row
	defaults.InEffect = ""
	lines = append(lines, "")
	for _, kv := range [][2]string{{"In effect", inEffect}, {"In the file", inFile}, {"Default", m.settingValue(defaults)}, {"Applies", settingApplies(row)}} {
		lines = append(lines, muted.Render(fit(kv[0], 12))+fit(kv[1], max(0, width-12)))
	}
	var note string
	switch {
	case row.Kind == SettingFixed:
		note = row.How
	case row.Source != "":
		note = "The file's value waits until " + row.Source + " is gone."
	case row.Pending:
		note = "Saved. It applies " + settingApplies(row) + "."
	}
	if note != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(note, width, 3)...)
	}
	if row.Kind != SettingFixed {
		lines = append(lines, "", muted.Render("↵ edit · x reset"))
	}
	return lines
}

// settingBrief is the two-line summary on screens too narrow for the
// details pane.
func (m *Model) settingBrief(p palette, width int) []string {
	row, ok := m.selectedSetting()
	if !ok {
		return []string{"", ""}
	}
	return []string{
		fit(lipgloss.NewStyle().Bold(true).Render(row.Key), width),
		lipgloss.NewStyle().Foreground(p.muted).Render(fit(row.About, width)),
	}
}

// settingBox draws the open editor.
func (m *Model) settingBox(p palette) string {
	e := m.editor
	muted := lipgloss.NewStyle().Foreground(p.muted)
	width := m.dialogWidth()
	// The frame takes eight rows: border, padding, title, footer and the
	// blank lines between them. The body gets the rest.
	room := max(1, m.height-8)
	var body []string
	enter := [2]string{"↵", "save"}
	if m.saveSetting == nil {
		enter = [2]string{"↵", "use for this run"}
	}
	keys := [][2]string{enter, {"esc", "cancel"}}
	switch e.row.Kind {
	case SettingTheme:
		keys = [][2]string{{"↑↓", "preview"}, enter, {"esc", "cancel"}}
		body = m.themeRows(p, width, room)
	case SettingAgents:
		keys = [][2]string{{"space", "choose"}, {"J K", "reorder"}, enter, {"esc", "cancel"}}
		for i, id := range e.agents {
			box := "[ ]"
			if e.checked[id] {
				box = "[x]"
			}
			line := "  " + box + " " + m.choiceName(e.row, id)
			if i == e.cursor {
				line = lipgloss.NewStyle().Foreground(p.accent).Render("›") + " " + box + " " + m.choiceName(e.row, id)
			}
			body = append(body, fit(line, width))
		}
	default:
		input := e.input
		input.SetWidth(max(10, width-4))
		body = []string{input.View()}
		if about := wrap(e.row.About, width, room-len(body)-1); len(about) > 0 {
			body = append(append(about, ""), body...)
		}
	}
	if e.err != "" {
		body = append(body, "")
		body = append(body, wrap(e.err, width, 3)...)
		for i := len(body) - 1; i >= 0 && body[i] != ""; i-- {
			body[i] = lipgloss.NewStyle().Foreground(p.failed).Render(body[i])
		}
	}
	// A footer that wrapped would push the box's border off a short screen;
	// the first hints give way, leaving Enter and Esc.
	for len(keys) > 2 && ansi.StringWidth(hints(p, keys)) > width {
		keys = keys[1:]
	}
	footer := hints(p, keys)
	if e.saving {
		footer = muted.Render("Saving…")
	}
	if len(body) > room {
		body = body[len(body)-room:]
	}
	return m.box(p, e.row.Key, p.accent, body, footer)
}

// themeRows lists the themes for the picker, as a window that keeps the
// cursor in view, with a line of every status in the theme on screen when
// there is room.
func (m *Model) themeRows(p palette, width, room int) []string {
	e := m.editor
	saved := e.row.File
	var rows []string
	for i, t := range themes {
		line := "  " + t.Name
		if i == e.cursor {
			line = paint{p: p, selected: true}.fg(p.accent, "›") + paint{p: p, selected: true}.plain(" "+t.Name)
		}
		if t.ID == saved {
			line = fit(line, max(0, width-6)) + lipgloss.NewStyle().Foreground(p.muted).Render(lpad("saved", 6))
		}
		rows = append(rows, line)
	}
	var statuses []string
	for _, value := range session.Status("").Values() {
		s := session.Status(value)
		glyph, label := mark(s)
		statuses = append(statuses, lipgloss.NewStyle().Foreground(p.status(s)).Render(glyph+" "+label))
	}
	status := []string{strings.Join(statuses[:3], "  "), strings.Join(statuses[3:], "  ")}
	if ansi.StringWidth(strings.Join(statuses, "  ")) <= width {
		status = []string{strings.Join(statuses, "  ")}
	}
	if len(rows)+1+len(status) <= room {
		return append(append(rows, ""), status...)
	}
	start := min(max(0, e.cursor-room/2), max(0, len(rows)-room))
	return rows[start:min(len(rows), start+room)]
}

// resetBox asks before a setting goes back to its default.
func (m *Model) resetBox(p palette) string {
	e := m.editor
	defaults := e.row
	defaults.InEffect = ""
	body := []string{fit("Reset "+e.row.Key+" to its default, "+m.settingValue(defaults)+"?", m.dialogWidth()), "",
		"This removes its line from the settings file."}
	if e.err != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(p.failed).Render(fit(e.err, m.dialogWidth())))
	}
	footer := hints(p, [][2]string{{"y", "reset"}, {"esc", "keep"}})
	if e.saving {
		footer = lipgloss.NewStyle().Foreground(p.muted).Render("Saving…")
	}
	return m.box(p, "Reset "+e.row.Key, p.waiting, body, footer)
}

// first returns the first value that is not empty.
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
