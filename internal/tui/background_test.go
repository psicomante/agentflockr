package tui

import (
	"context"
	"image/color"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/app"
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

// TestThemeFromOptionsIsApplied covers REQ-DSH-020: a named theme asks the
// terminal for its background and text color; Default asks for neither.
func TestThemeFromOptionsIsApplied(t *testing.T) {
	m := newModel(t, fixture())
	if v := m.View(); v.BackgroundColor != nil || v.ForegroundColor != nil {
		t.Fatal("Default must keep the terminal's own colors")
	}
	engine := &app.Engine{Providers: registry()}
	m = New(context.Background(), engine, Options{Refresh: time.Second, Theme: "gruvbox-dark"}, app.Snapshot{})
	want := themes[themeIndex("gruvbox-dark")].palette
	if v := m.View(); v.BackgroundColor != want.background || v.ForegroundColor != want.text {
		t.Fatalf("got %v and %v, want Gruvbox Dark's", v.BackgroundColor, v.ForegroundColor)
	}
}

// TestBackgroundReplyIsIgnoredUnderANamedTheme covers REQ-DSH-016: under a
// named theme the terminal reports the theme's own background, so the reply
// is not used; going back to Default asks the terminal again.
func TestBackgroundReplyIsIgnoredUnderANamedTheme(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	m.useTheme("tokyo-night")
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if m.dark {
		t.Fatal("a reply under a named theme changed the terminal's palette")
	}
	cmd := m.useTheme(DefaultTheme)
	if cmd == nil {
		t.Fatal("going back to Default must ask for the terminal's background")
	}
	if m.useTheme(DefaultTheme) != nil {
		t.Fatal("staying on Default must not ask again")
	}
}
