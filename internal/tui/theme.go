package tui

import (
	"image/color"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/psicomante/agentflockr/internal/session"
)

// palette holds the colors for one theme. Default's two palettes, the Signal
// Desk accents for a light or a dark terminal, leave background and text nil
// so body text keeps the terminal's own colors; a named theme sets both.
type palette struct {
	background, text                           color.Color
	accent, muted, faint, selection            color.Color
	waiting, working, completed, failed, quiet color.Color
}

// Theme is one choice in the theme picker. Its ID is the settings file's
// value; Name is what the picker shows.
type Theme struct{ ID, Name string }

// DefaultTheme follows the terminal's background (REQ-DSH-016).
const DefaultTheme = "default"

// themes lists every theme in the picker's order, Default first. Each named
// palette uses its scheme's published colors, except where a color is too
// pale (or, on Gruvbox Dark, too dark) to read as text: those are the same
// hue moved until text reaches 4.5 to 1 on the background and 3 to 1 on the
// selection (TestThemesAreLegible).
var themes = []struct {
	Theme
	palette palette
}{
	{Theme: Theme{DefaultTheme, "Default"}},
	{Theme{"tokyo-night", "Tokyo Night"}, named("#1A1B26", "#C0CAF5", "#7AA2F7", "#9AA5CE", "#3B4261", "#283457", "#E0AF68", "#7DCFFF", "#9ECE6A", "#F7768E", "#A9B1D6")},
	{Theme{"tokyo-night-day", "Tokyo Night Day"}, named("#E1E2E7", "#3760BF", "#1561CA", "#5061A0", "#A8AECB", "#C4C8DA", "#7C6037", "#006D92", "#526C35", "#C90941", "#5C638A")},
	{Theme{"catppuccin-mocha", "Catppuccin Mocha"}, named("#1E1E2E", "#CDD6F4", "#CBA6F7", "#A6ADC8", "#585B70", "#45475A", "#F9E2AF", "#89B4FA", "#A6E3A1", "#F38BA8", "#BAC2DE")},
	{Theme{"catppuccin-latte", "Catppuccin Latte"}, named("#EFF1F5", "#4C4F69", "#8839EF", "#6A6D82", "#BCC0CC", "#CCD0DA", "#996114", "#1761F5", "#327C21", "#D20F39", "#5C5F77")},
	{Theme{"gruvbox-dark", "Gruvbox Dark"}, named("#282828", "#EBDBB2", "#FE8019", "#BDAE93", "#665C54", "#3C3836", "#FABD2F", "#83A598", "#B8BB26", "#FB5441", "#D5C4A1")},
	{Theme{"gruvbox-light", "Gruvbox Light"}, named("#FBF1C7", "#3C3836", "#AF3A03", "#786B61", "#D5C4A1", "#EBDBB2", "#976311", "#076678", "#74700D", "#9D0006", "#665C54")},
}

// named builds a theme's palette from its colors, in the palette's order.
func named(background, text, accent, muted, faint, selection, waiting, working, completed, failed, quiet string) palette {
	c := lipgloss.Color
	return palette{
		background: c(background), text: c(text), accent: c(accent), muted: c(muted), faint: c(faint),
		selection: c(selection), waiting: c(waiting), working: c(working), completed: c(completed),
		failed: c(failed), quiet: c(quiet),
	}
}

// Themes lists every theme, Default first, in the picker's order.
func Themes() []Theme {
	out := make([]Theme, len(themes))
	for i, t := range themes {
		out[i] = t.Theme
	}
	return out
}

// themeIndex finds a theme by its ID, or -1.
func themeIndex(id string) int {
	for i, t := range themes {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// palette returns the colors of the theme on screen: the named theme's, or
// Default's light or dark palette to match the terminal.
func (m *Model) palette() palette {
	if i := themeIndex(m.theme); i > 0 {
		return themes[i].palette
	}
	return paletteFor(m.dark)
}

// namedTheme reports whether a named theme, not Default, is on screen.
func (m *Model) namedTheme() bool { return themeIndex(m.theme) > 0 }

func paletteFor(dark bool) palette {
	if dark {
		return palette{
			accent: lipgloss.Color("#E4774E"), muted: lipgloss.Color("#8E968B"), faint: lipgloss.Color("#4A5750"),
			selection: lipgloss.Color("#2B3832"),
			waiting:   lipgloss.Color("#EFD28C"), working: lipgloss.Color("#A9C9DC"), completed: lipgloss.Color("#B8C8A9"),
			failed: lipgloss.Color("#F1A39A"), quiet: lipgloss.Color("#B7BDB3"),
		}
	}
	// The brand's status colors are drawn for dark panels; on a light
	// background each uses a deeper shade of the same hue.
	return palette{
		accent: lipgloss.Color("#A43D21"), muted: lipgloss.Color("#62695F"), faint: lipgloss.Color("#C9CDC1"),
		selection: lipgloss.Color("#E6E2D3"),
		waiting:   lipgloss.Color("#8A6514"), working: lipgloss.Color("#2E6A8A"), completed: lipgloss.Color("#4B6A36"),
		failed: lipgloss.Color("#B3261E"), quiet: lipgloss.Color("#6E756B"),
	}
}

func (p palette) status(s session.Status) color.Color {
	switch s {
	case session.Waiting:
		return p.waiting
	case session.Working:
		return p.working
	case session.Completed:
		return p.completed
	case session.Failed:
		return p.failed
	default:
		return p.quiet
	}
}

// mark returns the glyph and label shown for a status. Every status keeps a
// text label beside its color, so the list reads without color too.
func mark(s session.Status) (glyph, label string) {
	switch s {
	case session.Waiting:
		return "●", "waiting"
	case session.Failed:
		return "✕", "failed"
	case session.Working:
		return "◐", "working"
	case session.Completed:
		return "✓", "done"
	case session.Idle:
		return "○", "idle"
	default:
		return "◌", "unknown"
	}
}

// styleInput colors the text fields for the theme on screen.
func (m *Model) styleInput() {
	p := m.palette()
	s := textinput.DefaultStyles(m.isDark())
	prompt := lipgloss.NewStyle().Foreground(p.accent)
	s.Focused.Prompt, s.Blurred.Prompt = prompt, prompt
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(p.muted)
	s.Blurred.Placeholder = s.Focused.Placeholder
	s.Cursor.Color = p.accent
	m.input.SetStyles(s)
	m.path.SetStyles(s)
	m.schedule.prompt.SetStyles(s)
	m.schedule.every.SetStyles(s)
	m.editor.input.SetStyles(s)
}

// paint renders text in a foreground color, on the selection background when
// the row is selected, so highlighted rows keep their status colors.
type paint struct {
	p        palette
	selected bool
}

func (t paint) style() lipgloss.Style {
	s := lipgloss.NewStyle()
	if t.selected {
		s = s.Background(t.p.selection)
	}
	return s
}

func (t paint) plain(text string) string { return t.style().Render(text) }
func (t paint) bold(text string) string  { return t.style().Bold(true).Render(text) }
func (t paint) fg(c color.Color, text string) string {
	return t.style().Foreground(c).Render(text)
}

// isDark reports whether the theme on screen has a dark background: the
// terminal's, for Default, or the named theme's own.
func (m *Model) isDark() bool {
	if !m.namedTheme() {
		return m.dark
	}
	return luminance(m.palette().background) < 0.5
}

// luminance is a color's relative luminance, from 0 for black to 1 for
// white, as WCAG defines it for contrast ratios.
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	channel := func(v uint32) float64 {
		s := float64(v) / 0xffff
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// askBackgroundAfter is how long the dashboard waits, after leaving a named
// theme, before asking the terminal for its background: long enough for the
// next frame to give the terminal its own colors back, so the reply is not
// the theme's.
const askBackgroundAfter = 100 * time.Millisecond

// useTheme puts the theme with the ID on screen. Leaving a named theme for
// Default asks the terminal for its own background again.
func (m *Model) useTheme(id string) tea.Cmd {
	wasNamed := m.namedTheme()
	m.theme = id
	m.styleInput()
	if wasNamed && !m.namedTheme() {
		return tea.Tick(askBackgroundAfter, func(time.Time) tea.Msg { return tea.RequestBackgroundColor() })
	}
	return nil
}
