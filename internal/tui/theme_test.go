package tui

import (
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

// TestEveryStatusHasAWord covers REQ-DSH-002: each status shows a symbol and
// a word, so the list reads without color. It walks every status, so a new
// one without a row here fails the test.
func TestEveryStatusHasAWord(t *testing.T) {
	want := map[session.Status][2]string{
		session.Waiting:   {"●", "waiting"},
		session.Failed:    {"✕", "failed"},
		session.Working:   {"◐", "working"},
		session.Completed: {"✓", "done"},
		session.Idle:      {"○", "idle"},
		session.Unknown:   {"◌", "unknown"},
	}
	for _, value := range session.Status("").Values() {
		status := session.Status(value)
		expected, ok := want[status]
		if !ok {
			t.Fatalf("status %q has no row in the REQ-DSH-002 table", status)
		}
		glyph, label := mark(status)
		if glyph != expected[0] || label != expected[1] {
			t.Fatalf("status %q shows %q %q, want %q %q", status, glyph, label, expected[0], expected[1])
		}
	}
}
