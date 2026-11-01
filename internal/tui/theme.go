package tui

import (
	"image/color"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/psicomante/agentflockr/internal/session"
)

// palette holds the Signal Desk colors for one terminal background. Body
// text keeps the terminal's own foreground; only accents and statuses are set.
type palette struct {
	accent, muted, faint, selection            color.Color
	waiting, working, completed, failed, quiet color.Color
}

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

// styleInput colors the search field for the current background.
func (m *Model) styleInput() {
	p := paletteFor(m.dark)
	s := textinput.DefaultStyles(m.dark)
	prompt := lipgloss.NewStyle().Foreground(p.accent)
	s.Focused.Prompt, s.Blurred.Prompt = prompt, prompt
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(p.muted)
	s.Blurred.Placeholder = s.Focused.Placeholder
	s.Cursor.Color = p.accent
	m.input.SetStyles(s)
	m.path.SetStyles(s)
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
