package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

var nativeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidID rejects IDs that could be read as options or paths by a native CLI.
func ValidID(id string) bool { return nativeID.MatchString(id) }

// CheckResumable validates what every native launch needs before any provider
// side effect: a safe session ID and an existing absolute project directory.
func CheckResumable(s session.Session) error {
	if !ValidID(s.ID) {
		return fmt.Errorf("invalid session ID")
	}
	return CheckFolder(s.Directory)
}

// ErrMissingFolder is wrapped by CheckFolder's error for a folder that no
// longer exists, so the dashboard can add how to fix it without looking at
// the file system itself.
var ErrMissingFolder = errors.New("the project folder no longer exists")

// CheckDeletable checks what every native deletion needs before it touches
// anything: a live context and a session ID that cannot be read as an option
// or a path. Each Deleter starts with it, as each Resume starts with
// CheckResumable, so a new agent cannot forget either check.
func CheckDeletable(ctx context.Context, s session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidID(s.ID) {
		return fmt.Errorf("invalid native session ID")
	}
	return nil
}

// CheckFolder requires an absolute path to an existing folder. Opening and
// starting share it, so both refuse a missing folder with the same message.
func CheckFolder(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("project directory must be absolute")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrMissingFolder, session.Text(dir, 200))
	}
	return nil
}

// ToolArgs returns the arguments after the tool's executable when one of the
// first three argv entries names it (allowing wrappers such as node or env).
func ToolArgs(args []string, tool session.Provider) ([]string, bool) {
	for i := 0; i < min(3, len(args)); i++ {
		if filepath.Base(args[i]) == string(tool) {
			return args[i+1:], true
		}
	}
	return nil, false
}

// FlagValue returns the value following the first of flags present in args.
func FlagValue(args []string, flags ...string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		for _, flag := range flags {
			if args[i] == flag {
				return args[i+1], true
			}
		}
	}
	return "", false
}

type Block struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// BlockText joins visible text blocks, excluding reasoning and tool content.
func BlockText(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "input_text" || b.Type == "output_text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

func SetState(s *session.Session, status session.Status, evidence string) {
	s.Status, s.Evidence = status, evidence
}

// DefaultStaleAfter is how long a transcript's working or waiting signal
// counts without a newer record (REQ-DSC-006). Each agent's stale-after
// setting can change it.
const DefaultStaleAfter = 2 * time.Minute

// Observe fills a parsed session's display fallbacks. A transcript that last
// showed live activity more than staleAfter ago cannot prove the turn is
// still live; zero means DefaultStaleAfter.
func Observe(s session.Session, now time.Time, staleAfter time.Duration) session.Session {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	if (s.Status == session.Working || s.Status == session.Waiting) && now.Sub(s.UpdatedAt) > staleAfter {
		SetState(&s, session.Unknown, "Stale transcript signal; process and pending input are unverified")
	}
	if s.Title == "" {
		s.Title = filepath.Base(s.Directory) + " session"
	}
	if s.Summary == "" {
		s.Summary = s.Title
	}
	if s.Evidence == "" {
		s.Evidence = "No recognized turn lifecycle evidence"
	}
	return s
}
