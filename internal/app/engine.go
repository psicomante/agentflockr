// Package app combines the provider scans with the local index. The TUI reads
// sessions only through an Engine.
package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/psicomante/agentflockr/internal/handoff"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

type Snapshot struct {
	Sessions  []session.Session `json:"sessions"`
	Warnings  []string          `json:"warnings,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	// Generation counts the index changes the snapshot already reflects. A
	// snapshot older than a completed change may still show deleted, removed
	// or unmoved sessions, and must not replace newer state.
	Generation uint64 `json:"-"`
}

// Engine orders every refresh's index work with every change to the index,
// so a snapshot's generation reliably orders it relative to deletions,
// removals and moves. Reading the agents happens outside that order, so a
// change never waits for a slow agent; see Scan.
type Engine struct {
	// mu orders index changes and the index part of each refresh.
	mu         sync.Mutex
	generation uint64
	// scanning keeps refreshes to one at a time. Each adapter's reader keeps
	// an unsynchronized cache between scans, so it must be scanned by one
	// goroutine at a time; adapters' other methods do not touch that cache.
	scanning  sync.Mutex
	Store     *store.Store
	Providers provider.Registry
	// Demo serves built-in sample sessions and never touches providers or
	// the index.
	Demo bool
	// Run executes tmux and ps when looking for a session's pane; nil runs
	// them for real. Tests substitute it.
	Run provider.Runner
}

// Open returns the command that brings a native session to the foreground:
// its tmux pane when one is already running it, otherwise the agent's own
// resume. It reads no index state, so it does not wait for a running scan.
func (e *Engine) Open(ctx context.Context, s session.Session) (provider.Command, error) {
	if e.Demo {
		return provider.Command{}, fmt.Errorf("demo sessions cannot be opened")
	}
	adapter := e.Providers.Get(s.Provider)
	if adapter == nil {
		return provider.Command{}, fmt.Errorf("%s is not among the agents being watched", s.Provider)
	}
	// A moved session must be resumed so its agent adopts the new folder;
	// attaching to a process still running in the old one would not.
	if !session.Moved(s) {
		// An agent that knows the session's process finds it however it was
		// started; the arguments remain the fallback.
		known := 0
		if finder, ok := adapter.(provider.ProcessFinder); ok {
			known, _ = finder.SessionProcess(s)
		}
		matches := func(pid int, args []string) bool {
			return (known > 0 && pid == known) || adapter.MatchesProcess(args, s)
		}
		command, ok, err := handoff.Tmux(ctx, matches, e.Run)
		if err != nil {
			// The session is running in a pane we could not switch to;
			// resuming it would start a second agent on it.
			return provider.Command{}, err
		}
		if ok {
			return command, nil
		}
	}
	return adapter.Resume(ctx, s)
}

// Generation reports the number of completed index changes.
func (e *Engine) Generation() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.generation
}

// Delete asks the session's agent to delete its history permanently, then
// forgets the session. The index changes only after the agent succeeded.
// It returns the generation after the change, even with an error, so the
// dashboard need not take the lock again to learn it (see change).
func (e *Engine) Delete(ctx context.Context, key string) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Demo {
		return e.generation, fmt.Errorf("demo sessions cannot be deleted")
	}
	if err := ctx.Err(); err != nil {
		return e.generation, err
	}
	items, err := e.Store.Load()
	if err != nil {
		return e.generation, err
	}
	for _, s := range items {
		if s.Key() != key {
			continue
		}
		deleter, ok := e.Providers.Get(s.Provider).(provider.Deleter)
		if !ok {
			return e.generation, fmt.Errorf("%s sessions cannot be deleted from agentflockr", s.Provider)
		}
		if err := deleter.Delete(ctx, s); err != nil {
			return e.generation, err
		}
		// The agent's copy is gone even if forgetting it fails, so any
		// earlier snapshot is stale either way.
		e.generation++
		if err := e.Store.DeleteSession(key); err != nil {
			return e.generation, fmt.Errorf("the agent deleted the session, but agentflockr could not forget it: %w", err)
		}
		return e.generation, nil
	}
	return e.generation, fmt.Errorf("the session is no longer in the index")
}

// RemoveSession and RemoveProject only change agentflockr's index; the
// agents' own history is left alone.
func (e *Engine) RemoveSession(key string) (uint64, error) {
	return e.change("removal", func() error { return e.Store.RemoveSession(key) })
}

func (e *Engine) RemoveProject(id string) (uint64, error) {
	return e.change("removal", func() error { return e.Store.RemoveProject(id) })
}

// Reconnect moves a project to a new folder in the index. Its sessions adopt
// the folder when they are next opened.
func (e *Engine) Reconnect(id, path string) (uint64, error) {
	return e.change("moving", func() error { return e.Store.Reconnect(id, path) })
}

// change applies one index change under the lock and returns the generation
// it produced. Returning it here, rather than through Generation, matters: a
// second lock could wait behind a whole refresh before the dashboard learns
// that the change is done.
func (e *Engine) change(action string, apply func() error) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Demo {
		return e.generation, fmt.Errorf("%s is not available in demo mode", action)
	}
	if err := apply(); err != nil {
		return e.generation, err
	}
	e.generation++
	return e.generation, nil
}

// View returns the saved dashboard view, or "" when none is saved or nothing
// is persisted (demo mode).
func (e *Engine) View() (string, error) {
	if e.Demo || e.Store == nil {
		return "", nil
	}
	return e.Store.View()
}

func (e *Engine) SetView(view string) error {
	if e.Demo || e.Store == nil {
		return nil
	}
	return e.Store.SetView(view)
}

// CanPreview reports whether the session's provider loads activity on demand.
func (e *Engine) CanPreview(s session.Session) bool {
	_, ok := e.Providers.Get(s.Provider).(provider.Previewer)
	return !e.Demo && ok
}

func (e *Engine) Preview(ctx context.Context, s session.Session) (string, error) {
	p, ok := e.Providers.Get(s.Provider).(provider.Previewer)
	if e.Demo || !ok {
		return "", fmt.Errorf("preview is unavailable for %s", s.Provider)
	}
	// The ID becomes part of an agent request, such as an API path.
	if !provider.ValidID(s.ID) {
		return "", fmt.Errorf("invalid session ID")
	}
	return p.Preview(ctx, s.ID)
}

// Cached returns the indexed history without scanning, for the first frame.
func (e *Engine) Cached() (Snapshot, error) {
	if e.Demo {
		return Demo(), nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	items, warnings, err := e.Store.LoadWithWarnings()
	checkFolders(items)
	Sort(items)
	for i := range warnings {
		warnings[i] = "Index: " + warnings[i]
	}
	return Snapshot{Sessions: items, Generation: e.generation, Warnings: warnings}, err
}

// Scan runs every provider in parallel, records what they found, and returns
// the whole index with live status for the sessions seen in this scan.
//
// The agents are read without the engine lock: a read can take up to 30
// seconds, and a remove, delete or move must not wait for it. A change made
// while the agents are read still wins, because the index checks every write
// against removals, deletions and moved folders. The lock is taken only to
// write what was found and load the index, so the snapshot's generation
// counts every change it reflects.
func (e *Engine) Scan(ctx context.Context) Snapshot {
	if e.Demo {
		return Demo()
	}
	e.scanning.Lock()
	defer e.scanning.Unlock()
	now := time.Now()
	found := make(chan provider.Result, len(e.Providers))
	for _, source := range e.Providers {
		go func(source provider.Adapter) {
			child, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			found <- source.Scan(child, now)
		}(source)
	}
	results := make([]provider.Result, 0, len(e.Providers))
	for range e.Providers {
		results = append(results, <-found)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	snap := Snapshot{UpdatedAt: now, Generation: e.generation}
	fresh := map[string]session.Session{}
	for _, r := range results {
		if r.Err != nil {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %v", r.Provider, r.Err))
		}
		for _, w := range r.Warnings {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s: %s", r.Provider, w))
		}
		// Preserve even a partial scan's valid history. Unseen cached entries stay
		// unknown, so a source failure never turns into fabricated live status.
		if err := e.Store.Upsert(r.Sessions); err != nil {
			snap.Warnings = append(snap.Warnings, "Index: "+err.Error())
			continue
		}
		for _, s := range r.Sessions {
			fresh[s.Key()] = s
		}
	}
	items, skipped, err := e.Store.LoadWithWarnings()
	for _, w := range skipped {
		snap.Warnings = append(snap.Warnings, "Index: "+w)
	}
	if err != nil {
		snap.Warnings = append(snap.Warnings, "Index: "+err.Error())
	}
	for i := range items {
		if s, ok := fresh[items[i].Key()]; ok {
			items[i].Status = s.Status
			items[i].Evidence = s.Evidence
			items[i].Cached = false
		}
	}
	Sort(items)
	checkFolders(items)
	snap.Sessions = items
	return snap
}

func checkFolders(items []session.Session) {
	missing := map[string]bool{}
	for i := range items {
		path := items[i].Directory
		value, ok := missing[path]
		if !ok {
			info, err := os.Stat(path)
			value = err != nil || !info.IsDir()
			missing[path] = value
		}
		items[i].Missing = value
	}
}

// Sort orders sessions by attention: status priority, then most recent first.
func Sort(items []session.Session) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if session.Priority(a.Status) != session.Priority(b.Status) {
			return session.Priority(a.Status) < session.Priority(b.Status)
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.Key() < b.Key()
	})
}
