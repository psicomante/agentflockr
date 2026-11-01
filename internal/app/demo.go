package app

import (
	"fmt"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

// Demo returns sample sessions spanning every status, dated relative to now.
func Demo() Snapshot {
	now := time.Now()
	type example struct {
		provider                          session.Provider
		project, title, summary, evidence string
		status                            session.Status
		age                               time.Duration
	}
	examples := []example{
		{session.OpenCode, "agentflockr", "Build session adapters", "Permission requested: run the integration tests with the race detector.", "OpenCode API: pending permission", session.Waiting, time.Minute},
		{session.Claude, "website", "Refine the checkout flow", "Which payment provider should the new checkout use: Stripe or Adyen?", "Transcript: user question requested", session.Waiting, 2 * time.Minute},
		{session.Codex, "api", "Investigate rate limiting", "The concurrency test still fails under load; the limiter resets too early.", "Transcript: error reported", session.Failed, 4 * time.Minute},
		{session.Codex, "agentflockr", "Add project history", "Implementing the history filter and its tests.", "Transcript: turn started", session.Working, 12 * time.Second},
		{session.OpenCode, "website", "Improve accessibility", "Checking keyboard navigation and color contrast on the product pages.", "OpenCode API: running turn", session.Working, 30 * time.Second},
		{session.Claude, "api", "Document authentication", "Documentation updated with the token refresh flow and examples.", "Transcript: last turn completed", session.Completed, time.Hour},
		{session.Codex, "website", "Fix checkout tests", "All 18 checkout tests pass.", "Transcript: last turn completed", session.Completed, 2 * time.Hour},
		{session.OpenCode, "agentflockr", "Define the MVP", "Confirmed the local control room scope.", "OpenCode API: last turn succeeded", session.Completed, 26 * time.Hour},
		{session.Claude, "archive", "Review old experiment", "Historical session with no live status evidence.", "Cached history; awaiting a successful provider scan", session.Unknown, 7 * 24 * time.Hour},
	}
	snap := Snapshot{UpdatedAt: now}
	for i, x := range examples {
		dir := "/work/" + x.project
		snap.Sessions = append(snap.Sessions, session.Session{ID: fmt.Sprintf("demo_%d", i), Provider: x.provider, ProjectID: x.project, Directory: dir, OriginalDirectory: dir, Title: x.title, Summary: x.summary, Status: x.status, Evidence: x.evidence, UpdatedAt: now.Add(-x.age)})
	}
	Sort(snap.Sessions)
	return snap
}
