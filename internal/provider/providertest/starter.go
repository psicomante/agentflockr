package providertest

import (
	"context"
	"fmt"
	"sync"

	"github.com/psicomante/agentflockr/internal/provider"
)

// Starter scripts a fake agent's new-session support and records every call.
// The dashboard runs these calls as commands, so the record is guarded.
type Starter struct {
	Profiles    []provider.Profile
	ProfilesErr error
	// Catalogs is keyed by profile ID; an unknown profile is an error.
	Catalogs   map[string]provider.Catalog
	CatalogErr error
	StartErr   error

	mu      sync.Mutex
	calls   []string
	started []provider.NewSession
}

// Calls lists the calls made so far, such as "profiles" or "catalog:work".
func (s *Starter) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Started lists the sessions the dashboard asked to start.
func (s *Starter) Started() []provider.NewSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.NewSession(nil), s.started...)
}

func (s *Starter) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

type withStarter struct {
	*Fake
	starter *Starter
}

func (w withStarter) StartProfiles(context.Context) ([]provider.Profile, error) {
	w.starter.record("profiles")
	if w.starter.ProfilesErr != nil {
		return nil, w.starter.ProfilesErr
	}
	return w.starter.Profiles, nil
}

func (w withStarter) StartCatalog(_ context.Context, profile string) (provider.Catalog, error) {
	w.starter.record("catalog:" + profile)
	if w.starter.CatalogErr != nil {
		return provider.Catalog{}, w.starter.CatalogErr
	}
	c, ok := w.starter.Catalogs[profile]
	if !ok {
		return provider.Catalog{}, fmt.Errorf("unknown profile %q", profile)
	}
	return c, nil
}

// Start records the request and returns `true`, which exits at once.
func (w withStarter) Start(_ context.Context, n provider.NewSession) (provider.Command, error) {
	w.starter.record("start")
	if w.starter.StartErr != nil {
		return provider.Command{}, w.starter.StartErr
	}
	w.starter.mu.Lock()
	w.starter.started = append(w.starter.started, n)
	w.starter.mu.Unlock()
	return provider.Command{Name: "true", Directory: n.Directory}, nil
}

// WithStarter returns f as an adapter that also implements provider.Starter,
// answering from s.
func WithStarter(f *Fake, s *Starter) provider.Adapter {
	return withStarter{Fake: f, starter: s}
}

// Start starts n the way the engine does: provider.CheckStart, then the
// adapter's Start. Adapter tests use it so their refusals are the real ones.
func Start(ctx context.Context, s provider.Starter, n provider.NewSession) (provider.Command, error) {
	if err := provider.CheckStart(ctx, s, n); err != nil {
		return provider.Command{}, err
	}
	return s.Start(ctx, n)
}
