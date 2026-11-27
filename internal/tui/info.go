package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

type infoMsg struct {
	key  string
	seq  uint64
	info provider.Info
}

type infoRequest struct {
	item session.Session
	seq  uint64
}

const infoDelay = 150 * time.Millisecond

// preview requests both activity and metadata off the update loop. Selection
// and refresh generations prevent an older response replacing newer facts.
func (m *Model) preview() tea.Cmd {
	var inspect tea.Cmd
	if it, ok := m.selected(); ok && !it.project {
		s := it.session
		if !m.infoPending[s.Key()] {
			seq := m.infoSeq
			inspect = tea.Tick(infoDelay, func(time.Time) tea.Msg { return infoRequest{item: s, seq: seq} })
		}
	}
	return tea.Batch(m.activityPreview(), inspect)
}

func infoLines(agent session.Provider, info provider.Info, now time.Time) []string {
	var lines []string
	for _, row := range provider.InfoRows(agent, info, now) {
		line := row.Value
		if row.Code {
			line = shortPath(line)
		}
		if row.Label != "" {
			line = row.Label + ": " + line
		}
		lines = append(lines, line)
		if row.Note != "" {
			lines = append(lines, row.Note)
		}
	}
	return lines
}

func (m *Model) information(p palette) []string {
	it, ok := m.selected()
	if !ok || it.project {
		return m.overlay(m.box(p, "Session information", p.accent, []string{"Select a session first."}, "esc back"))
	}
	body := m.informationBody(it.session)
	visible := max(1, m.height-10)
	offset := min(m.infoOffset, max(0, len(body)-visible))
	body = body[offset:min(len(body), offset+visible)]
	return m.overlay(m.box(p, "Session information", p.accent, body, "↑↓ scroll · esc back"))
}

func (m *Model) scrollInformation(delta int) {
	it, ok := m.selected()
	if !ok || it.project {
		return
	}
	last := max(0, len(m.informationBody(it.session))-max(1, m.height-10))
	m.infoOffset = min(max(0, m.infoOffset+delta), last)
}

func (m *Model) informationBody(item session.Session) []string {
	info, loaded := m.infos[item.Key()]
	if !loaded {
		info.Folder = item.Directory
	}
	var body []string
	for _, line := range infoLines(item.Provider, info, m.now()) {
		body = append(body, wrap(line, min(64, m.width-12), 100)...)
	}
	if !loaded {
		body = append(body, "Loading…")
	}
	for _, problem := range info.Problems {
		body = append(body, wrap(session.Text(problem, 300), min(64, m.width-12), 100)...)
	}
	return body
}
