package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// The scheduled prompt form's fields. Which of them a form shows depends on
// its target; see scheduleForm.order.
const (
	schedAgent = iota
	schedProfile
	schedPrompt
	schedEvery
	schedConnection
	schedModel
	schedEffort
)

// schedulesMsg carries the saved scheduled prompts, read after each scan.
type schedulesMsg struct {
	list []app.Schedule
	err  error
}

type connectionsMsg struct {
	seq  uint64
	list []provider.Connection
	err  error
}

type scheduleProfilesMsg struct {
	seq  uint64
	list []provider.Profile
	err  error
}

type scheduleCatalogMsg struct {
	seq     uint64
	catalog provider.Catalog
	err     error
}

// scheduleSavedMsg reports a save, pause, resume or delete. key is the
// schedule's; notice is what the context line says when it worked.
type scheduleSavedMsg struct {
	key    string
	notice string
	form   bool // the form sent it, and closes once it worked
	err    error
}

// scheduleForm is the Scheduled prompt form for one session or profile.
// Nothing in it is kept once it closes, except what Enter saved.
type scheduleForm struct {
	target   app.ScheduleTarget
	title    string // the session's title, or the agent's name
	existing *app.Schedule
	prompt   textinput.Model
	every    textinput.Model
	field    int // index into order()
	// agents are the agents that take prompts through a profile, and
	// profiles the chosen one's profiles; both are offered only for a new
	// profile schedule. agent and profile index them, and profSeq orders
	// the profile reads, so changing agent quickly ends on the last one's.
	agents       []session.Provider
	agent        int
	profiles     []provider.Profile
	profile      int
	profileState loadState
	profileErr   string
	profSeq      uint64
	// connections are the saved OpenCode connections; connection indexes
	// them, and -1 keeps whichever is active when the prompt runs.
	connections []provider.Connection
	connection  int
	connState   loadState
	connErr     string
	// catalog lists the models and efforts; model and effort are the chosen
	// IDs, "" for the default. A saved one no longer listed is kept and
	// marked, never replaced.
	catalog  provider.Catalog
	catState loadState
	catErr   string
	catSeq   uint64
	model    string
	effort   string
	seq      uint64
	saving   bool
	err      string
}

// order lists the fields the form shows, top to bottom.
func (f *scheduleForm) order() []int {
	var out []int
	if f.choosesProfile() {
		out = append(out, schedAgent, schedProfile)
	}
	out = append(out, schedPrompt, schedEvery)
	if f.target.Session != nil && f.target.Session.Provider == session.OpenCode {
		out = append(out, schedConnection)
	}
	return append(out, schedModel, schedEffort)
}

func (f *scheduleForm) current() int {
	order := f.order()
	return order[min(max(f.field, 0), len(order)-1)]
}

// choosesProfile reports whether the form picks its profile: only a new
// profile schedule does, since the profile is part of a saved one's key.
func (f *scheduleForm) choosesProfile() bool {
	return f.target.Session == nil && f.existing == nil
}

// loadSchedules reads the saved scheduled prompts for the Schedules view and
// the details pane.
func (m *Model) loadSchedules() tea.Cmd {
	engine := m.engine
	return func() tea.Msg {
		list, err := engine.Schedules()
		return schedulesMsg{list: list, err: err}
	}
}

func (m *Model) applySchedules(msg schedulesMsg) {
	if msg.err != nil {
		return
	}
	previous, had := m.selectedSchedule()
	m.schedules = map[string]app.Schedule{}
	for _, sc := range msg.list {
		m.schedules[sc.Key()] = sc
	}
	m.schedList = sortSchedules(msg.list)
	m.schedCursor = min(m.schedCursor, max(0, len(m.schedList)-1))
	if had {
		for i, sc := range m.schedList {
			if sc.Key() == previous.Key() {
				m.schedCursor = i
			}
		}
	}
	m.ensureScheduleVisible()
}

// beginSchedule opens the form on the selected session, filled with its
// saved schedule when it has one.
func (m *Model) beginSchedule() tea.Cmd {
	it, ok := m.selected()
	if !ok || m.busy != "" || m.opening != "" {
		return nil
	}
	if m.engine.Demo {
		m.notice = "Demo sessions can't have scheduled prompts."
		return nil
	}
	if it.project {
		m.notice = "Open the project and pick a session to schedule a prompt in."
		return nil
	}
	if !m.engine.CanSchedule(it.session) {
		m.notice = m.title(it.session.Provider) + " sessions can't have scheduled prompts."
		return nil
	}
	s := it.session
	return m.openScheduleForm(app.ScheduleTarget{Session: &s}, session.Text(s.Title, 200), true)
}

// beginProfileSchedule opens the form for a new schedule that runs through
// a profile of the first agent that takes one.
func (m *Model) beginProfileSchedule() tea.Cmd {
	if m.busy != "" || m.opening != "" {
		return nil
	}
	if m.engine.Demo {
		m.notice = "Scheduled prompts can't be saved in demo mode."
		return nil
	}
	agents := m.engine.ScheduleAgents()
	if len(agents) == 0 {
		m.notice = "No watched agent can run a scheduled prompt through a profile."
		return nil
	}
	return m.openScheduleForm(app.ScheduleTarget{Agent: agents[0]}, m.title(agents[0])+" profile", false)
}

// editSchedule opens the form on a saved schedule from the Schedules view.
// A session no longer listed is still named by its key, so its schedule can
// be changed or deleted.
func (m *Model) editSchedule(sc app.Schedule) tea.Cmd {
	if sc.IsProfile() {
		t := app.ScheduleTarget{Agent: session.Provider(sc.Agent), Profile: sc.Profile, ProfileName: sc.ProfileName}
		return m.openScheduleForm(t, m.title(t.Agent)+" profile", true)
	}
	s, ok := m.listedSession(sc.Session)
	if !ok {
		p, id, valid := session.SplitKey(sc.Session)
		if !valid {
			m.notice = "This scheduled prompt names no session it can be edited for; press d to delete it."
			return nil
		}
		s = session.Session{Provider: p, ID: id, Title: id}
	}
	return m.openScheduleForm(app.ScheduleTarget{Session: &s}, session.Text(s.Title, 200), true)
}

// openScheduleForm opens the form for t, filled from its saved schedule
// when saved is true, and reads what its choices need: profiles,
// connections and models. A new profile schedule passes false: its target
// is only the first agent's, and may match a saved schedule by chance.
func (m *Model) openScheduleForm(t app.ScheduleTarget, title string, saved bool) tea.Cmd {
	m.scheduleSeq++
	f := scheduleForm{target: t, title: title, connection: -1, seq: m.scheduleSeq}
	f.prompt = textinput.New()
	f.prompt.CharLimit = provider.MaxMessage
	f.prompt.Prompt = ""
	f.prompt.Placeholder = "the prompt to send"
	f.prompt.SetVirtualCursor(true)
	f.every = textinput.New()
	f.every.CharLimit = 16
	f.every.Prompt = ""
	f.every.Placeholder = "such as 5h or 90m"
	f.every.SetVirtualCursor(true)
	f.every.SetValue(shortDuration(app.DefaultScheduleEvery))
	if sc, ok := m.schedules[t.Key()]; ok && saved {
		f.existing = &sc
		f.prompt.SetValue(sc.Prompt)
		f.every.SetValue(shortDuration(sc.Every))
		f.model, f.effort = sc.Model, sc.Effort
	}
	width := max(10, m.dialogWidth()-14)
	f.prompt.SetWidth(width)
	f.every.SetWidth(width)
	m.schedule = f
	m.styleInput()
	m.mode = scheduling
	cmds := []tea.Cmd{m.focusSchedule(m.fieldIndex(schedPrompt))}
	engine, parent, seq := m.engine, m.ctx, f.seq
	if m.schedule.choosesProfile() {
		m.schedule.agents = m.engine.ScheduleAgents()
		cmds = append(cmds, m.loadScheduleProfiles())
	} else {
		cmds = append(cmds, m.loadScheduleCatalog())
	}
	if t.Session != nil && t.Session.Provider == session.OpenCode {
		m.schedule.connState = loading
		p := t.Session.Provider
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, startTimeout)
			defer cancel()
			list, err := engine.Connections(ctx, p)
			return connectionsMsg{seq: seq, list: list, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// loadScheduleProfiles reads the chosen agent's profiles.
func (m *Model) loadScheduleProfiles() tea.Cmd {
	f := &m.schedule
	f.profSeq++
	f.profileState, f.profileErr, f.profiles, f.profile = loading, "", nil, 0
	f.target.Profile, f.target.ProfileName = "", ""
	engine, parent, seq, agent := m.engine, m.ctx, f.seq<<32|f.profSeq, f.target.Agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, startTimeout)
		defer cancel()
		list, err := engine.ScheduleProfiles(ctx, agent)
		return scheduleProfilesMsg{seq: seq, list: list, err: err}
	}
}

// loadScheduleCatalog reads the models for the form's target as it stands.
// A newer read replaces an older one, so changing profile quickly ends on
// the last profile's models.
func (m *Model) loadScheduleCatalog() tea.Cmd {
	f := &m.schedule
	f.catSeq++
	f.catState, f.catErr = loading, ""
	engine, parent, seq, catSeq, t := m.engine, m.ctx, f.seq, f.catSeq, f.target
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, startTimeout)
		defer cancel()
		catalog, err := engine.ScheduleCatalog(ctx, t)
		return scheduleCatalogMsg{seq: seq<<32 | catSeq, catalog: catalog, err: err}
	}
}

func (m *Model) applyConnections(msg connectionsMsg) {
	f := &m.schedule
	if m.mode != scheduling || msg.seq != f.seq {
		return
	}
	f.connState, f.connections = loaded, msg.list
	if msg.err != nil {
		f.connErr = "Could not list the saved connections: " + session.Text(msg.err.Error(), 200)
	}
	if f.existing != nil && f.existing.Connection != "" {
		for i, c := range f.connections {
			if c.ID == f.existing.Connection {
				f.connection = i
			}
		}
		if f.connection < 0 {
			f.connErr = "The saved connection " + session.Text(f.existing.ConnectionName, 100) + " is no longer offered."
		}
	}
}

func (m *Model) applyScheduleProfiles(msg scheduleProfilesMsg) tea.Cmd {
	f := &m.schedule
	if m.mode != scheduling || msg.seq != f.seq<<32|f.profSeq {
		return nil
	}
	f.profileState, f.profiles = loaded, msg.list
	if msg.err != nil {
		f.profileErr = "Could not list the profiles: " + session.Text(msg.err.Error(), 200)
	}
	if len(f.profiles) == 0 {
		if f.profileErr == "" {
			f.profileErr = "This agent lists no profiles."
		}
		// No profile, so no models to wait for.
		f.catState = loaded
		return nil
	}
	f.profile = 0
	for i, p := range f.profiles {
		// Start on the first usable profile.
		if p.Problem == "" {
			f.profile = i
			break
		}
	}
	m.pickProfile()
	return m.loadScheduleCatalog()
}

// pickProfile points the target at the chosen profile.
func (m *Model) pickProfile() {
	f := &m.schedule
	if f.profile >= 0 && f.profile < len(f.profiles) {
		f.target.Profile, f.target.ProfileName = f.profiles[f.profile].ID, f.profiles[f.profile].Name
	}
}

func (m *Model) applyScheduleCatalog(msg scheduleCatalogMsg) {
	f := &m.schedule
	if m.mode != scheduling || msg.seq != f.seq<<32|f.catSeq {
		return
	}
	f.catState, f.catalog = loaded, msg.catalog
	if msg.err != nil {
		f.catErr = "Could not list the models, so only Default is offered: " + session.Text(msg.err.Error(), 200)
	}
	if f.existing == nil && !contains(f.modelOptions(), f.model) {
		// A new schedule's model belonged to the profile chosen before;
		// a saved one is kept and marked instead.
		f.model = ""
	}
	if f.catalog.ModelRequired && f.model == "" && len(f.catalog.Models) > 0 {
		// A profile with no default, such as an OpenCode connection, starts
		// on its first model, as the New session form does.
		f.model = f.catalog.Models[0].ID
	}
	if f.existing == nil && !contains(f.effortOptions(), f.effort) {
		f.effort = ""
	}
}

// updateSchedule answers the form's keys; typing goes to the focused text
// field.
func (m *Model) updateSchedule(msg tea.KeyPressMsg) tea.Cmd {
	f := &m.schedule
	if f.saving {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.mode = browsing
		m.schedule = scheduleForm{}
		return nil
	case "tab", "down":
		return m.focusSchedule(min(len(f.order())-1, f.field+1))
	case "shift+tab", "up":
		return m.focusSchedule(max(0, f.field-1))
	case "enter":
		return m.saveSchedule()
	case "ctrl+x":
		if f.existing == nil {
			return nil
		}
		f.saving = true
		engine, key := m.engine, f.existing.Key()
		return func() tea.Msg {
			return scheduleSavedMsg{key: key, form: true, notice: "Scheduled prompt deleted.", err: engine.DeleteSchedule(key)}
		}
	}
	switch f.current() {
	case schedAgent, schedProfile, schedConnection, schedModel, schedEffort:
		delta := 0
		switch msg.String() {
		case "left", "h":
			delta = -1
		case "right", "l":
			delta = 1
		}
		if delta != 0 {
			return m.stepSchedule(f.current(), delta)
		}
		return nil
	}
	f.err = ""
	var cmd tea.Cmd
	if f.current() == schedPrompt {
		f.prompt, cmd = f.prompt.Update(msg)
	} else {
		f.every, cmd = f.every.Update(msg)
	}
	return cmd
}

// stepSchedule moves a choice field one option along, wrapping around.
func (m *Model) stepSchedule(field, delta int) tea.Cmd {
	f := &m.schedule
	f.err = ""
	switch field {
	case schedAgent:
		if len(f.agents) < 2 {
			return nil
		}
		f.agent = (f.agent + delta + len(f.agents)) % len(f.agents)
		f.target.Agent = f.agents[f.agent]
		f.title = m.title(f.target.Agent) + " profile"
		// Models belong to the agent: start over on its defaults.
		f.model, f.effort, f.catalog, f.catErr = "", "", provider.Catalog{}, ""
		f.catState = loading
		return m.loadScheduleProfiles()
	case schedProfile:
		if len(f.profiles) == 0 {
			return nil
		}
		f.profile = (f.profile + delta + len(f.profiles)) % len(f.profiles)
		m.pickProfile()
		return m.loadScheduleCatalog()
	case schedConnection:
		n := len(f.connections) + 1
		f.connection = (f.connection+1+delta+n)%n - 1
	case schedModel:
		f.model = step(f.modelOptions(), f.model, delta)
		// An effort the new model does not take goes back to Default rather
		// than being sent and refused at the next run.
		if !contains(f.effortOptions(), f.effort) {
			f.effort = ""
		}
	case schedEffort:
		f.effort = step(f.effortOptions(), f.effort, delta)
	}
	return nil
}

// modelOptions are "" for Default, unless the profile has no default, then
// each listed model's ID.
func (f *scheduleForm) modelOptions() []string {
	out := []string{""}
	if f.catalog.ModelRequired && len(f.catalog.Models) > 0 {
		out = nil
	}
	for _, m := range f.catalog.Models {
		out = append(out, m.ID)
	}
	return out
}

// effortOptions are "" for Default, then the efforts the chosen model, or
// the agent's default model, takes.
func (f *scheduleForm) effortOptions() []string {
	out := []string{""}
	if f.model == "" {
		return append(out, f.catalog.DefaultEfforts...)
	}
	for _, m := range f.catalog.Models {
		if m.ID == f.model {
			return append(out, m.Efforts...)
		}
	}
	return out
}

func (f *scheduleForm) modelName(id string) (string, bool) {
	if id == "" {
		return "Default", true
	}
	for _, m := range f.catalog.Models {
		if m.ID == id {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			if m.Connection != "" {
				name += "  · " + m.Connection
			}
			return session.Text(name, 100), true
		}
	}
	return session.Text(id, 100), false
}

// fieldIndex is the position of field in the form's order, or 0.
func (m *Model) fieldIndex(field int) int {
	for i, f := range m.schedule.order() {
		if f == field {
			return i
		}
	}
	return 0
}

func (m *Model) focusSchedule(index int) tea.Cmd {
	f := &m.schedule
	f.field = index
	f.prompt.Blur()
	f.every.Blur()
	switch f.current() {
	case schedPrompt:
		return f.prompt.Focus()
	case schedEvery:
		return f.every.Focus()
	}
	return nil
}

// saveSchedule checks the form and saves it; the engine checks it again,
// the model and effort against the agent's catalogue included.
func (m *Model) saveSchedule() tea.Cmd {
	f := &m.schedule
	if f.connState == loading || f.profileState == loading || f.catState == loading {
		return nil
	}
	if f.choosesProfile() && f.profile >= len(f.profiles) {
		f.err = "Choose a profile to run the prompt through."
		return nil
	}
	if f.catalog.ModelRequired && f.model == "" {
		f.err = "Choose a model; this profile has no default."
		return nil
	}
	every, err := time.ParseDuration(strings.TrimSpace(f.every.Value()))
	if err != nil || every <= 0 {
		f.err = "Write the interval as hours and minutes, such as 5h or 4h30m."
		return nil
	}
	if _, ok := f.modelName(f.model); !ok {
		f.err = "The saved model is not available here; choose another or Default."
		return nil
	}
	if !contains(f.effortOptions(), f.effort) {
		f.err = "The saved effort is not available for this model; choose another or Default."
		return nil
	}
	in := app.ScheduleInput{Prompt: f.prompt.Value(), Every: every, Model: f.model, Effort: f.effort}
	if f.connection >= 0 && f.connection < len(f.connections) {
		in.Connection, in.ConnectionName = f.connections[f.connection].ID, f.connections[f.connection].Name
	}
	f.saving, f.err = true, ""
	notice := "Scheduled prompt saved; the first one is sent within a minute."
	if f.existing != nil {
		notice = "Scheduled prompt saved."
	}
	engine, parent, target := m.engine, m.ctx, f.target
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, startTimeout)
		defer cancel()
		return scheduleSavedMsg{key: target.Key(), form: true, notice: notice, err: engine.SetSchedule(ctx, target, in)}
	}
}

func (m *Model) finishSchedule(msg scheduleSavedMsg) tea.Cmd {
	if !msg.form {
		// A pause, resume or delete from the Schedules view.
		m.busy = ""
		m.notice = msg.notice
		if msg.err != nil {
			m.notice = "Could not change the scheduled prompt: " + session.Text(msg.err.Error(), 200)
		}
		return m.loadSchedules()
	}
	f := &m.schedule
	if m.mode != scheduling || f.target.Key() != msg.key {
		return nil
	}
	f.saving = false
	if msg.err != nil {
		f.err = session.Text(msg.err.Error(), 300)
		return nil
	}
	m.mode = browsing
	m.schedule = scheduleForm{}
	m.notice = msg.notice
	if !m.scheduled {
		// Saved from a session; the Schedules view lists it with the others.
		m.notice += " Press s to see every scheduled prompt."
	}
	return m.loadSchedules()
}

// scheduleBox renders the form with the box the other dialogs use.
func (m *Model) scheduleBox(p palette) string {
	f := &m.schedule
	muted := lipgloss.NewStyle().Foreground(p.muted)
	warn := lipgloss.NewStyle().Foreground(p.waiting)
	inner := m.dialogWidth()
	var body []string
	if s := f.target.Session; s != nil {
		body = []string{
			lipgloss.NewStyle().Bold(true).Render(fit(f.title, inner)),
			muted.Render(tail(m.title(s.Provider)+" · "+shortPath(s.Directory), inner)), "",
		}
	} else {
		body = []string{lipgloss.NewStyle().Bold(true).Render(fit(f.title, inner)), muted.Render(fit("Runs outside any session; nothing is kept.", inner)), ""}
	}
	row := func(field int, label, value string) string {
		marker, style := "  ", muted
		if f.current() == field {
			marker, style = "› ", lipgloss.NewStyle().Bold(true).Foreground(p.accent)
		}
		return marker + style.Render(fit(label, 12)) + value
	}
	note := func(text string) {
		if text != "" {
			body = append(body, warn.Render(fit("", 14)+session.Text(text, 200)))
		}
	}
	for _, field := range f.order() {
		switch field {
		case schedAgent:
			value := "‹ " + m.title(f.target.Agent) + " ›"
			if len(f.agents) < 2 {
				value = m.title(f.target.Agent)
			}
			body = append(body, row(schedAgent, "Agent", value))
		case schedProfile:
			value := muted.Render("loading…")
			if f.profileState == loaded && f.profile < len(f.profiles) {
				pr := f.profiles[f.profile]
				value = "‹ " + session.Text(pr.Name, 60) + " ›  " + muted.Render(session.Text(pr.Detail, 120))
				if pr.Problem != "" {
					note("Cannot run: " + pr.Problem)
				}
			}
			body = append(body, row(schedProfile, "Profile", value))
			note(f.profileErr)
		case schedPrompt:
			body = append(body, row(schedPrompt, "Prompt", f.prompt.View()))
		case schedEvery:
			body = append(body, row(schedEvery, "Every", f.every.View()))
		case schedConnection:
			value := "‹ Keep current ›"
			switch {
			case f.connState == loading:
				value = muted.Render("loading…")
			case f.connection >= 0 && f.connection < len(f.connections):
				c := f.connections[f.connection]
				value = "‹ " + session.Text(c.Name, 100) + " ›  " + muted.Render(session.Text(c.Integration, 60))
			}
			body = append(body, row(schedConnection, "Connection", value))
			note(f.connErr)
		case schedModel:
			value := muted.Render("loading…")
			if f.catState != loading {
				name, ok := f.modelName(f.model)
				value = "‹ " + name + " ›"
				if !ok {
					value += warn.Render("  not available here")
				}
			}
			body = append(body, row(schedModel, "Model", value))
			note(f.catErr)
		case schedEffort:
			value := "‹ Default ›"
			if f.effort != "" {
				value = "‹ " + session.Text(f.effort, 40) + " ›"
				if f.catState != loading && !contains(f.effortOptions(), f.effort) {
					value += warn.Render("  not available here")
				}
			}
			if f.catState != loading && len(f.effortOptions()) == 1 && f.effort == "" {
				why := "this model takes no effort setting"
				if f.model == "" {
					why = "choose a model to set effort"
				}
				value += muted.Render("  " + why)
			}
			body = append(body, row(schedEffort, "Effort", value))
		}
	}
	explain := "Sends this prompt now, then once every interval, while the dashboard or agentflockr serve runs. A session in the middle of a turn waits until it ends. The model and effort apply to these runs only."
	switch {
	case f.target.Session == nil:
		explain = "Sends this prompt through the profile now, then once every interval, while the dashboard or agentflockr serve runs, to keep the profile's usage window going. Each run is a short conversation outside your sessions, which the agent does not keep."
		if f.target.Agent == session.OpenCode {
			explain += " OpenCode cannot run a prompt it does not keep, so each run's session is deleted once its turn ends."
		}
	case f.target.Session.Provider == session.OpenCode:
		explain += " A chosen connection or model is set only for the run; the ones before come back after it."
	}
	body = append(body, "", muted.Render(explain))
	if f.existing != nil {
		body = append(body, "", muted.Render(scheduleStatus(*f.existing, m.ago)))
	}
	if f.err != "" {
		body = append(body, "", warn.Render(f.err))
	}
	keys := [][2]string{{"tab", "field"}, {"←→", "choose"}, {"↵", "save"}}
	if f.existing != nil {
		keys = append(keys, [2]string{"ctrl+x", "delete"})
	}
	footer := hints(p, append(keys, [2]string{"esc", "cancel"}))
	if f.saving {
		footer = muted.Render("Saving…")
	}
	title := "Scheduled prompt"
	if f.target.Session == nil {
		title = "Scheduled prompt on a profile"
	}
	return m.box(p, title, p.accent, body, footer)
}

// scheduleStatus says when a schedule runs next and how its last run went.
func scheduleStatus(sc app.Schedule, age func(time.Time) string) string {
	text := "Every " + shortDuration(sc.Every)
	if sc.Paused {
		text += " · paused"
	} else {
		text += " · next " + sc.Next.Local().Format("Jan 2 15:04")
	}
	if choice := scheduleChoice(sc); choice != "" {
		text += " · " + choice
	}
	if sc.ConnectionName != "" {
		text += " · on " + sc.ConnectionName
	}
	switch {
	case sc.Last.IsZero():
		text += " · not sent yet"
	case sc.LastError != "":
		text += " · last run failed " + age(sc.Last) + ": " + sc.LastError
	default:
		text += " · last sent " + age(sc.Last)
	}
	return session.Text(text, 400)
}

// scheduleChoice names the model and effort, or "" for the defaults.
func scheduleChoice(sc app.Schedule) string {
	switch {
	case sc.Model != "" && sc.Effort != "":
		return sc.Model + " · " + sc.Effort
	case sc.Model != "":
		return sc.Model
	case sc.Effort != "":
		return "default model · " + sc.Effort
	}
	return ""
}

// shortDuration writes 5h0m0s as 5h and 4h30m0s as 4h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	if s == "" {
		return "0m"
	}
	return s
}
