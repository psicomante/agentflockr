package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// detailRow finds the details pane line holding text, as screen coordinates
// of its first cell.
func detailRow(t *testing.T, m *Model, text string) (x, y int) {
	t.Helper()
	l := m.layout()
	for row, line := range m.details(paletteFor(m.dark), l.detailWidth, l.listHeight) {
		if col := strings.Index(ansi.Strip(line), text); col >= 0 {
			return l.detailX() + ansi.StringWidth(ansi.Strip(line)[:col]), l.listTop + row
		}
	}
	t.Fatalf("details pane has no line with %q", text)
	return 0, 0
}

func TestDraggingOverTheDetailsCopiesTheText(t *testing.T) {
	m := newModel(t, fixture())
	it, _ := m.selected()
	title := it.session.Title
	x, y := detailRow(t, m, title)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 3, Y: y})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + len(title), Y: y})
	if cmd == nil {
		t.Fatal("the release must copy the selection")
	}
	if got := fmt.Sprint(cmd()); got != title {
		t.Fatalf("copied %q, want %q", got, title)
	}
	if selectedID(m) != it.session.ID {
		t.Fatal("selecting text must not move the list's selection")
	}
	if !strings.Contains(screen(m), "Copied") {
		t.Fatal("the footer must say the text was copied")
	}
	if !strings.Contains(m.render(), "\x1b[7m") {
		t.Fatal("the selection must stay highlighted after the release")
	}

	// A click without a drag copies nothing and clears the highlight.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if _, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y}); cmd != nil {
		t.Fatal("a click must not copy")
	}
	if m.sel.shown {
		t.Fatal("a click must clear the highlight")
	}
}

func TestADragAcrossLinesCopiesEachLine(t *testing.T) {
	m := newModel(t, fixture())
	l := m.layout()
	_, y := detailRow(t, m, "Folder")
	// From the label's start to past the pane's right edge on the next line:
	// the drag is clamped to the pane, and the path comes whole.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: l.detailX(), Y: y})
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: m.width + 5, Y: y + 1})
	if cmd == nil {
		t.Fatal("the release must copy the selection")
	}
	lines := strings.Split(fmt.Sprint(cmd()), "\n")
	if len(lines) != 2 || lines[0] != "Folder" || lines[1] == "" {
		t.Fatalf("copied %q", lines)
	}
}
