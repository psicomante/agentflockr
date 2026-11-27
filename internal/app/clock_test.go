package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
)

// TestScanUsesTheEngineClock checks that a refresh reads the engine's clock:
// the time the agents are given and the snapshot's UpdatedAt are both it.
func TestScanUsesTheEngineClock(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	var given time.Time
	engine := Engine{Store: db, Now: func() time.Time { return at }, Providers: scanning(func(_ context.Context, now time.Time) provider.Result {
		given = now
		return provider.Result{Provider: session.Codex}
	})}
	snap := engine.Scan(context.Background())
	if !given.Equal(at) || !snap.UpdatedAt.Equal(at) {
		t.Fatalf("the agents were given %v and the snapshot is dated %v, want %v", given, snap.UpdatedAt, at)
	}
}
