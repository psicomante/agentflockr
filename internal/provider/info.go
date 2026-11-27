package provider

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

// InfoRow is a display-ready line shared by the dashboard and phone. A negative
// Percent means no meter; Code marks paths and identifiers, not executable text.
type InfoRow struct {
	Key, Label, Value, Note string
	Percent                 float64
	ResetsAt                time.Time
	Code                    bool
}

var claudeModelName = regexp.MustCompile(`^claude-([a-z]+)-(\d+)(?:-(\d+))?(?:-\d{8})?(\[.*\])?$`)

// RecordedInputSource distinguishes recorded requests from live measurements.
const RecordedInputSource = "Last reported input, including cache; not a live measurement"

// ReportedInput rejects missing, negative and overflowing token components.
func ReportedInput(parts ...*int) (int, bool) {
	total := 0
	for _, n := range parts {
		if n == nil || *n < 0 || total+*n < total {
			return 0, false
		}
		total += *n
	}
	return total, true
}

// ModelName shortens Claude IDs just as Claude Code does; other IDs stay intact.
func ModelName(id string) string {
	m := claudeModelName.FindStringSubmatch(id)
	if m == nil {
		return id
	}
	name := strings.ToUpper(m[1][:1]) + m[1][1:] + " " + m[2]
	if m[3] != "" {
		name += "." + m[3]
	}
	if m[4] != "" {
		name += " " + m[4]
	}
	return name
}

// InfoRows owns the row order, labels, missing values and formatting. Sources
// remain separate: Git changes never become evidence of an agent's edits.
func InfoRows(agent session.Provider, info Info, now time.Time) []InfoRow {
	var rows []InfoRow
	add := func(key, label, value, note string, code bool) {
		value = session.Text(value, 4096)
		if value == "" {
			value = "Unavailable"
		}
		rows = append(rows, InfoRow{Key: key, Label: label, Value: value, Note: session.Text(note, 4096), Percent: -1, Code: code})
	}
	model := ModelName(session.Text(info.Model, 80))
	if model == "" {
		model = "Unavailable"
	}
	if effort := session.Text(info.Effort, 20); effort != "" {
		model += " · " + effort + " effort"
	}
	add("model", "Model", model, "", false)
	context := "Unavailable"
	known := (info.ContextKnown || info.ContextTokens > 0) && info.ContextTokens >= 0
	percent := -1.0
	if known {
		context = compactTokens(info.ContextTokens) + " tokens · window unavailable"
		if info.ContextWindow > 0 {
			percent = float64(info.ContextTokens) * 100 / float64(info.ContextWindow)
			context = fmt.Sprintf("%.0f%% · %s of %s", percent, compactTokens(info.ContextTokens), compactTokens(info.ContextWindow))
		}
	} else if info.ContextWindow > 0 {
		context = "Usage unavailable · window " + compactTokens(info.ContextWindow)
	}
	add("context", "Context", context, info.ContextSource, false)
	rows[len(rows)-1].Percent = percent
	var counts []string
	if info.CostUSD > 0 && !math.IsInf(info.CostUSD, 0) && !math.IsNaN(info.CostUSD) {
		cost := fmt.Sprintf("$%.2f", info.CostUSD)
		if info.CostUSD < .01 {
			cost = "under $0.01"
		}
		counts = append(counts, cost)
	}
	for _, c := range []struct {
		n         int
		one, many string
	}{{info.Turns, "prompt", "prompts"}, {info.ToolCalls, "tool call", "tool calls"}} {
		if c.n > 0 {
			word := c.many
			if c.n == 1 {
				word = c.one
			}
			counts = append(counts, fmt.Sprintf("%d %s", c.n, word))
		}
	}
	if len(counts) > 0 {
		add("counts", "", strings.Join(counts, " · "), "", false)
	}
	missing := "not reported by this session"
	if agent == session.OpenCode {
		missing = "not reported for this connection"
	}
	for _, window := range []struct{ key, label string }{{"five_hour", "5-hour"}, {"seven_day", "Weekly"}} {
		value, note, p := "Unavailable · "+missing, "", -1.0
		var reset time.Time
		for _, l := range info.Limits {
			if l.Kind != window.key {
				continue
			}
			if !l.ResetsAt.IsZero() && !l.ResetsAt.After(now) {
				value = "Unavailable · reset since last reported"
				note = info.LimitsSource
				break
			}
			if l.PercentUsed < 0 || math.IsNaN(l.PercentUsed) || math.IsInf(l.PercentUsed, 0) {
				continue
			}
			p, reset, note = l.PercentUsed, l.ResetsAt, info.LimitsSource
			value = fmt.Sprintf("%.0f%%", p)
			if !reset.IsZero() {
				value += " · resets " + ResetsIn(reset, now)
			}
			break
		}
		add(window.key, window.label, value, note, false)
		rows[len(rows)-1].Percent, rows[len(rows)-1].ResetsAt = p, reset
	}
	labels := map[session.Provider]string{session.Claude: "Permission mode", session.Codex: "Approval policy", session.OpenCode: "Agent mode"}
	mode := session.Text(info.Mode, 80)
	if agent == session.Claude {
		names := map[string]string{"default": "Manual", "auto": "Auto", "acceptEdits": "Accept edits", "plan": "Plan", "dontAsk": "Don't ask", "bypassPermissions": "Bypass permissions"}
		if name, ok := names[mode]; ok {
			mode = name
		}
	} else if agent == session.OpenCode {
		if mode == "plan" {
			mode = "Plan"
		}
		if mode == "build" {
			mode = "Build"
		}
	}
	add("mode", labels[agent], mode, "", false)
	if info.Connection != "" || agent == session.OpenCode {
		add("connection", "Connection", info.Connection, "", false)
	}
	profile := info.Profile
	if profile == "" && info.Home != "" {
		profile = info.Home
		if !strings.HasPrefix(profile, "/") && !strings.HasPrefix(profile, "~/") {
			profile = "~/" + profile
		}
	}
	if profile != "" {
		add("profile", "Profile", profile, "", true)
	}
	add("folder", "Folder", info.Folder, "", true)
	add("worktree", "Worktree (current)", info.Worktree, "", true)
	add("branch", "Branch (current)", info.Branch, "", false)
	if info.Repo != "" {
		add("repository", "Repository", info.Repo, "", true)
	}
	if len(info.MCP) > 0 {
		var names []string
		for _, m := range info.MCP {
			names = append(names, fmt.Sprintf("%s (%d)", session.Text(m.Name, 100), m.Tools))
		}
		add("mcp", "MCP", strings.Join(names, ", "), "", false)
	}
	if version := session.Text(info.Version, 40); version != "" {
		add("version", "Version", version, "", false)
	}
	files := "Unavailable"
	if info.FilesKnown {
		files = strconv.Itoa(len(info.Files))
	}
	add("files", "Files reported by the agent", files, info.FilesSource, false)
	files = "Unavailable"
	if info.GitKnown {
		files = strconv.Itoa(len(info.GitFiles))
		if len(info.GitFiles) == 0 {
			files = "none"
		}
	}
	add("git_files", "Current worktree changes", files, "Git working copy, including staged and untracked files. Not attributed to this session.", false)
	return rows
}

func compactTokens(n int) string {
	for _, unit := range []struct {
		size   int
		suffix string
	}{{1000000, "M"}, {1000, "k"}} {
		if n >= unit.size {
			return strconv.FormatFloat(math.Round(float64(n)/float64(unit.size)*10)/10, 'f', -1, 64) + unit.suffix
		}
	}
	return strconv.Itoa(n)
}

// ResetsIn is the phone's relative reset rule, also used by the dashboard.
// Time is an input so both renderers and their tests can use the same instant.
func ResetsIn(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	minutes := int(math.Max(0, math.Floor(at.Sub(now).Minutes()+.5)))
	if minutes < 1 {
		return "now"
	}
	if minutes < 60 {
		return fmt.Sprintf("in %d min", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		if minutes%60 > 0 {
			return fmt.Sprintf("in %d h %d min", hours, minutes%60)
		}
		return fmt.Sprintf("in %d h", hours)
	}
	if hours%24 > 0 {
		return fmt.Sprintf("in %d d %d h", hours/24, hours%24)
	}
	return fmt.Sprintf("in %d d", hours/24)
}

// Informer providers say what a live session reports about itself.
type Informer interface {
	Info(s session.Session) (Info, bool)
}

// Inspector reads metadata on demand. It must not change an agent or run a turn.
type Inspector interface {
	Inspect(context.Context, session.Session) (Info, error)
}

// MaxInfoFiles bounds each independently sourced file list sent to a screen.
const MaxInfoFiles = 200

// Info combines reported session metadata with independently sourced local
// facts. Empty values are unknown unless an explicit Known flag says otherwise.
type Info struct {
	Folder string
	// SelectionRead makes Mode/Model/Effort/Connection authoritative even
	// when empty. A partial inspector (such as transcript file evidence)
	// leaves live selection metadata alone.
	SelectionRead bool
	// ContextKnown distinguishes a reported zero from missing usage.
	ContextKnown  bool
	ContextSource string
	Files         []FileChange
	FilesKnown    bool
	FilesSource   string
	GitFiles      []FileChange
	GitKnown      bool
	Problems      []string
	// Mode is Claude's permission mode, Codex's approval policy and sandbox,
	// or OpenCode's selected agent.
	// Empty means unavailable; it is never inferred from working/waiting status.
	Mode    string
	Model   string
	Effort  string // such as "high"
	Version string // the agent's own
	// Home is the legacy profile basename; Profile is its full directory.
	Home       string
	Profile    string // configured profile directory, when known
	Connection string // OpenCode connection name and ID
	Repo       string
	Branch     string
	Worktree   string // the linked worktree's folder, or "main tree"
	// Context is what the conversation uses of the model's window, in
	// tokens.
	ContextTokens  int
	ContextWindow  int
	ContextPercent float64
	CostUSD        float64
	Turns          int // the person's prompts so far
	ToolCalls      int
	// Limits are the account's rate limits, the same for each of its
	// sessions.
	LimitsSource string
	Limits       []Limit
	MCP          []MCPServer
	At           time.Time
}

// FileChange is a reported filename, never a path to read or write.
type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type Limit struct {
	Kind        string // such as "five_hour"
	PercentUsed float64
	ResetsAt    time.Time
}

type MCPServer struct {
	Name  string
	Tools int
}
