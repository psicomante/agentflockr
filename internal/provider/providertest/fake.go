// Package providertest provides a configurable adapter for engine and TUI tests.
package providertest

import (
	"context"
	"fmt"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Fake implements provider.Adapter and provider.Deleter. It is not a
// provider.Previewer; wrap it with WithPreview to add previews.
type Fake struct {
	Provider   session.Provider
	ScanFunc   func(context.Context, time.Time) provider.Result
	DeleteFunc func(context.Context, session.Session) error
}

func (f *Fake) Delete(ctx context.Context, s session.Session) error {
	if f.DeleteFunc == nil {
		return fmt.Errorf("fake deletion is not configured")
	}
	return f.DeleteFunc(ctx, s)
}

func (f *Fake) ID() session.Provider { return f.Provider }

func (f *Fake) Meta() provider.Meta {
	return provider.Meta{Name: string(f.Provider), Title: string(f.Provider), Short: "FK"}
}

func (f *Fake) Scan(ctx context.Context, now time.Time) provider.Result {
	if f.ScanFunc == nil {
		return provider.Result{Provider: f.Provider}
	}
	return f.ScanFunc(ctx, now)
}

// Resume validates like the real adapters and opens `true`, which exits at once.
func (f *Fake) Resume(_ context.Context, s session.Session) (provider.Command, error) {
	if err := provider.CheckResumable(s); err != nil {
		return provider.Command{}, err
	}
	return provider.Command{Name: "true", Directory: s.Directory}, nil
}

func (f *Fake) MatchesProcess([]string, session.Session) bool { return false }

type withPreview struct {
	*Fake
	preview func(context.Context, string) (string, error)
}

func (w withPreview) Preview(ctx context.Context, id string) (string, error) {
	return w.preview(ctx, id)
}

// WithPreview returns f as an adapter that also implements provider.Previewer.
func WithPreview(f *Fake, preview func(context.Context, string) (string, error)) provider.Adapter {
	return withPreview{Fake: f, preview: preview}
}
