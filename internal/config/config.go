// Package config holds agentflockr's settings: their defaults, and reading
// them from agentflockr.toml, the settings file. internal/cli applies
// environment variables and flags over what is read here.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/tui"
)

// FileName is the settings file's name, in agentflockr's config folder.
const FileName = "agentflockr.toml"

// MinRefresh is the shortest refresh interval accepted (REQ-CLI-006).
const MinRefresh = time.Second

// Settings is everything a person can set. Its TOML keys are the file's
// keys; most settings are also flags, under the same name.
type Settings struct {
	// Providers lists the agents to watch, in display order. Empty means
	// every agent, in the default order.
	Providers []string `toml:"providers,omitempty"`
	// Refresh is how often to look for changes.
	Refresh Duration `toml:"refresh"`
	// DataDir is where the index lives. Empty means the default folder.
	DataDir string `toml:"data-dir,omitempty"`
	// RecentWindow separates RECENT from EARLIER.
	RecentWindow Duration `toml:"recent-window"`
	Codex        Agent    `toml:"codex"`
	Claude       Claude   `toml:"claude"`
}

// Agent holds the settings of an agent read from its own folder.
type Agent struct {
	// Home is the agent's data folder. Empty means its default.
	Home string `toml:"home,omitempty"`
	// StaleAfter is how long a transcript's working or waiting signal
	// counts without a newer record (REQ-DSC-006).
	StaleAfter Duration `toml:"stale-after"`
}

// Claude holds the Claude Code settings. Profiles are kept apart from Agent
// because only Claude Code has them; Codex would ignore the key.
type Claude struct {
	// Home is the Claude Code data folder. Empty means its default.
	Home string `toml:"home,omitempty"`
	// StaleAfter is how long a transcript's working or waiting signal
	// counts without a newer record (REQ-DSC-006).
	StaleAfter Duration `toml:"stale-after"`
	// Profiles are the launch contexts offered for new sessions, in file
	// order (REQ-CLI-010). Empty means one per Claude Code home agentflockr
	// reads, started with the claude command.
	Profiles []ClaudeProfile `toml:"profiles,omitempty"`
}

// ClaudeProfile is one [[claude.profiles]] entry. Command is a list so that
// no shell text is ever run; nil means ["claude"].
type ClaudeProfile struct {
	Name    string   `toml:"name"`
	Home    string   `toml:"home"`
	Command []string `toml:"command,omitempty"`
}

// maxProfileName is the longest profile name the form can show in full.
const maxProfileName = 64

// Default returns the built-in settings. Each value is owned by the package
// that uses it; this only gathers them.
func Default() Settings {
	return Settings{
		Refresh:      D(5 * time.Second),
		RecentWindow: D(tui.DefaultRecentWindow),
		Codex:        Agent{StaleAfter: D(provider.DefaultStaleAfter)},
		Claude:       Claude{StaleAfter: D(provider.DefaultStaleAfter)},
	}
}

// Validate refuses values no part of agentflockr can work with.
func (s Settings) Validate() error {
	if s.Refresh.Duration < MinRefresh {
		return fmt.Errorf("refresh must be at least %v", MinRefresh)
	}
	if s.RecentWindow.Duration <= 0 {
		return fmt.Errorf("recent-window must be longer than 0s")
	}
	for _, a := range []struct {
		name       string
		staleAfter Duration
	}{{"codex", s.Codex.StaleAfter}, {"claude", s.Claude.StaleAfter}} {
		if a.staleAfter.Duration <= 0 {
			return fmt.Errorf("%s.stale-after must be longer than 0s", a.name)
		}
	}
	return s.Claude.validateProfiles()
}

// validateProfiles refuses a profile that could not start, naming it so the
// person knows which entry to fix. Load adds the file name.
func (c Claude) validateProfiles() error {
	seen := map[string]bool{}
	for _, p := range c.Profiles {
		switch {
		case p.Name == "":
			return fmt.Errorf("claude.profiles: a profile has an empty name")
		case utf8.RuneCountInString(p.Name) > maxProfileName:
			return fmt.Errorf("claude profile %q: the name is longer than %d characters", p.Name, maxProfileName)
		case seen[p.Name]:
			return fmt.Errorf("claude profile %q: the name is used twice", p.Name)
		case p.Home == "":
			return fmt.Errorf("claude profile %q: home is empty", p.Name)
		}
		seen[p.Name] = true
		// nil means the key was left out, which is the default command; an
		// empty list was written out and is refused.
		if p.Command == nil {
			continue
		}
		if len(p.Command) == 0 {
			return fmt.Errorf("claude profile %q: command is empty", p.Name)
		}
		executable := p.Command[0]
		switch {
		case strings.TrimSpace(executable) == "":
			return fmt.Errorf("claude profile %q: command names no executable", p.Name)
		case strings.Contains(executable, "="):
			return fmt.Errorf("claude profile %q: command starts with a variable assignment; start the command with the executable and set the home with home, not CLAUDE_CONFIG_DIR=", p.Name)
		case relativePath(executable):
			return fmt.Errorf("claude profile %q: command %q is a relative path; use a name on PATH, an absolute path or one starting with ~/", p.Name, executable)
		}
	}
	return nil
}

// relativePath reports whether an executable is a path that is neither
// absolute nor under the home folder, such as "bin/claude-work". The agent
// starts with the project folder as its working folder, and Go resolves a
// relative path against that folder, so such a command would run a file from
// the project's own tree. A bare name is looked up on PATH instead, and
// internal/cli expands "~/".
func relativePath(executable string) bool {
	return strings.ContainsRune(executable, '/') && !filepath.IsAbs(executable) && !strings.HasPrefix(executable, "~/")
}

// DefaultPath is where the settings file is looked for when none is named:
// $XDG_CONFIG_HOME/agentflockr/agentflockr.toml, else
// ~/.config/agentflockr/agentflockr.toml. It is empty when neither is known.
func DefaultPath(home string) string {
	// The XDG spec says to ignore a relative XDG_CONFIG_HOME.
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "agentflockr", FileName)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "agentflockr", FileName)
}

// Load reads the settings file at path over the defaults. A missing file is
// not an error unless required: found is false and the defaults stand. A
// file with an unknown key or a bad value is refused, naming the problem.
func Load(path string, required bool) (s Settings, found bool, err error) {
	s = Default()
	if path == "" {
		return s, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return s, false, nil
	}
	if err != nil {
		return s, false, fmt.Errorf("settings file: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, true, describe(path, err)
	}
	if err := s.Validate(); err != nil {
		return s, true, fmt.Errorf("%s: %w", path, err)
	}
	return s, true, nil
}

// describe turns a decoding error into one line that names the file, and
// the key or position at fault.
func describe(path string, err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		var keys []string
		for _, e := range strict.Errors {
			keys = append(keys, fmt.Sprintf("%q", strings.Join(e.Key(), ".")))
		}
		return fmt.Errorf("%s: unknown setting %s", path, strings.Join(keys, ", "))
	}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		row, column := decode.Position()
		message := strings.TrimPrefix(decode.Error(), "toml: ")
		// go-toml names Go types here; say which key has the wrong type.
		if strings.HasPrefix(message, "cannot decode TOML") {
			message = fmt.Sprintf("wrong type for %q; see agentflockr --help", strings.Join(decode.Key(), "."))
		}
		return fmt.Errorf("%s:%d:%d: %s", path, row, column, message)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// Encode writes s as a settings file.
func Encode(s Settings) ([]byte, error) { return toml.Marshal(s) }

// Duration is a time.Duration written as text, such as "5s" or "10m". It is
// a struct so that a bare number, which go-toml would read as nanoseconds,
// is refused as the wrong type instead.
type Duration struct{ time.Duration }

// D makes a Duration.
func D(d time.Duration) Duration { return Duration{d} }

// MarshalText writes the short form people type: "24h" and "10m" rather
// than Go's "24h0m0s" and "10m0s".
func (d Duration) MarshalText() ([]byte, error) {
	text := d.String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return []byte(text), nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as \"5s\" or \"10m\"", text)
	}
	d.Duration = v
	return nil
}
