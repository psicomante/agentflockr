package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

// Bounds on scheduled prompts (see docs/notes/hardcoded-rules.md).
const (
	// DefaultScheduleEvery is the interval the form proposes: the length of
	// an agent subscription's usage window.
	DefaultScheduleEvery = 5 * time.Hour
	// MinScheduleEvery keeps a schedule from sending prompts in a tight loop.
	MinScheduleEvery = 15 * time.Minute
	// scheduleCheck is how often due schedules are looked for.
	scheduleCheck = time.Minute
)

// Schedule is a saved scheduled prompt, as the dashboard reads it.
type Schedule = store.Schedule

// ErrNoSchedules means the session's agent cannot take scheduled prompts.
var ErrNoSchedules = errors.New("this agent cannot take scheduled prompts")

// schedules keeps one run per session at a time within this process. Runs in
// other processes are kept apart by store.ClaimSchedule.
type schedules struct {
	mu      sync.Mutex
	running map[string]bool
}

// CanSchedule reports whether the session's agent takes scheduled prompts.
func (e *Engine) CanSchedule(s session.Session) bool {
	_, ok := e.Providers.Get(s.Provider).(provider.Prompter)
	return !e.Demo && e.Store != nil && ok
}

// ScheduleAgents are the watched agents that take scheduled prompts through
// a profile, outside any session.
func (e *Engine) ScheduleAgents() []session.Provider {
	if e.Demo || e.Store == nil {
		return nil
	}
	var out []session.Provider
	for _, a := range e.Providers {
		if _, ok := a.(provider.ProfilePrompter); ok {
			out = append(out, a.ID())
		}
	}
	return out
}

// Schedules lists the saved scheduled prompts. Demo mode has none.
func (e *Engine) Schedules() ([]Schedule, error) {
	if e.Demo || e.Store == nil {
		return nil, nil
	}
	list, _, err := e.Store.Schedules()
	return list, err
}

// Connections lists the saved connections a scheduled prompt for agent p may
// switch to, or none when the agent has no such choice.
func (e *Engine) Connections(ctx context.Context, p session.Provider) ([]provider.Connection, error) {
	lister, ok := e.Providers.Get(p).(provider.ConnectionLister)
	if e.Demo || !ok {
		return nil, nil
	}
	return lister.Connections(ctx)
}

// ScheduleTarget is where a scheduled prompt goes: into Session when it is
// set, else through Agent's Profile, a StartProfiles ID.
type ScheduleTarget struct {
	Session     *session.Session
	Agent       session.Provider
	Profile     string
	ProfileName string // only shown back to the person
}

// Key is the key the target's schedule is saved under.
func (t ScheduleTarget) Key() string {
	if t.Session != nil {
		return t.Session.Key()
	}
	return store.Schedule{Agent: string(t.Agent), Profile: t.Profile}.Key()
}

// ScheduleInput is what the person chose in the form. ConnectionName is
// only shown back to them.
type ScheduleInput struct {
	Prompt                     string
	Every                      time.Duration
	Connection, ConnectionName string
	Model, Effort              string
}

// ScheduleCatalog lists the models and efforts a scheduled prompt to t may
// choose. An agent that lists none offers only its default.
func (e *Engine) ScheduleCatalog(ctx context.Context, t ScheduleTarget) (provider.Catalog, error) {
	if e.Demo {
		return provider.Catalog{}, nil
	}
	if t.Session != nil {
		catalog, ok := e.Providers.Get(t.Session.Provider).(provider.PromptCatalog)
		if !ok {
			return provider.Catalog{}, nil
		}
		return catalog.PromptCatalog(ctx, *t.Session)
	}
	p, ok := e.Providers.Get(t.Agent).(provider.ProfilePrompter)
	if !ok {
		return provider.Catalog{}, ErrNoSchedules
	}
	return p.StartCatalog(ctx, t.Profile)
}

// ScheduleProfiles lists agent's profiles that a scheduled prompt can run
// through.
func (e *Engine) ScheduleProfiles(ctx context.Context, agent session.Provider) ([]provider.Profile, error) {
	p, ok := e.Providers.Get(agent).(provider.ProfilePrompter)
	if e.Demo || e.Store == nil || !ok {
		return nil, ErrNoSchedules
	}
	return p.StartProfiles(ctx)
}

// SetSchedule saves a scheduled prompt for t, checking the model and effort
// against the agent's catalogue. A new schedule's first run is due at once.
// Saving over an existing one keeps its last run and whether it is paused;
// its next run is one new interval after the last, or at once when that has
// passed or it never ran.
func (e *Engine) SetSchedule(ctx context.Context, t ScheduleTarget, in ScheduleInput) error {
	if e.Demo || e.Store == nil {
		return ErrNoSchedules
	}
	sc := store.Schedule{Every: in.Every}
	switch {
	case t.Session != nil:
		if !e.CanSchedule(*t.Session) {
			return ErrNoSchedules
		}
		sc.Session = t.Session.Key()
	default:
		if _, ok := e.Providers.Get(t.Agent).(provider.ProfilePrompter); !ok {
			return ErrNoSchedules
		}
		// The ID is only compared with the listed profiles, never used as a
		// path or an argument, so any listed one is accepted.
		profiles, err := e.ScheduleProfiles(ctx, t.Agent)
		if err != nil {
			return err
		}
		found := false
		for _, p := range profiles {
			if p.ID == t.Profile {
				found = true
				if p.Problem != "" {
					return fmt.Errorf("%w: the profile cannot run: %s", provider.ErrInvalid, p.Problem)
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: that profile is no longer offered", provider.ErrInvalid)
		}
		sc.Agent, sc.Profile, sc.ProfileName = string(t.Agent), t.Profile, session.Text(t.ProfileName, 100)
	}
	text, err := provider.CleanPrompt(in.Prompt)
	if err != nil {
		return err
	}
	sc.Prompt = text
	if in.Every < MinScheduleEvery {
		return fmt.Errorf("%w: run it at most every %s", provider.ErrInvalid, MinScheduleEvery)
	}
	if in.Connection != "" && (t.Session == nil || t.Session.Provider != session.OpenCode || !provider.ValidArgument(in.Connection)) {
		return fmt.Errorf("%w: only OpenCode sessions switch connection", provider.ErrInvalid)
	}
	if in.Connection != "" {
		sc.Connection, sc.ConnectionName = in.Connection, session.Text(in.ConnectionName, 200)
	}
	if in.Model != "" || in.Effort != "" {
		if (in.Model != "" && !provider.ValidArgument(in.Model)) || (in.Effort != "" && !provider.ValidArgument(in.Effort)) {
			return fmt.Errorf("%w: invalid model or effort", provider.ErrInvalid)
		}
		catalog, err := e.ScheduleCatalog(ctx, t)
		if err != nil {
			return fmt.Errorf("could not check the model: %w", err)
		}
		if err := provider.CheckChoice(catalog, in.Model, in.Effort); err != nil {
			return fmt.Errorf("%w: %v", provider.ErrInvalid, err)
		}
		sc.Model, sc.Effort = in.Model, in.Effort
	}
	now := e.now()
	sc.Next = now
	old, ok, err := e.Store.Schedule(sc.Key())
	if err != nil {
		return err
	}
	if ok {
		sc.Last, sc.LastError, sc.Paused = old.Last, old.LastError, old.Paused
		if next := old.Last.Add(sc.Every); !old.Last.IsZero() && next.After(now) {
			sc.Next = next
		}
	}
	return e.Store.SetSchedule(sc)
}

// PauseSchedule pauses or resumes a scheduled prompt. A run in progress
// finishes; a resumed one whose next run has passed is sent at the next
// check.
func (e *Engine) PauseSchedule(key string, paused bool) error {
	if e.Demo || e.Store == nil {
		return nil
	}
	found, err := e.Store.PauseSchedule(key, paused)
	if err == nil && !found {
		err = errors.New("that scheduled prompt no longer exists")
	}
	return err
}

// DeleteSchedule forgets a scheduled prompt. A run in progress finishes.
func (e *Engine) DeleteSchedule(key string) error {
	if e.Demo || e.Store == nil {
		return nil
	}
	return e.Store.DeleteSchedule(key)
}

// RunSchedules sends due scheduled prompts until ctx ends. Both the dashboard
// and serve run it on the same index; each run is claimed there first, so it
// is sent once. A session with a running turn is skipped until it is idle,
// so a prompt never lands in the middle of the person's own work, and so an
// OpenCode connection is never switched under a turn.
func (e *Engine) RunSchedules(ctx context.Context) {
	if e.Demo || e.Store == nil {
		return
	}
	var runs sync.WaitGroup
	defer runs.Wait()
	ticker := time.NewTicker(scheduleCheck)
	defer ticker.Stop()
	for {
		e.dueSchedules(ctx, &runs)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// dueSchedules starts the runs that are due now.
func (e *Engine) dueSchedules(ctx context.Context, runs *sync.WaitGroup) {
	list, _, err := e.Store.Schedules()
	if err != nil || len(list) == 0 {
		return
	}
	now := e.now()
	e.mu.Lock()
	seen := e.seen
	e.mu.Unlock()
	if seen == nil {
		// No scan yet: a running turn could not be told from an idle one.
		return
	}
	var sessions map[string]session.Session
	for _, sc := range list {
		key := sc.Key()
		if sc.Paused || sc.Next.After(now) || e.scheduleRunning(key) {
			continue
		}
		var s session.Session
		listed := false
		if !sc.IsProfile() {
			if sessions == nil {
				if sessions, err = e.indexed(); err != nil {
					// Without the index no session can be told apart from a gone
					// one; every due run waits for the next check.
					return
				}
			}
			s, listed = sessions[key]
			if seen[key] == session.Working {
				continue
			}
		}
		claimed, ok, err := e.Store.ClaimSchedule(key, now)
		if err != nil || !ok {
			continue
		}
		if !claimed.IsProfile() && !listed {
			// Kept rather than deleted: the session may be listed again once its
			// agent is scanned, and the person sees why runs are failing.
			e.Store.FinishSchedule(key, claimed.Prompt, now, errors.New("the session is no longer in the index"))
			continue
		}
		e.markSchedule(key, true)
		runs.Go(func() {
			defer e.markSchedule(key, false)
			err := e.runSchedule(ctx, s, claimed)
			// Recorded even when the dashboard is closing, so the next start
			// shows how the run ended.
			e.Store.FinishSchedule(key, claimed.Prompt, now, err)
		})
	}
}

// runSchedule sends one run: into s, or through the schedule's profile.
func (e *Engine) runSchedule(ctx context.Context, s session.Session, sc store.Schedule) error {
	prompt := provider.ScheduledPrompt{Text: sc.Prompt, Connection: sc.Connection, Model: sc.Model, Effort: sc.Effort}
	if sc.IsProfile() {
		p, ok := e.Providers.Get(session.Provider(sc.Agent)).(provider.ProfilePrompter)
		if !ok {
			return ErrNoSchedules
		}
		dir, err := e.scheduleDir()
		if err != nil {
			return err
		}
		return p.PromptProfile(ctx, sc.Profile, dir, prompt)
	}
	prompter, ok := e.Providers.Get(s.Provider).(provider.Prompter)
	if !ok {
		return ErrNoSchedules
	}
	return prompter.Prompt(ctx, s, prompt)
}

// scheduleDir makes sure the folder profile runs happen in exists. It is
// agentflockr's own, kept empty, so a run reads no project's instructions.
func (e *Engine) scheduleDir() (string, error) {
	if e.ScheduleDir == "" {
		return "", errors.New("no folder is set for runs through a profile")
	}
	if err := os.MkdirAll(e.ScheduleDir, 0o700); err != nil {
		return "", fmt.Errorf("could not create the folder for runs through a profile: %w", err)
	}
	return e.ScheduleDir, nil
}

// indexed returns the index's sessions by key.
func (e *Engine) indexed() (map[string]session.Session, error) {
	items, err := e.Store.Load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]session.Session, len(items))
	for _, s := range items {
		out[s.Key()] = s
	}
	return out, nil
}

func (e *Engine) scheduleRunning(key string) bool {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	return e.sched.running[key]
}

func (e *Engine) markSchedule(key string, running bool) {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if e.sched.running == nil {
		e.sched.running = map[string]bool{}
	}
	if running {
		e.sched.running[key] = true
	} else {
		delete(e.sched.running, key)
	}
}
