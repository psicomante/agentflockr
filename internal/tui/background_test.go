package tui

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/session"
)

// TestPaletteFollowsTheTerminalBackground covers REQ-DSH-016: the terminal
// reports its background, the model picks the light or dark palette to
// match, and each status has its own shade in each, so a light terminal
// gets the deeper colors.
func TestPaletteFollowsTheTerminalBackground(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.dark {
		t.Fatal("a white terminal must select the light palette")
	}
	light := paletteFor(m.dark)
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if !m.dark {
		t.Fatal("a black terminal must select the dark palette")
	}
	dark := paletteFor(m.dark)
	for _, value := range session.Status("").Values() {
		status := session.Status(value)
		lr, lg, lb, la := light.status(status).RGBA()
		dr, dg, db, da := dark.status(status).RGBA()
		if lr == dr && lg == dg && lb == db && la == da {
			t.Fatalf("status %q has the same color on a light and a dark terminal", status)
		}
	}
}
