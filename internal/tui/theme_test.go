package tui

import (
	"image/color"
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

// contrast is the WCAG contrast ratio of two colors, from 1 to 21.
func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestThemesAreLegible covers REQ-DSH-020: in every named theme, each color
// used for text reads at 4.5 to 1 on the background and 3 to 1 on a selected
// row. Default comes first and has no palette of its own; IDs are unique.
func TestThemesAreLegible(t *testing.T) {
	if themes[0].ID != DefaultTheme || themes[0].palette.background != nil {
		t.Fatal("Default must be the first theme and follow the terminal")
	}
	seen := map[string]bool{}
	for _, th := range themes {
		if seen[th.ID] {
			t.Fatalf("theme %q is listed twice", th.ID)
		}
		seen[th.ID] = true
		if th.ID == DefaultTheme {
			continue
		}
		p := th.palette
		roles := map[string]color.Color{
			"text": p.text, "accent": p.accent, "muted": p.muted, "waiting": p.waiting,
			"working": p.working, "completed": p.completed, "failed": p.failed, "quiet": p.quiet,
		}
		for role, c := range roles {
			if got := contrast(c, p.background); got < 4.5 {
				t.Errorf("%s %s: %.2f to 1 on the background, want 4.5", th.Name, role, got)
			}
			if got := contrast(c, p.selection); got < 3 {
				t.Errorf("%s %s: %.2f to 1 on a selected row, want 3", th.Name, role, got)
			}
		}
	}
}
