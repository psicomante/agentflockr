package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The dashboard captures the mouse for clicks and the wheel, which stops
// the terminal from selecting text. The details pane selects its own text
// instead: a drag highlights it, and the release copies it through the
// terminal (OSC 52), so nothing but the terminal sees it.

// spot is a cell in the details pane: a line and a column within it.
type spot struct{ row, col int }

// selection is a drag over the details pane, from where it was pressed
// (from) to where the pointer is now (to).
type selection struct {
	active   bool // the button is still down
	shown    bool // the highlight stays after the release
	from, to spot
}

// detailX is the screen column where the details pane starts: the margin,
// the list, then a space, the one-column scrollbar and a space.
func (l layout) detailX() int { return margin + l.listWidth + 3 }

// detailSpot maps a screen cell to the details pane, clamped to its edges
// when the drag leaves it. ok is false for a cell outside the pane when
// clamp is off, or when there is no pane.
func (m *Model) detailSpot(x, y int, clamp bool) (spot, bool) {
	l := m.layout()
	if l.detailWidth == 0 || l.listHeight == 0 {
		return spot{}, false
	}
	s := spot{row: y - l.listTop, col: x - l.detailX()}
	inside := s.row >= 0 && s.row < l.listHeight && s.col >= 0 && s.col < l.detailWidth
	if !inside && !clamp {
		return spot{}, false
	}
	s.row = min(max(s.row, 0), l.listHeight-1)
	s.col = min(max(s.col, 0), l.detailWidth)
	return s, true
}

// pressDetails starts a selection when the press lands in the details pane.
func (m *Model) pressDetails(x, y int) bool {
	s, ok := m.detailSpot(x, y, false)
	if !ok {
		return false
	}
	m.sel = selection{active: true, from: s, to: s}
	return true
}

func (m *Model) dragDetails(x, y int) {
	if !m.sel.active {
		return
	}
	if s, ok := m.detailSpot(x, y, true); ok {
		m.sel.to = s
	}
}

// releaseDetails ends the drag and copies what it covered. A press without
// a drag selects nothing and clears the highlight.
func (m *Model) releaseDetails(x, y int) tea.Cmd {
	if !m.sel.active {
		return nil
	}
	m.dragDetails(x, y)
	m.sel.active, m.sel.shown = false, true
	text := m.selectedText()
	if text == "" {
		m.sel = selection{}
		return nil
	}
	m.notice = "Copied " + plural(len([]rune(text)), "character") + " to the clipboard."
	return tea.SetClipboard(text)
}

// bounds orders the selection's ends: start is before end in reading order.
func (s selection) bounds() (start, end spot) {
	start, end = s.from, s.to
	if end.row < start.row || (end.row == start.row && end.col < start.col) {
		start, end = end, start
	}
	return start, end
}

// span is the columns the selection covers on one pane line, as [a, b).
// A line in the middle of the selection is covered whole.
func (s selection) span(row, width int) (a, b int, ok bool) {
	if !s.active && !s.shown {
		return 0, 0, false
	}
	start, end := s.bounds()
	if row < start.row || row > end.row {
		return 0, 0, false
	}
	a, b = 0, width
	if row == start.row {
		a = start.col
	}
	if row == end.row {
		b = end.col
	}
	return a, b, a < b
}

// selectedText is the plain text under the selection: each pane line's
// covered columns without styling or trailing spaces, one per line.
func (m *Model) selectedText() string {
	l := m.layout()
	if l.detailWidth == 0 {
		return ""
	}
	lines := m.details(paletteFor(m.dark), l.detailWidth, l.listHeight)
	var out []string
	for row, line := range lines {
		a, b, ok := m.sel.span(row, l.detailWidth)
		if !ok {
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(line, a, b)), " "))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// highlight shows the selection on the rendered pane lines.
func (m *Model) highlight(lines []string, width int) {
	mark := lipgloss.NewStyle().Reverse(true)
	for row, line := range lines {
		a, b, ok := m.sel.span(row, width)
		if !ok {
			continue
		}
		lines[row] = ansi.Cut(line, 0, a) + mark.Render(ansi.Strip(ansi.Cut(line, a, b))) + ansi.Cut(line, b, width)
	}
}
