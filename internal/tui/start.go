package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// startTimeout bounds each read of profiles or models, and the start itself.
// It is the same bound as opening a session.
const startTimeout = 15 * time.Second

// The form's fields, top to bottom.
const (
	fieldAgent = iota
	fieldProfile
	fieldModel
	fieldEffort
	fieldCount
)

// loadState tracks one list in the form.
type loadState int

const (
	notLoaded loadState = iota
	loading
	loaded
)

// startProfilesMsg and startCatalogMsg carry a read's result. Each carries the
// sequence number of the request, so a reply the form no longer waits for is
// dropped.
type startProfilesMsg struct {
	agent    session.Provider
	seq      uint64
	profiles []provider.Profile
	err      error
}

type startCatalogMsg struct {
	key     string
	seq     uint64
	catalog provider.Catalog
	err     error
}

// startForm is the New session form (REQ-OPN-007 to REQ-OPN-012). It holds
// what the person chose and what has been read, and nothing is remembered
// once the form closes.
type startForm struct {
	agents    []provider.Adapter
	agent     int
	folder    string // the project's current folder
	profiles  []provider.Profile
	profile   int
	profState loadState
	profErr   string // why the profiles could not be read
	catalog   provider.Catalog
	catState  loadState
	catErr    string // why the models could not be read
	model     string // "" is Default
	// autoModel means the form chose model itself, because the profile has
	// no default; it is not the person's choice, so another profile drops it.
	autoModel bool
	effort    string // "" is Default
	field     int
	// wantProfiles and wantCatalog are the requests the lists wait for.
	wantProfiles, wantCatalog uint64
	profileCache              map[session.Provider]startProfilesMsg
	catalogCache              map[string]startCatalogMsg
	// err is why Enter refused the choice; it clears when a choice changes.
	err string
}

func (f *startForm) agentID() session.Provider { return f.agents[f.agent].ID() }

func (f *startForm) currentProfile() (provider.Profile, bool) {
	if f.profile < 0 || f.profile >= len(f.profiles) {
		return provider.Profile{}, false
	}
	return f.profiles[f.profile], true
}

// key identifies the current profile's catalogue in the cache.
func (f *startForm) key() string {
	p, _ := f.currentProfile()
	return string(f.agentID()) + "\x00" + p.ID
}

func (f *startForm) modelInfo() (provider.Model, bool) {
	for _, model := range f.catalog.Models {
		if model.ID == f.model {
			return model, true
		}
	}
	return provider.Model{}, false
}

func (f *startForm) modelOffered() bool {
	_, ok := f.modelInfo()
	return (f.model == "" && !f.catalog.ModelRequired) || ok
}

// efforts are the efforts the current model takes, or the agent's default
// model's when the model is Default.
func (f *startForm) efforts() []string {
	if f.model == "" {
		return f.catalog.DefaultEfforts
	}
	model, _ := f.modelInfo()
	return model.Efforts
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// modelValues are the choices ←→ moves through. A chosen model the catalogue
// no longer lists stays at the end, so it can be moved off like any value.
func (f *startForm) modelValues() []string {
	var values []string
	if !f.catalog.ModelRequired {
		values = append(values, "")
	}
	for _, model := range f.catalog.Models {
		values = append(values, model.ID)
	}
	if !f.modelOffered() && f.model != "" {
		values = append(values, f.model)
	}
	if len(values) == 0 {
		values = []string{""}
	}
	return values
}

// effortValues are the efforts ←→ moves through; none when the choice takes
// no effort or effort needs a model.
func (f *startForm) effortValues() []string {
	list := f.efforts()
	if len(list) == 0 {
		return nil
	}
	values := append([]string{""}, list...)
	if f.effort != "" && !contains(list, f.effort) {
		values = append(values, f.effort)
	}
	return values
}

// choiceProblem says why the chosen model and effort cannot start, naming the
// one to change, or "" when they can. It never picks a replacement.
func (f *startForm) choiceProblem() string {
	if f.model == "" && f.catalog.ModelRequired {
		return "Choose a model: this profile has no default model."
	}
	if !f.modelOffered() {
		return fmt.Sprintf("%s is not available here. Choose another model or Default.", f.model)
	}
	if err := provider.CheckChoice(f.catalog, f.model, f.effort); err != nil {
		if f.effort != "" && len(f.efforts()) > 0 && !contains(f.efforts(), f.effort) {
			return fmt.Sprintf("%s is not available here. Choose another effort or Default.", f.effort)
		}
		return err.Error()
	}
	return ""
}

// beginStart opens the form on the project the person is looking at: a row
// in the Projects view, or the project whose sessions are listed.
func (m *Model) beginStart() tea.Cmd {
	if m.busy != "" || m.opening != "" {
		return nil
	}
	if m.engine.Demo {
		m.notice = "New sessions can't be started in demo mode."
		return nil
	}
	folder, ok := m.startFolder()
	if !ok {
		m.notice = "Choose a project in the Projects view to start a new session."
		return nil
	}
	agents := m.engine.StartAgents()
	if len(agents) == 0 {
		m.notice = "None of the watched agents can start a session."
		return nil
	}
	m.notice = ""
	m.start = startForm{
		agents:       agents,
		folder:       folder,
		profileCache: map[session.Provider]startProfilesMsg{},
		catalogCache: map[string]startCatalogMsg{},
	}
	m.mode = startingNew
	return m.loadProfiles()
}

// startFolder is the current folder of the project the form starts in.
func (m *Model) startFolder() (string, bool) {
	if m.filter.project != "" {
		for _, s := range m.snapshot.Sessions {
			if s.ProjectID == m.filter.project {
				return s.Directory, true
			}
		}
		return "", false
	}
	if it, ok := m.selected(); ok && m.projects && it.project {
		return it.session.Directory, true
	}
	return "", false
}

// loadProfiles reads the agent's profiles, or takes them from the form's cache.
func (m *Model) loadProfiles() tea.Cmd {
	f := &m.start
	agent := f.agentID()
	f.profile = 0
	f.catState, f.catErr, f.catalog = notLoaded, "", provider.Catalog{}
	if cached, ok := f.profileCache[agent]; ok {
		f.applyProfileResult(cached)
		return m.loadCatalog()
	}
	m.startSeq++
	seq := m.startSeq
	f.wantProfiles = seq
	f.profState, f.profErr, f.profiles = loading, "", nil
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, startTimeout)
		defer cancel()
		profiles, err := m.engine.StartProfiles(ctx, agent)
		return startProfilesMsg{agent: agent, seq: seq, profiles: profiles, err: err}
	}
}

func (f *startForm) applyProfileResult(msg startProfilesMsg) {
	f.profiles = msg.profiles
	f.profState = loaded
	f.profErr = ""
	if msg.err != nil {
		f.profErr = "Could not read profiles: " + session.Text(msg.err.Error(), 200)
	}
	f.profile = min(f.profile, max(0, len(f.profiles)-1))
}

// loadCatalog reads the models and efforts of the chosen profile.
func (m *Model) loadCatalog() tea.Cmd {
	f := &m.start
	if f.autoModel {
		// The form picked that model for the previous profile; this one
		// starts again from its own choices.
		f.model, f.autoModel = "", false
	}
	p, ok := f.currentProfile()
	if !ok || p.Problem != "" {
		// Nothing to read: the profile is absent or cannot run, and Enter says why.
		f.catalog, f.catState, f.catErr = provider.Catalog{}, loaded, ""
		return nil
	}
	key := f.key()
	if cached, ok := f.catalogCache[key]; ok {
		f.applyCatalogResult(cached)
		return nil
	}
	m.startSeq++
	seq := m.startSeq
	f.wantCatalog = seq
	f.catState, f.catErr, f.catalog = loading, "", provider.Catalog{}
	agent := f.agentID()
	profile := p.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, startTimeout)
		defer cancel()
		catalog, err := m.engine.StartCatalog(ctx, agent, profile)
		return startCatalogMsg{key: key, seq: seq, catalog: catalog, err: err}
	}
}

func (f *startForm) applyCatalogResult(msg startCatalogMsg) {
	f.catalog = msg.catalog
	f.catState = loaded
	f.catErr = ""
	if msg.err != nil {
		f.catalog = provider.Catalog{}
		f.catErr = "Could not read models: " + session.Text(msg.err.Error(), 200)
	}
	// A profile with no default model starts on its first model rather than
	// on a Default it does not offer. Only Default is replaced: a model the
	// person chose stays, marked if this profile lacks it.
	if f.catalog.ModelRequired && f.model == "" && len(f.catalog.Models) > 0 {
		f.model, f.autoModel = f.catalog.Models[0].ID, true
	}
}

// applyStartProfiles takes a profile read that the form still waits for.
// Results are cached either way, since the form reuses them.
func (m *Model) applyStartProfiles(msg startProfilesMsg) tea.Cmd {
	if m.mode != startingNew {
		return nil
	}
	f := &m.start
	f.profileCache[msg.agent] = msg
	if msg.agent != f.agentID() || msg.seq != f.wantProfiles {
		return nil
	}
	f.applyProfileResult(msg)
	return m.loadCatalog()
}

func (m *Model) applyStartCatalog(msg startCatalogMsg) tea.Cmd {
	if m.mode != startingNew {
		return nil
	}
	f := &m.start
	f.catalogCache[msg.key] = msg
	if msg.key != f.key() || msg.seq != f.wantCatalog {
		return nil
	}
	f.applyCatalogResult(msg)
	return nil
}

// updateStart answers the form's keys. While the agent is starting, the form
// takes no input.
func (m *Model) updateStart(msg tea.KeyPressMsg) tea.Cmd {
	f := &m.start
	if m.opening != "" {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.mode = browsing
		m.start = startForm{}
	case "up", "k":
		f.field = max(0, f.field-1)
	case "down", "j":
		f.field = min(fieldCount-1, f.field+1)
	case "left", "h":
		return m.changeStart(-1)
	case "right", "l":
		return m.changeStart(1)
	case "enter":
		return m.startEnter()
	}
	return nil
}

// changeStart moves the focused field to its previous or next choice.
func (m *Model) changeStart(delta int) tea.Cmd {
	f := &m.start
	f.err = ""
	switch f.field {
	case fieldAgent:
		if len(f.agents) < 2 {
			return nil
		}
		f.agent = (f.agent + delta + len(f.agents)) % len(f.agents)
		return m.loadProfiles()
	case fieldProfile:
		if len(f.profiles) < 2 {
			return nil
		}
		f.profile = (f.profile + delta + len(f.profiles)) % len(f.profiles)
		return m.loadCatalog()
	case fieldModel:
		f.model = step(f.modelValues(), f.model, delta)
		f.autoModel = false
	case fieldEffort:
		if values := f.effortValues(); len(values) > 0 {
			f.effort = step(values, f.effort, delta)
		}
	}
	return nil
}

// step returns the value after (or before) current in values, wrapping.
func step(values []string, current string, delta int) string {
	index := 0
	for i, v := range values {
		if v == current {
			index = i
			break
		}
	}
	return values[(index+delta+len(values))%len(values)]
}

// startEnter starts the session when every choice is loaded and offered.
// Otherwise it says why and starts nothing.
func (m *Model) startEnter() tea.Cmd {
	f := &m.start
	if f.profState == loading || f.catState == loading {
		return nil
	}
	p, ok := f.currentProfile()
	if !ok {
		f.err = "This agent offers no profiles to start with."
		return nil
	}
	if p.Problem != "" {
		f.err = p.Problem
		return nil
	}
	// An unreadable catalogue leaves only Default to offer (REQ-OPN-008), and
	// Default needs no catalogue, so it still starts. A model or effort kept
	// from another profile cannot be checked, and is refused.
	if f.catErr != "" && (f.model != "" || f.effort != "") {
		f.err = f.catErr
		return nil
	}
	if problem := f.choiceProblem(); problem != "" {
		f.err = problem
		return nil
	}
	agent := f.agentID()
	folder := f.folder
	n := provider.NewSession{Directory: folder, Profile: p.ID, Model: f.model, Effort: f.effort}
	m.opening = m.title(agent)
	m.notice = "Starting " + m.opening + "…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, startTimeout)
		defer cancel()
		command, err := m.engine.Start(ctx, agent, n)
		return preparedMsg{command: command, err: err, starting: true}
	}
}

// startBox renders the form with the box the change dialogs use.
func (m *Model) startBox(p palette) string {
	f := &m.start
	muted := lipgloss.NewStyle().Foreground(p.muted)
	warn := lipgloss.NewStyle().Foreground(p.waiting)
	inner := m.dialogWidth()
	var body []string
	body = append(body, muted.Render(fit("Folder", 10))+tail(shortPath(f.folder), inner-10))
	body = append(body, "")
	agent := f.agents[f.agent]
	body = append(body, startRow(p, f, fieldAgent, "Agent", "‹ "+m.title(agent.ID())+" ›"))

	profile, hasProfile := f.currentProfile()
	switch {
	case f.profState == loading:
		body = append(body, startRow(p, f, fieldProfile, "Profile", muted.Render("loading…")))
	case !hasProfile:
		body = append(body, startRow(p, f, fieldProfile, "Profile", muted.Render("none offered")))
	default:
		value := "‹ " + session.Text(profile.Name, 64) + " ›"
		if profile.Detail != "" {
			value += "   " + muted.Render(tail(session.Text(profile.Detail, 200), inner-10-ansi.StringWidth(value)-3))
		}
		body = append(body, startRow(p, f, fieldProfile, "Profile", value))
	}
	if hasProfile && profile.Problem != "" {
		body = append(body, warn.Render(fit("", 10)+session.Text(profile.Problem, 200)))
	}
	if f.profErr != "" {
		body = append(body, warn.Render(fit("", 10)+session.Text(f.profErr, 200)))
	}

	switch {
	case f.catState == loading:
		body = append(body, startRow(p, f, fieldModel, "Model", muted.Render("loading…")))
	default:
		body = append(body, startRow(p, f, fieldModel, "Model", m.modelValue(p, f)))
	}
	if f.catErr != "" {
		body = append(body, warn.Render(fit("", 10)+session.Text(f.catErr, 200)))
	}
	body = append(body, startRow(p, f, fieldEffort, "Effort", m.effortValue(p, f)))

	body = append(body, "", muted.Render(session.Text(f.summary(m.title(agent.ID())), 200)))
	if f.err != "" {
		body = append(body, "", warn.Render(session.Text(f.err, 300)))
	}
	footer := hints(p, [][2]string{{"↑↓", "field"}, {"←→", "change"}, {"↵", "start"}, {"esc", "cancel"}})
	if m.opening != "" {
		footer = muted.Render("Starting…")
	}
	return m.box(p, "New session", p.accent, body, footer)
}

// startRow lays out one field: its label, and its value.
func startRow(p palette, f *startForm, field int, label, value string) string {
	marker, labelStyle := "  ", lipgloss.NewStyle().Foreground(p.muted)
	if f.field == field {
		marker, labelStyle = "› ", lipgloss.NewStyle().Bold(true).Foreground(p.accent)
	}
	return marker + labelStyle.Render(fit(label, 8)) + value
}

func (m *Model) modelValue(p palette, f *startForm) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	warn := lipgloss.NewStyle().Foreground(p.waiting)
	if f.model == "" && f.catalog.ModelRequired {
		return warn.Render("choose a model") + muted.Render(" · this profile has no default model")
	}
	if f.model == "" {
		return "‹ Default ›"
	}
	if !f.modelOffered() {
		return session.Text(f.model, 200) + " · " + warn.Render("not available here")
	}
	if model, ok := f.modelInfo(); ok && model.Name != "" && model.Name != model.ID {
		return "‹ " + session.Text(model.ID, 200) + " ›  " + muted.Render(session.Text(model.Name, 100))
	}
	return "‹ " + session.Text(f.model, 200) + " ›"
}

func (m *Model) effortValue(p palette, f *startForm) string {
	muted := lipgloss.NewStyle().Foreground(p.muted)
	warn := lipgloss.NewStyle().Foreground(p.waiting)
	if f.effort != "" && !contains(f.efforts(), f.effort) {
		return session.Text(f.effort, 64) + " · " + warn.Render("not available here")
	}
	if len(f.efforts()) == 0 {
		if f.model == "" {
			return "Default · " + muted.Render("choose a model to set effort")
		}
		return "Default · " + muted.Render("this model takes no effort setting")
	}
	if f.effort == "" {
		return "‹ Default ›"
	}
	return "‹ " + session.Text(f.effort, 64) + " ›"
}

// summary says what Enter will start, so the choices are never a guess.
func (f *startForm) summary(agent string) string {
	var b strings.Builder
	b.WriteString("Starts " + agent)
	if p, ok := f.currentProfile(); ok && p.ID != "" {
		b.WriteString(" (" + p.Name + ")")
	}
	if f.model == "" {
		b.WriteString(" with its default model")
	} else {
		b.WriteString(" with " + f.model)
	}
	if f.effort == "" {
		b.WriteString(" at the default effort.")
	} else {
		b.WriteString(" at " + f.effort + " effort.")
	}
	return b.String()
}

// startFailed says why a start did not happen, for the context line. The
// error says whether the folder is missing, so Update reads no files.
func startFailed(err error) string {
	message := "Could not start: " + session.Text(err.Error(), 300)
	if errors.Is(err, provider.ErrMissingFolder) {
		message += ". Press m to choose its new folder."
	}
	return message
}
