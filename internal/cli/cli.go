// Package cli is agentflockr's command line: it reads the settings from
// their four layers, chooses the agents to watch, and starts the dashboard.
// cmd/agentflockr only calls Main, so everything here can be tested.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/buildinfo"
	"github.com/psicomante/agentflockr/internal/config"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/claude"
	"github.com/psicomante/agentflockr/internal/provider/codex"
	"github.com/psicomante/agentflockr/internal/provider/opencode"
	"github.com/psicomante/agentflockr/internal/session"
	"github.com/psicomante/agentflockr/internal/store"
	"github.com/psicomante/agentflockr/internal/tui"
)

// errReported means the flag package has already printed the problem and
// the usage, so it must not be printed again.
var errReported = errors.New("invalid flags")

// agent is one agent agentflockr can watch. build makes its adapter from the
// options, and fails when a folder the agent needs is unknown.
type agent struct {
	name  string
	build func(o options) (provider.Adapter, error)
}

// agents lists every agent in the default display order. Adding an agent is
// one row here. The --providers default and its messages come from this list.
// Construction performs no I/O, so the demo builds them too, for their names.
var agents = []agent{
	{"opencode", func(options) (provider.Adapter, error) { return opencode.New(), nil }},
	{"codex", func(o options) (provider.Adapter, error) {
		if o.s.Codex.Home == "" && !o.demo {
			return nil, missing("the Codex home", "--codex-home", "CODEX_HOME", "codex.home")
		}
		a := codex.New(o.s.Codex.Home)
		a.StaleAfter = o.s.Codex.StaleAfter.Duration
		return a, nil
	}},
	{"claude", func(o options) (provider.Adapter, error) {
		if o.s.Claude.Home == "" && !o.demo {
			return nil, missing("the Claude Code home", "--claude-home", "CLAUDE_CONFIG_DIR", "claude.home")
		}
		a := claude.New(o.s.Claude.Home, o.claudeProfiles...)
		a.StaleAfter = o.s.Claude.StaleAfter.Duration
		a.DefaultHome = o.claudeDefaultHome
		for _, p := range o.s.Claude.Profiles {
			a.Profiles = append(a.Profiles, claude.Profile{Name: p.Name, Home: p.Home, Command: p.Command})
		}
		return a, nil
	}},
}

// options is what one run of agentflockr was asked to do.
type options struct {
	// s holds the settings in effect, after every layer is applied.
	s config.Settings
	// claudeProfiles are the Claude Code homes read beside the default one.
	claudeProfiles []string
	// claudeDefaultHome is ~/.claude, where Claude Code keeps its data when
	// CLAUDE_CONFIG_DIR is unset. Empty without a home folder.
	claudeDefaultHome string
	// settingsFile is the file read, or looked for; found says which.
	settingsFile string
	found        bool
	demo         bool
	version      bool
	printConfig  bool
}

// Main runs agentflockr with args and returns its exit status.
func Main(args []string, stdout, stderr io.Writer) int {
	return exitCode(run(args, stdout, stderr), stderr)
}

// exitCode reports err once and returns the process status: 2 for a usage
// error, as flag parsers conventionally do, and 1 for any other failure.
func exitCode(err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errReported):
		return 2
	}
	fmt.Fprintln(stderr, "agentflockr:", err)
	return 1
}

func run(args []string, stdout, stderr io.Writer) error {
	o, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.version {
		fmt.Fprintln(stdout, "agentflockr "+buildinfo.Version)
		return nil
	}
	providers, err := registry(o)
	if err != nil {
		return err
	}
	if o.printConfig {
		return printSettings(stdout, o, providers)
	}
	engine := &app.Engine{Demo: o.demo, Providers: providers}
	if !o.demo {
		engine.Store, err = store.Open(filepath.Join(o.s.DataDir, "sessions.db"))
		if err != nil {
			return err
		}
		defer engine.Store.Close()
	}
	snapshot, err := engine.Cached()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ui := tui.New(ctx, engine, tui.Options{Refresh: o.s.Refresh.Duration, RecentWindow: o.s.RecentWindow.Duration}, snapshot)
	_, err = tea.NewProgram(ui, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// parseOptions reads the flags, then applies the settings in four layers,
// each over the one before: built-in defaults, the settings file,
// environment variables, flags. It returns flag.ErrHelp after printing
// usage, and errReported after the flag package printed a parse error, both
// to output.
func parseOptions(args []string, output io.Writer) (options, error) {
	var o options
	var settingsFile, providers, dataDir, codexHome, claudeHome string
	var refresh time.Duration
	defaults := config.Default()
	flags := flag.NewFlagSet("agentflockr", flag.ContinueOnError)
	flags.SetOutput(output)
	// The folder flags default to empty and are filled after parsing, so
	// --version and --help need no home folder. Their help states the default.
	flags.StringVar(&settingsFile, "config", "", "settings file (default $AGENTFLOCKR_CONFIG, else\n$XDG_CONFIG_HOME/agentflockr/"+config.FileName+", else ~/.config/agentflockr/"+config.FileName+")")
	flags.StringVar(&dataDir, "data-dir", "", "where agentflockr keeps its session index\n(default $AGENTFLOCKR_DATA_DIR, else $XDG_DATA_HOME/agentflockr,\nelse ~/.local/share/agentflockr)")
	flags.StringVar(&codexHome, "codex-home", "", "Codex data directory (default $CODEX_HOME, else ~/.codex)")
	flags.StringVar(&claudeHome, "claude-home", "", "Claude Code data directory (default $CLAUDE_CONFIG_DIR, else ~/.claude\nand every ~/.claude-* with a projects folder)")
	flags.StringVar(&providers, "providers", strings.Join(agentNames(), ","), "comma-separated agents to discover")
	flags.DurationVar(&refresh, "refresh", defaults.Refresh.Duration, fmt.Sprintf("how often to look for changes (at least %v)", config.MinRefresh))
	flags.BoolVar(&o.printConfig, "print-config", false, "print the settings in effect, as a settings file, and exit")
	flags.BoolVar(&o.demo, "demo", false, "show sample sessions without reading any agent data")
	flags.BoolVar(&o.version, "version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `agentflockr: keep an eye on your coding agents' sessions

Usage:
  agentflockr [flags]

Settings can also be kept in the settings file, `+config.FileName+`, including
some with no flag; --print-config shows them all. Flags win over environment
variables, which win over the file.

Flags:`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err
		}
		return options{}, errReported
	}
	if o.version {
		return o, nil
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	// The settings file, over the built-in defaults. A file named with
	// --config or AGENTFLOCKR_CONFIG must exist; the default one may not.
	path, required := first(settingsFile, os.Getenv("AGENTFLOCKR_CONFIG")), true
	if path == "" {
		path, required = config.DefaultPath(home), false
	}
	o.settingsFile = expandHome(path, home)
	s, found, err := config.Load(o.settingsFile, required)
	if err != nil {
		return options{}, err
	}
	o.found = found

	// Environment variables, over the file.
	s.DataDir = first(os.Getenv("AGENTFLOCKR_DATA_DIR"), s.DataDir)
	s.Codex.Home = first(os.Getenv("CODEX_HOME"), s.Codex.Home)
	s.Claude.Home = first(os.Getenv("CLAUDE_CONFIG_DIR"), s.Claude.Home)

	// Flags given on the command line, over both.
	s.DataDir = first(dataDir, s.DataDir)
	s.Codex.Home = first(codexHome, s.Codex.Home)
	s.Claude.Home = first(claudeHome, s.Claude.Home)
	if set["providers"] {
		s.Providers = strings.Split(providers, ",")
	}
	if set["refresh"] {
		s.Refresh = config.D(refresh)
	}
	if err := s.Validate(); err != nil {
		return options{}, err
	}
	o.s = s
	if o.demo {
		// The demo reads no agent data and opens no index (REQ-CLI-007).
		return o, nil
	}
	return o, o.resolveFolders(home)
}

// resolveFolders fills the folders still empty from the home folder, as the
// help describes, and makes them absolute. Without a home folder, those
// defaults stay empty and fail where they are needed.
func (o *options) resolveFolders(home string) error {
	inHome := func(parts ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, parts...)...)
	}
	// The XDG spec says to ignore a relative XDG_DATA_HOME.
	dataHome := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataHome) {
		dataHome = inHome(".local", "share")
	}
	if dataHome != "" {
		dataHome = filepath.Join(dataHome, "agentflockr")
	}
	s := &o.s
	s.DataDir = expandHome(first(s.DataDir, dataHome), home)
	if s.DataDir == "" {
		return missing("where to keep the index", "--data-dir", "AGENTFLOCKR_DATA_DIR", "data-dir")
	}
	// A relative folder would put a different index in every folder
	// agentflockr is started from.
	var err error
	if s.DataDir, err = filepath.Abs(s.DataDir); err != nil {
		return err
	}
	// The index stores transcript paths found under these homes. A relative
	// home would store relative paths, which resume refuses and which would
	// resolve against whatever folder agentflockr runs from next.
	s.Codex.Home = expandHome(first(s.Codex.Home, inHome(".codex")), home)
	if s.Codex.Home, err = absolute(s.Codex.Home); err != nil {
		return err
	}
	// A chosen Claude Code home is read alone; the default one comes with the
	// profiles kept beside it (REQ-CLI-004).
	if s.Claude.Home == "" && home != "" {
		s.Claude.Home = filepath.Join(home, ".claude")
		o.claudeProfiles = claude.Profiles(home)
	}
	s.Claude.Home = expandHome(s.Claude.Home, home)
	if s.Claude.Home, err = absolute(s.Claude.Home); err != nil {
		return err
	}
	if home != "" {
		// Claude Code keeps its account file beside ~/.claude when
		// CLAUDE_CONFIG_DIR is unset, so new sessions in that home leave the
		// variable unset. Someone who runs Claude Code with the variable set
		// to ~/.claude on purpose keeps the account file inside it instead;
		// with no default home, their sessions get the variable, as in their
		// shell.
		defaultHome := filepath.Join(home, ".claude")
		if set := os.Getenv("CLAUDE_CONFIG_DIR"); set == "" || !session.SameDirectory(expandHome(set, home), defaultHome) {
			o.claudeDefaultHome = defaultHome
		}
	}
	// Profile paths are written as the person wrote them, with "~", and are
	// used as absolute paths. A command's executable gets the same "~".
	for i := range s.Claude.Profiles {
		p := &s.Claude.Profiles[i]
		// Abs also cleans, so "/Users/me/.claude/" becomes the default home's
		// own spelling and is recognized as it (see claude.Adapter.Start).
		if p.Home, err = filepath.Abs(expandHome(p.Home, home)); err != nil {
			return err
		}
		if len(p.Command) > 0 {
			p.Command[0] = expandHome(p.Command[0], home)
		}
	}
	return nil
}

// registry builds adapters in the order the settings list them, or every
// agent in the default order when they list none. Empty entries, from a
// doubled or trailing comma, are skipped.
func registry(o options) (provider.Registry, error) {
	names := o.s.Providers
	if names == nil {
		names = agentNames()
	}
	var r provider.Registry
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("agent %q is listed twice in the providers", name)
		}
		seen[name] = true
		a, ok := find(name)
		if !ok {
			return nil, fmt.Errorf("unknown agent %q; use %s", name, oneOf())
		}
		adapter, err := a.build(o)
		if err != nil {
			return nil, err
		}
		r = append(r, adapter)
	}
	if len(r) == 0 {
		return nil, fmt.Errorf("the providers name no agent; use %s", oneOf())
	}
	return r, nil
}

// printSettings writes the settings in effect as a settings file, which can
// be saved as agentflockr.toml to start one.
func printSettings(w io.Writer, o options, providers provider.Registry) error {
	s := o.s
	s.Providers = nil
	for _, p := range providers {
		s.Providers = append(s.Providers, string(p.ID()))
	}
	data, err := config.Encode(s)
	if err != nil {
		return err
	}
	state := "not found; built-in defaults"
	if o.found {
		state = "read"
	}
	fmt.Fprintf(w, "# agentflockr settings in effect.\n# Settings file: %s (%s)\n", first(o.settingsFile, "none"), state)
	if len(o.claudeProfiles) > 0 {
		fmt.Fprintf(w, "# Claude Code profiles also read: %s\n", strings.Join(o.claudeProfiles, ", "))
	}
	fmt.Fprintln(w)
	_, err = w.Write(data)
	return err
}

func find(name string) (agent, bool) {
	for _, a := range agents {
		if a.name == name {
			return a, true
		}
	}
	return agent{}, false
}

func agentNames() []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.name
	}
	return names
}

// oneOf lists the agents for a message: "opencode, codex or claude".
func oneOf() string {
	names := agentNames()
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// missing explains a folder that has no value and no home folder to default
// to, naming every way to set it.
func missing(what, flagName, variable, key string) error {
	return fmt.Errorf("cannot choose %s without a home folder. Use %s, %s, or %s in %s",
		what, flagName, variable, key, config.FileName)
}

// expandHome replaces a leading ~ with the home folder, since paths in the
// settings file don't pass through a shell.
func expandHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// absolute makes a folder absolute, leaving an empty one empty: Abs would turn
// it into the current folder, and an empty home means "none".
func absolute(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

// first returns the first non-empty value. An empty flag or variable counts
// as not set.
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
