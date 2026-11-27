package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/psicomante/agentflockr/internal/config"
	"github.com/psicomante/agentflockr/internal/tui"
)

// settingSpec describes one row of the Settings view: how it is edited,
// when it applies, its text, what can set it over the file, and how to read
// its value from the settings.
type settingSpec struct {
	key     string
	kind    tui.SettingKind
	applies tui.SettingApplies
	about   string
	// flag and env can set the value over the file; "" when nothing can.
	flag, env string
	// def is the default as shown, for a value with no fixed default.
	def   string
	how   string
	value func(config.Settings) string
}

func duration(d config.Duration) string {
	text, _ := d.MarshalText()
	return string(text)
}

// settingSpecs are the rows, in the view's order (config.Editable's, with
// data-dir among them). serveSettingSpecs adds the server's rows in builds
// with the phone app.
var settingSpecs = []settingSpec{
	{key: "theme", kind: tui.SettingTheme, applies: tui.AppliesNow,
		about: "The dashboard's colors. Default follows the terminal's background.",
		value: func(s config.Settings) string { return s.Theme }},
	{key: "refresh", kind: tui.SettingDuration, applies: tui.AppliesNow, flag: "refresh",
		about: fmt.Sprintf("How often agentflockr looks for changes. At least %v.", config.MinRefresh),
		value: func(s config.Settings) string { return duration(s.Refresh) }},
	{key: "recent-window", kind: tui.SettingDuration, applies: tui.AppliesNow,
		about: "Sessions active within this time are listed under RECENT, older ones under EARLIER.",
		value: func(s config.Settings) string { return duration(s.RecentWindow) }},
	{key: "providers", kind: tui.SettingAgents, applies: tui.AppliesNextStart, flag: "providers",
		about: "The agents to watch, in the order they are listed.",
		value: func(s config.Settings) string { return strings.Join(s.Providers, ",") }},
	{key: "codex.home", kind: tui.SettingFolder, applies: tui.AppliesNextStart, flag: "codex-home", env: "CODEX_HOME",
		about: "Where Codex keeps its sessions.", def: "~/.codex",
		value: func(s config.Settings) string { return s.Codex.Home }},
	{key: "codex.stale-after", kind: tui.SettingDuration, applies: tui.AppliesNextStart,
		about: "How long a Codex session counts as working or waiting without a newer record.",
		value: func(s config.Settings) string { return duration(s.Codex.StaleAfter) }},
	{key: "claude.home", kind: tui.SettingFolder, applies: tui.AppliesNextStart, flag: "claude-home", env: "CLAUDE_CONFIG_DIR",
		about: "Where Claude Code keeps its sessions. Set here, it is read alone, without the other ~/.claude-* homes.",
		def:   "~/.claude and its profiles",
		value: func(s config.Settings) string { return s.Claude.Home }},
	{key: "claude.stale-after", kind: tui.SettingDuration, applies: tui.AppliesNextStart,
		about: "How long a Claude Code session counts as working or waiting without a newer record.",
		value: func(s config.Settings) string { return duration(s.Claude.StaleAfter) }},
	{key: "data-dir", kind: tui.SettingFixed, applies: tui.AppliesNextStart, flag: "data-dir", env: "AGENTFLOCKR_DATA_DIR",
		about: "Where agentflockr keeps its index, with your scheduled prompts.",
		def:   "~/.local/share/agentflockr", how: "Change it in the settings file, or with --data-dir.",
		value: func(s config.Settings) string { return s.DataDir }},
}

// serveSettingSpecs are the server's rows, filled by phone.go.
var serveSettingSpecs []settingSpec

// settingRows builds the Settings view's rows. file holds the settings as
// the file alone gives them; o.s, the settings in effect. A setting that
// applies at once and has no override is in effect as the file says, so
// rows read back after a save show what the dashboard now uses.
func settingRows(o options, file config.Settings) []tui.Setting {
	defaults := config.Default()
	// The file is read again only to tell a value it writes from one it
	// leaves to the default; a file that can't be read sets nothing.
	text, _ := os.ReadFile(o.settingsFile)
	var rows []tui.Setting
	for _, sp := range append(append([]settingSpec(nil), settingSpecs...), serveSettingSpecs...) {
		row := tui.Setting{Key: sp.key, About: sp.about, Kind: sp.kind, Applies: sp.applies,
			Default: first(sp.def, sp.value(defaults)), How: sp.how, Source: o.source(sp)}
		if config.Names(text, sp.key) {
			row.File = sp.value(file)
		}
		row.InEffect = sp.value(o.s)
		if sp.applies == tui.AppliesNow && row.Source == "" {
			row.InEffect = sp.value(file)
		}
		// o.file is the file as this run read it, so a difference is a save
		// made since, which this run does not use.
		row.Pending = sp.applies != tui.AppliesNow && row.Source == "" && sp.value(file) != sp.value(o.file)
		if sp.kind == tui.SettingFolder || sp.kind == tui.SettingFixed {
			row.InEffect = tildify(row.InEffect, o.home)
		}
		if sp.kind == tui.SettingAgents {
			row.Choices, row.ChoiceNames = agentNames(), agentTitles(o)
		}
		rows = append(rows, row)
	}
	return rows
}

// agentTitles names every agent, in the default order. Adapters are built
// as the demo builds them, needing no folders and doing no I/O, only to ask
// their names.
func agentTitles(o options) []string {
	o.demo = true
	titles := make([]string, len(agents))
	for i, a := range agents {
		titles[i] = a.name
		if adapter, err := a.build(o); err == nil {
			titles[i] = adapter.Meta().Title
		}
	}
	return titles
}

// source names the flag, else the variable, that set the setting over the
// file this run, or "".
func (o options) source(sp settingSpec) string {
	switch {
	case sp.flag != "" && o.flagsSet[sp.flag]:
		return "--" + sp.flag
	case sp.env != "" && os.Getenv(sp.env) != "":
		return sp.env
	}
	return ""
}

// tildify writes a path under the home folder as "~/…", as people type it.
func tildify(path, home string) string {
	if home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}

// saveSetting saves one key from the Settings view in the settings file
// that was read, and returns the rows read back from it (REQ-CLI-016).
func (o options) saveSetting(key, value string, reset bool) ([]tui.Setting, error) {
	var err error
	switch {
	case reset:
		err = config.Unset(o.settingsFile, key)
	case key == "providers":
		err = config.Set(o.settingsFile, key, strings.Split(value, ","))
	case key == "codex.home" || key == "claude.home":
		// A home is read at the next start; refuse one that is not there
		// now rather than fail then.
		if info, statErr := os.Stat(expandHome(value, o.home)); statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s: %s is not a folder", key, value)
		}
		err = config.Set(o.settingsFile, key, value)
	default:
		err = config.Set(o.settingsFile, key, value)
	}
	if err != nil {
		return nil, err
	}
	file, _, err := config.Load(o.settingsFile, false)
	if err != nil {
		return nil, err
	}
	return settingRows(o, file), nil
}
