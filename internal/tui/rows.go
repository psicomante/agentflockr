package tui

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

// group is a heading in the list. Order is attention order.
type group int

const (
	needsYou group = iota
	working
	recent
	earlier
)

var groupNames = [...]string{"NEEDS YOU", "WORKING", "RECENT", "EARLIER"}

// DefaultRecentWindow separates RECENT from EARLIER unless the
// recent-window setting changes it.
const DefaultRecentWindow = 24 * time.Hour

func groupFor(status session.Status, updated, now time.Time, window time.Duration) group {
	switch status {
	case session.Waiting, session.Failed:
		return needsYou
	case session.Working:
		return working
	}
	if now.Sub(updated) < window {
		return recent
	}
	return earlier
}

// item is one selectable row: a session, or a project represented by its
// most urgent session.
type item struct {
	session  session.Session
	project  bool
	sessions int       // projects: sessions in the project
	working  int       // projects: sessions working now
	updated  time.Time // latest activity; for a project, its newest session
	group    group
	members  []session.Session // projects: its sessions, most urgent first
}

// key identifies the row across refreshes so the selection can follow it.
func (it item) key() string {
	if it.project {
		return "project:" + it.session.ProjectID
	}
	return it.session.Key()
}

// line is one row of the list: a group heading or an item.
type line struct {
	heading bool
	group   group
	count   int // headings: items in the group
	item    int // items: index into the item slice
}

// filter selects the sessions the list shows.
type filter struct {
	provider session.Provider // "" for all agents
	project  string           // project ID when drilled into a project
	query    string
}

func (f filter) active() bool { return f.provider != "" || f.project != "" || f.query != "" }

func (f filter) match(s session.Session) bool {
	// Sub-agents and jobs an agent started on its own are part of their
	// parent's work, not sessions someone needs to watch.
	if !s.Root() {
		return false
	}
	if f.provider != "" && s.Provider != f.provider {
		return false
	}
	if f.project != "" && s.ProjectID != f.project {
		return false
	}
	if f.query == "" {
		return true
	}
	haystack := strings.ToLower(s.Title + " " + s.Summary + " " + s.Directory + " " + s.ID)
	for _, word := range strings.Fields(strings.ToLower(f.query)) {
		if !strings.Contains(haystack, word) {
			return false
		}
	}
	return true
}

// buildItems turns attention-sorted sessions into list rows. Sessions keep
// the engine's attention order inside NEEDS YOU; the other groups read
// newest first.
func buildItems(sessions []session.Session, f filter, projects bool, now time.Time, window time.Duration) []item {
	var items []item
	index := map[string]int{}
	for _, s := range sessions {
		if !f.match(s) {
			continue
		}
		if !projects {
			items = append(items, item{session: s, updated: s.UpdatedAt, group: groupFor(s.Status, s.UpdatedAt, now, window)})
			continue
		}
		if s.ProjectID == "" {
			continue
		}
		i, ok := index[s.ProjectID]
		if !ok {
			// The first session seen is the project's most urgent one.
			index[s.ProjectID] = len(items)
			items = append(items, item{session: s, project: true, updated: s.UpdatedAt})
			i = len(items) - 1
		}
		it := &items[i]
		it.sessions++
		it.members = append(it.members, s)
		if s.Status == session.Working {
			it.working++
		}
		if s.UpdatedAt.After(it.updated) {
			it.updated = s.UpdatedAt
		}
	}
	for i := range items {
		if items[i].project {
			items[i].group = groupFor(items[i].session.Status, items[i].updated, now, window)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if a.group == needsYou {
			if pa, pb := session.Priority(a.session.Status), session.Priority(b.session.Status); pa != pb {
				return pa < pb
			}
		}
		if !a.updated.Equal(b.updated) {
			return a.updated.After(b.updated)
		}
		return a.key() < b.key()
	})
	return items
}

// buildLines adds a heading before each non-empty group.
func buildLines(items []item) []line {
	var lines []line
	heading := 0
	for i, it := range items {
		if i == 0 || items[i-1].group != it.group {
			heading = len(lines)
			lines = append(lines, line{heading: true, group: it.group})
		}
		lines[heading].count++
		lines = append(lines, line{item: i, group: it.group})
	}
	return lines
}

// lineOf returns the list line that shows item i.
func lineOf(lines []line, i int) int {
	for n, l := range lines {
		if !l.heading && l.item == i {
			return n
		}
	}
	return 0
}

// projectName is the project folder's last element, cleaned here because the
// folder comes from agent data and every caller draws the result.
func projectName(s session.Session) string {
	return session.Text(filepath.Base(s.Directory), 200)
}
