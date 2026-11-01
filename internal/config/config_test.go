package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileOverridesOnlyWhatItNames(t *testing.T) {
	path := write(t, `
# Codex runs long test suites here.
providers = ["codex", "claude"]
refresh = "10s"

[codex]
home = "~/work/.codex"
stale-after = "10m"
`)
	s, found, err := Load(path, false)
	if err != nil || !found {
		t.Fatalf("found %v, err %v", found, err)
	}
	want := Default()
	want.Providers = []string{"codex", "claude"}
	want.Refresh = D(10 * time.Second)
	want.Codex = Agent{Home: "~/work/.codex", StaleAfter: D(10 * time.Minute)}
	if s.Refresh != want.Refresh || s.Codex != want.Codex || !reflect.DeepEqual(s.Claude, want.Claude) ||
		s.RecentWindow != want.RecentWindow || strings.Join(s.Providers, ",") != "codex,claude" {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}

func TestMissingFileKeepsTheDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if s, found, err := Load(path, false); err != nil || found || s.Refresh != Default().Refresh {
		t.Fatalf("got %+v %v %v", s, found, err)
	}
	if _, _, err := Load(path, true); err == nil {
		t.Fatal("a settings file named explicitly must exist")
	}
}

// A typo or a bad value stops agentflockr with the file and the key, rather
// than running on half the settings the person wrote.
func TestBadFilesAreRefusedWithTheKeyAtFault(t *testing.T) {
	for text, want := range map[string]string{
		`refesh = "5s"`:                      `unknown setting "refesh"`,
		"[codex]\nstale-afte = \"1m\"":       `unknown setting "codex.stale-afte"`,
		`refresh = "soon"`:                   `"soon" is not a duration`,
		`refresh = "500ms"`:                  "refresh must be at least 1s",
		`recent-window = "0s"`:               "recent-window must be longer than 0s",
		"[claude]\nstale-after = \"-1m\"":    "claude.stale-after must be longer than 0s",
		`providers = "codex"`:                `wrong type for "providers"`,
		`refresh = 5`:                        `"5" is not a duration`,
		"refresh = \"5s\"\nrefresh = \"6s\"": FileName,
	} {
		_, _, err := Load(write(t, text), false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

func TestDefaultPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := DefaultPath("/home/a"), "/home/a/.config/agentflockr/agentflockr.toml"; got != want {
		t.Errorf("default: %s, want %s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if got, want := DefaultPath("/home/a"), "/home/a/.config/agentflockr/agentflockr.toml"; got != want {
		t.Errorf("a relative XDG_CONFIG_HOME must be ignored: %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := DefaultPath(""), "/xdg/agentflockr/agentflockr.toml"; got != want {
		t.Errorf("XDG_CONFIG_HOME: %s, want %s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultPath(""); got != "" {
		t.Errorf("no home and no XDG_CONFIG_HOME: %q, want none", got)
	}
}

// What --print-config writes can be read back as a settings file.
func TestEncodedSettingsReadBack(t *testing.T) {
	s := Default()
	s.Providers = []string{"claude"}
	s.Codex.StaleAfter = D(10 * time.Minute)
	s.Claude.Profiles = []ClaudeProfile{
		{Name: "work", Home: "~/.claude-work", Command: []string{"claude", "--verbose"}},
		{Name: "plain", Home: "~/.claude-plain"},
	}
	data, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := Load(write(t, string(data)), true)
	if err != nil || back.Codex != s.Codex || back.Refresh != s.Refresh || strings.Join(back.Providers, ",") != "claude" ||
		!reflect.DeepEqual(back.Claude.Profiles, s.Claude.Profiles) {
		t.Fatalf("got %+v %v from:\n%s", back, err, data)
	}
}

// Each refusal names the profile at fault, and the file is named by Load.
func TestClaudeProfilesAreValidated(t *testing.T) {
	for text, want := range map[string]string{
		"[[claude.profiles]]\nname = \"\"\nhome = \"~/a\"":                                                           "a profile has an empty name",
		"[[claude.profiles]]\nname = \"work\"\nhome = \"\"":                                                          `claude profile "work": home is empty`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\n[[claude.profiles]]\nname = \"work\"\nhome = \"~/b\"": `claude profile "work": the name is used twice`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = []":                                         `claude profile "work": command is empty`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"\"]":                                     `claude profile "work": command names no executable`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"CLAUDE_CONFIG_DIR=~/x\", \"claude\"]":    `claude profile "work": command starts with a variable assignment`,
		"[[claude.profiles]]\nname = \"" + strings.Repeat("n", 65) + "\"\nhome = \"~/a\"":                            "the name is longer than 64 characters",
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"bin/claude-work\"]":                      `claude profile "work": command "bin/claude-work" is a relative path`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"./claude-work\"]":                        `claude profile "work": command "./claude-work" is a relative path`,
		"[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"  \"]":                                   `claude profile "work": command names no executable`,
	} {
		_, _, err := Load(write(t, text), false)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), FileName) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	// A valid profile with the default command is accepted, and its
	// command stays nil so the adapter can tell it was left out.
	s, _, err := Load(write(t, "[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\""), false)
	if err != nil || len(s.Claude.Profiles) != 1 || s.Claude.Profiles[0].Command != nil {
		t.Fatalf("valid profile: %+v %v", s.Claude.Profiles, err)
	}
	// A name on PATH, an absolute path and one under the home folder are
	// not relative to the project folder, so they are accepted.
	for _, command := range []string{"claude-work", "/usr/local/bin/claude-work", "~/bin/claude-work"} {
		text := "[[claude.profiles]]\nname = \"work\"\nhome = \"~/a\"\ncommand = [\"" + command + "\"]"
		if _, _, err := Load(write(t, text), false); err != nil {
			t.Errorf("command %q: %v", command, err)
		}
	}
}

func TestDurationsAreWrittenInShortForm(t *testing.T) {
	for d, want := range map[time.Duration]string{24 * time.Hour: "24h", 10 * time.Minute: "10m",
		90 * time.Minute: "1h30m", 5 * time.Second: "5s", 1500 * time.Millisecond: "1.5s"} {
		if got, _ := D(d).MarshalText(); string(got) != want {
			t.Errorf("%v: %s, want %s", d, got, want)
		}
	}
}
