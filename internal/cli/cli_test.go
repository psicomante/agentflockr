package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/config"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/codex"
	"github.com/psicomante/agentflockr/internal/session"
)

// isolate gives the test its own home folder and clears every variable
// parseOptions reads, so the real environment and settings file never leak in.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"AGENTFLOCKR_CONFIG", "XDG_CONFIG_HOME", "AGENTFLOCKR_DATA_DIR", "XDG_DATA_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
	return home
}

// settingsFile writes the default settings file in home.
func settingsFile(t *testing.T, home, text string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "agentflockr", config.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parse(t *testing.T, args ...string) (options, string, error) {
	t.Helper()
	var out bytes.Buffer
	o, err := parseOptions(args, &out)
	return o, out.String(), err
}

func TestPositionalArgumentsAreRefused(t *testing.T) {
	isolate(t)
	_, _, err := parse(t, "list")
	if err == nil || err.Error() != "unexpected arguments: [list]" {
		t.Fatalf("got %v", err)
	}
}

// The flag package prints a parse error with the usage; agentflockr must not
// print it a second time, and exits 2 as for any usage error.
func TestFlagErrorsAreReportedOnce(t *testing.T) {
	isolate(t)
	_, out, err := parse(t, "--bogus")
	if !errors.Is(err, errReported) {
		t.Fatalf("got %v, want errReported", err)
	}
	if n := strings.Count(out, "-bogus"); n != 1 {
		t.Fatalf("the error appears %d times:\n%s", n, out)
	}
	var stderr bytes.Buffer
	if code := exitCode(err, &stderr); code != 2 || stderr.Len() != 0 {
		t.Fatalf("exit %d, printed %q", code, stderr.String())
	}
	if code := exitCode(errors.New("boom"), &stderr); code != 1 || stderr.String() != "agentflockr: boom\n" {
		t.Fatalf("other errors: exit %d, printed %q", code, stderr.String())
	}
}

func TestVersionAndHelpNeedNoHomeFolder(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	if o, _, err := parse(t, "--version"); err != nil || !o.version {
		t.Fatalf("--version without HOME: %+v %v", o, err)
	}
	if _, out, err := parse(t, "--help"); !errors.Is(err, flag.ErrHelp) || !strings.Contains(out, "-data-dir") {
		t.Fatalf("--help without HOME: %v\n%s", err, out)
	}
	if _, _, err := parse(t); err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("starting without HOME must name the flag to set: %v", err)
	}
	if _, _, err := parse(t, "--demo"); err != nil {
		t.Fatalf("the demo needs no folders: %v", err)
	}
}

// The folder flags default to empty and are filled after parsing, so the
// help must say where each default comes from, variables included.
func TestHelpNamesEveryDefaultAndVariable(t *testing.T) {
	isolate(t)
	_, out, err := parse(t, "--help")
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	for _, want := range []string{config.FileName, "AGENTFLOCKR_CONFIG", "XDG_CONFIG_HOME",
		"AGENTFLOCKR_DATA_DIR", "XDG_DATA_HOME", "~/.local/share/agentflockr",
		"CODEX_HOME", "~/.codex", "CLAUDE_CONFIG_DIR", "~/.claude-*", "opencode,codex,claude", "at least 1s"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help does not mention %q:\n%s", want, out)
		}
	}
}

func TestMainPrintsTheVersion(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--version"}, &stdout, &stderr); code != 0 || !strings.HasPrefix(stdout.String(), "agentflockr ") || stderr.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestProvidersAreReadInOrderAndStrictly(t *testing.T) {
	agents := func(list string) ([]session.Provider, error) {
		o := options{s: config.Settings{Providers: strings.Split(list, ","),
			Codex: config.Agent{Home: "/c"}, Claude: config.Claude{Home: "/h"}}}
		r, err := registry(o)
		var ids []session.Provider
		for _, a := range r {
			ids = append(ids, a.ID())
		}
		return ids, err
	}
	for list, want := range map[string][]session.Provider{
		"codex,claude":       {session.Codex, session.Claude},
		" claude , opencode": {session.Claude, session.OpenCode},
		"codex,,claude,":     {session.Codex, session.Claude},
	} {
		if got, err := agents(list); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v %v, want %v", list, got, err, want)
		}
	}
	for list, message := range map[string]string{
		"claude, nope":  `unknown agent "nope"`,
		"claude,claude": `agent "claude" is listed twice`,
		" , ":           "name no agent",
	} {
		if _, err := agents(list); err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("%q: got %v, want %q", list, err, message)
		}
	}
}

func TestIndexFolderPrecedenceAndAbsolutePath(t *testing.T) {
	home := isolate(t)
	dataDir := func(args ...string) string {
		t.Helper()
		o, _, err := parse(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		return o.s.DataDir
	}
	if got, want := dataDir(), filepath.Join(home, ".local", "share", "agentflockr"); got != want {
		t.Errorf("default: %s, want %s", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "relative/share")
	if got, want := dataDir(), filepath.Join(home, ".local", "share", "agentflockr"); got != want {
		t.Errorf("a relative XDG_DATA_HOME must be ignored: %s", got)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if got, want := dataDir(), filepath.Join(xdg, "agentflockr"); got != want {
		t.Errorf("XDG_DATA_HOME: %s, want %s", got, want)
	}
	t.Setenv("AGENTFLOCKR_DATA_DIR", "/from/env")
	if got := dataDir(); got != "/from/env" {
		t.Errorf("AGENTFLOCKR_DATA_DIR must win over XDG_DATA_HOME: %s", got)
	}
	if got := dataDir("--data-dir", "/from/flag"); got != "/from/flag" {
		t.Errorf("--data-dir must win over the environment: %s", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dataDir("--data-dir", "rel"), filepath.Join(wd, "rel"); got != want {
		t.Errorf("a relative --data-dir must become absolute: %s, want %s", got, want)
	}
}

// A relative agent home would put relative transcript paths in the index,
// which resume refuses, so both homes are made absolute like the data folder.
func TestAgentHomesBecomeAbsolute(t *testing.T) {
	isolate(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := parse(t, "--claude-home", "fixtures/claude", "--codex-home", "fixtures/codex/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := o.s.Claude.Home, filepath.Join(wd, "fixtures", "claude"); got != want {
		t.Errorf("Claude Code home %q, want %q", got, want)
	}
	if got, want := o.s.Codex.Home, filepath.Join(wd, "fixtures", "codex"); got != want {
		t.Errorf("Codex home %q, want %q", got, want)
	}
}

func TestRefreshInterval(t *testing.T) {
	isolate(t)
	if o, _, err := parse(t); err != nil || o.s.Refresh.Duration != 5*time.Second {
		t.Fatalf("default: %v %v", o.s.Refresh, err)
	}
	if o, _, err := parse(t, "--refresh", "30s"); err != nil || o.s.Refresh.Duration != 30*time.Second {
		t.Fatalf("30s: %v %v", o.s.Refresh, err)
	}
	if _, _, err := parse(t, "--refresh", "500ms"); err == nil || !strings.Contains(err.Error(), "at least 1s") {
		t.Fatalf("500ms must be refused: %v", err)
	}
}

// Extra Claude Code profiles are found beside the default home, unless a home
// was chosen or the demo is running, which reads no agent data.
func TestClaudeProfilesBesideTheDefaultHome(t *testing.T) {
	home := isolate(t)
	for _, dir := range []string{".claude-work/projects", ".claude-empty"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	work := []string{filepath.Join(home, ".claude-work")}
	if o, _, err := parse(t); err != nil || !reflect.DeepEqual(o.claudeProfiles, work) {
		t.Fatalf("default: %v %v, want %v", o.claudeProfiles, err, work)
	}
	if o, _, _ := parse(t, "--claude-home", home); o.claudeProfiles != nil {
		t.Errorf("--claude-home must read that home only: %v", o.claudeProfiles)
	}
	if o, _, _ := parse(t, "--demo"); o.claudeProfiles != nil {
		t.Errorf("the demo must not look for Claude Code homes: %v", o.claudeProfiles)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	if o, _, _ := parse(t); o.claudeProfiles != nil {
		t.Errorf("CLAUDE_CONFIG_DIR must read that home only: %v", o.claudeProfiles)
	}
}

// Each layer wins over the one before: defaults, the settings file,
// environment variables, flags.
func TestSettingsFileSitsBetweenDefaultsAndTheEnvironment(t *testing.T) {
	home := isolate(t)
	settingsFile(t, home, `
providers = ["claude", "codex"]
refresh = "10s"
data-dir = "~/from-file"
recent-window = "48h"

[codex]
home = "~/codex-from-file"
stale-after = "10m"

[claude]
home = "~/claude-from-file"
`)
	o, _, err := parse(t)
	if err != nil {
		t.Fatal(err)
	}
	if !o.found || o.s.Refresh.Duration != 10*time.Second || o.s.RecentWindow.Duration != 48*time.Hour ||
		o.s.DataDir != filepath.Join(home, "from-file") || o.s.Codex.Home != filepath.Join(home, "codex-from-file") {
		t.Fatalf("the file was not applied, or ~ not expanded: %+v", o)
	}
	if o.claudeProfiles != nil {
		t.Errorf("a Claude Code home chosen in the file must be read alone: %v", o.claudeProfiles)
	}
	t.Setenv("CODEX_HOME", "/codex-from-env")
	o, _, err = parse(t, "--refresh", "20s", "--data-dir", "/from-flag")
	if err != nil {
		t.Fatal(err)
	}
	if o.s.Codex.Home != "/codex-from-env" || o.s.Refresh.Duration != 20*time.Second || o.s.DataDir != "/from-flag" {
		t.Fatalf("variables and flags must win over the file: %+v", o.s)
	}
	if o.s.Claude.Home != filepath.Join(home, "claude-from-file") {
		t.Errorf("what no layer above changes keeps the file's value: %s", o.s.Claude.Home)
	}
	r, err := registry(o)
	if err != nil || len(r) != 2 || r[0].ID() != session.Claude {
		t.Fatalf("the file's providers were not used: %v %v", r, err)
	}
	if a, ok := r[1].(*codex.Adapter); !ok || a.StaleAfter != 10*time.Minute {
		t.Errorf("codex.stale-after did not reach the adapter: %+v", r[1])
	}
}

func TestSettingsFileProblemsStopStartup(t *testing.T) {
	home := isolate(t)
	settingsFile(t, home, `refesh = "10s"`)
	var stdout, stderr bytes.Buffer
	if code := Main(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), `unknown setting "refesh"`) {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	missing := filepath.Join(home, "nowhere.toml")
	if _, _, err := parse(t, "--config", missing); err == nil {
		t.Error("a settings file named with --config must exist")
	}
	t.Setenv("AGENTFLOCKR_CONFIG", missing)
	if _, _, err := parse(t); err == nil {
		t.Error("a settings file named with AGENTFLOCKR_CONFIG must exist")
	}
}

// --print-config writes the settings in effect, and its output is itself a
// settings file.
func TestPrintConfigShowsTheSettingsInEffect(t *testing.T) {
	home := isolate(t)
	path := settingsFile(t, home, "[codex]\nstale-after = \"10m\"\n")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--print-config", "--providers", "codex"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, path+" (read)") || !strings.Contains(out, "10m") {
		t.Fatalf("output does not name the file or its value:\n%s", out)
	}
	saved := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(saved, stdout.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, _, err := config.Load(saved, true)
	if err != nil || s.Codex.StaleAfter.Duration != 10*time.Minute || strings.Join(s.Providers, ",") != "codex" {
		t.Fatalf("the output does not read back: %+v %v\n%s", s, err, out)
	}
}

// Claude Code profiles from the settings file reach the adapter with "~"
// expanded and their homes absolute, and --print-config writes them back.
func TestClaudeProfilesReachTheAdapter(t *testing.T) {
	home := isolate(t)
	settingsFile(t, home, `
[[claude.profiles]]
name = "work"
home = "~/.claude-work"
command = ["~/bin/claude-work", "--verbose"]

[[claude.profiles]]
name = "plain"
home = "~/.claude-plain"
`)
	o, _, err := parse(t)
	if err != nil {
		t.Fatal(err)
	}
	profiles := o.s.Claude.Profiles
	if len(profiles) != 2 || profiles[0].Home != filepath.Join(home, ".claude-work") ||
		!reflect.DeepEqual(profiles[0].Command, []string{filepath.Join(home, "bin", "claude-work"), "--verbose"}) ||
		profiles[1].Command != nil {
		t.Fatalf("profiles not expanded as written: %+v", profiles)
	}
	if o.claudeDefaultHome != filepath.Join(home, ".claude") {
		t.Errorf("default home %q", o.claudeDefaultHome)
	}

	providers, err := registry(o)
	if err != nil {
		t.Fatal(err)
	}
	starter, ok := providers.Get(session.Claude).(provider.Starter)
	if !ok {
		t.Fatal("the Claude Code adapter does not start sessions")
	}
	listed, err := starter.StartProfiles(context.Background())
	if err != nil || len(listed) != 2 || listed[0].ID != "work" || listed[1].ID != "plain" {
		t.Fatalf("adapter profiles: %+v %v", listed, err)
	}

	var out bytes.Buffer
	if code := Main([]string{"--print-config"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("--print-config exited %d", code)
	}
	text := out.String()
	for _, want := range []string{"[[claude.profiles]]", "name = 'work'", filepath.Join(home, ".claude-work")} {
		if !strings.Contains(text, want) {
			t.Errorf("--print-config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "~/") {
		t.Errorf("--print-config wrote an unexpanded path:\n%s", text)
	}
}

// A profile home written as an absolute path is cleaned like one written with
// "~", so the default home with a trailing slash matches the default home.
func TestClaudeProfileHomesAreCleaned(t *testing.T) {
	home := isolate(t)
	settingsFile(t, home, "[[claude.profiles]]\nname = \"default\"\nhome = \""+home+"/.claude/\"\n")
	o, _, err := parse(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := o.s.Claude.Profiles[0].Home; got != o.claudeDefaultHome {
		t.Fatalf("profile home %q, want the default home %q", got, o.claudeDefaultHome)
	}
}

// Someone who runs Claude Code with CLAUDE_CONFIG_DIR set to ~/.claude keeps
// that choice: new sessions get the variable, as in their shell, instead of
// having it unset for the default home.
func TestAnInheritedDefaultConfigDirIsKept(t *testing.T) {
	home := isolate(t)
	o, _, err := parse(t)
	if err != nil || o.claudeDefaultHome != filepath.Join(home, ".claude") {
		t.Fatalf("without the variable: %q %v", o.claudeDefaultHome, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")+"/")
	if o, _, err = parse(t); err != nil || o.claudeDefaultHome != "" {
		t.Fatalf("with the variable at the default home: %q %v, want no default home", o.claudeDefaultHome, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-work"))
	if o, _, err = parse(t); err != nil || o.claudeDefaultHome != filepath.Join(home, ".claude") {
		t.Fatalf("with the variable elsewhere: %q %v", o.claudeDefaultHome, err)
	}
}
