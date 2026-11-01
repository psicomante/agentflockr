package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type Provider string

const (
	OpenCode Provider = "opencode"
	Codex    Provider = "codex"
	Claude   Provider = "claude"
)

type Status string

const (
	Working   Status = "working"
	Waiting   Status = "waiting"
	Completed Status = "completed"
	Idle      Status = "idle"
	Failed    Status = "failed"
	Unknown   Status = "unknown"
)

type Session struct {
	ID                string    `json:"id"`
	Provider          Provider  `json:"provider"`
	ProjectID         string    `json:"project_id"`
	Directory         string    `json:"directory"`
	OriginalDirectory string    `json:"original_directory"`
	Title             string    `json:"title"`
	Summary           string    `json:"summary"`
	Status            Status    `json:"status"`
	Evidence          string    `json:"evidence"`
	UpdatedAt         time.Time `json:"updated_at"`
	Transcript        string    `json:"transcript,omitempty"`
	Cached            bool      `json:"cached"`
	Missing           bool      `json:"missing_folder"`
	// Parent is the ID of the session that spawned this one, for providers
	// that store sub-agent runs as sessions of their own.
	Parent string `json:"parent_id,omitempty"`
	// Spawned marks a run that no person started: a sub-agent, or a job the
	// provider runs by itself, such as a review or memory consolidation.
	// Parent is set as well when the provider names the spawning session.
	Spawned bool `json:"spawned,omitempty"`
}

func (s Session) Key() string { return string(s.Provider) + ":" + s.ID }

// SplitKey is the inverse of Key.
func SplitKey(key string) (Provider, string, bool) {
	provider, id, ok := strings.Cut(key, ":")
	return Provider(provider), id, ok && provider != "" && id != ""
}

// Root reports whether a person started this session.
func (s Session) Root() bool { return s.Parent == "" && !s.Spawned }

func Priority(s Status) int {
	switch s {
	case Waiting:
		return 0
	case Failed:
		return 1
	case Working:
		return 2
	case Completed:
		return 3
	case Idle:
		return 4
	default:
		return 5
	}
}

// Text removes terminal escape sequences and control characters, then bounds the
// excerpt in runes. Provider text must never become terminal control input.
func Text(s string, limit int) string {
	return bound(strings.Join(strings.Fields(strip(s, false)), " "), limit)
}

// Block is Text for preformatted content such as command output or
// indented JSON: it removes the same sequences but keeps spaces and line
// breaks, expands tabs, trims trailing spaces and blank lines at either
// end, and bounds the result in runes.
func Block(s string, limit int) string {
	lines := strings.Split(strip(s, true), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return bound(strings.Join(trimBlank(lines), "\n"), limit)
}

// trimBlank drops the blank lines at either end, for Block and Lines.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// strip removes escape sequences and control characters. In a block, line
// breaks stay, tabs become four spaces and carriage returns go; otherwise
// all three become spaces.
func strip(s string, block bool) string {
	var b strings.Builder
	state := byte(0)
	for _, r := range s {
		switch state {
		case 1:
			switch r {
			case '[':
				state = 2
			case ']', 'P', '^', '_', 'X':
				state = 3
			default:
				state = 0
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				state = 0
			}
			continue
		case 3:
			if r == 7 || r == 0x9c {
				state = 0
			} else if r == 27 {
				state = 4
			}
			continue
		case 4:
			if r == '\\' {
				state = 0
			} else {
				state = 3
			}
			continue
		}
		if r == 27 {
			state = 1
			continue
		}
		if r == 0x9b {
			state = 2
			continue
		}
		if r == 0x9d {
			state = 3
			continue
		}
		if unicode.IsControl(r) {
			switch {
			case block && r == '\n':
				b.WriteByte('\n')
			case block && r == '\t':
				b.WriteString("    ")
			case block && r == '\r':
			case r == '\n' || r == '\t' || r == '\r':
				b.WriteByte(' ')
			}
			continue
		}
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Lines is Text for content whose line breaks matter, such as a command or
// a message: each line is cleaned like Text, blank lines at either end are
// dropped, and the result is bounded in runes.
func Lines(s string, limit int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = Text(line, 0)
	}
	return bound(strings.Join(trimBlank(lines), "\n"), limit)
}

func bound(s string, limit int) string {
	r := []rune(s)
	if limit > 0 && len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// Moved reports whether the session's project folder differs from the one the
// agent recorded. A symlink alias or an unclean spelling of the same folder is
// not a move, so the files are compared when the cleaned paths differ.
func Moved(s Session) bool {
	return !SameDirectory(s.Directory, s.OriginalDirectory)
}

// SameDirectory reports whether two paths name the same folder.
func SameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && os.SameFile(left, right)
}
