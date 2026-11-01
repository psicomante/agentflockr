// Package claude integrates Claude Code sessions from local project transcripts.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/jsonl"
	"github.com/psicomante/agentflockr/internal/session"
)

type Adapter struct {
	Home       string
	ExtraHomes []string
	// Profiles are the configured launch contexts (REQ-CLI-010). Empty means
	// one profile per home the adapter reads.
	Profiles []Profile
	// DefaultHome is the folder Claude Code uses when CLAUDE_CONFIG_DIR is
	// unset. Starting in it leaves the variable unset (see Start).
	DefaultHome string
	// StaleAfter is how long a transcript's live signal counts; zero means
	// provider.DefaultStaleAfter. Set it before the first Scan.
	StaleAfter time.Duration
	files      *jsonl.Source
}

func New(home string, extraHomes ...string) *Adapter {
	return &Adapter{Home: home, ExtraHomes: extraHomes}
}

// homes lists every folder that holds sessions: Home, the extra homes, and
// the profiles' homes. A folder named twice is read once, so a session
// started with a profile whose home is also Home is listed once.
func (a *Adapter) homes() []string {
	var homes []string
	seen := map[string]bool{}
	add := func(home string) {
		if home == "" || seen[filepath.Clean(home)] {
			return
		}
		seen[filepath.Clean(home)] = true
		homes = append(homes, home)
	}
	add(a.Home)
	for _, home := range a.ExtraHomes {
		add(home)
	}
	for _, p := range a.Profiles {
		add(p.Home)
	}
	return homes
}

func (a *Adapter) ID() session.Provider { return session.Claude }

func (a *Adapter) Meta() provider.Meta {
	return provider.Meta{Name: "Claude", Title: "Claude Code", Short: "CC", Source: "Claude Code: local project transcripts",
		DeleteScope: "Deletes its local transcript and subagent files."}
}

// Scan is called by one scan worker at a time; the source keeps its parse
// cache between scans.
func (a *Adapter) Scan(ctx context.Context, now time.Time) provider.Result {
	if a.files == nil {
		var roots []string
		for _, home := range a.homes() {
			roots = append(roots, filepath.Join(home, "projects"))
		}
		// Transcripts sit directly in each project folder; deeper folders hold
		// subagent artifacts that are not separate sessions.
		a.files = &jsonl.Source{
			Provider:   session.Claude,
			Roots:      roots,
			MaxDepth:   1,
			StaleAfter: a.StaleAfter,
			Apply:      apply,
		}
	}
	result := a.files.Scan(ctx, now)
	if result.Err == nil {
		a.applyLive(&result)
	}
	return result
}

// Resume prefers the transcript path, which identifies the session even when
// Claude Code would look in a different project folder for the bare ID.
func (a *Adapter) Resume(_ context.Context, s session.Session) (provider.Command, error) {
	if err := provider.CheckResumable(s); err != nil {
		return provider.Command{}, err
	}
	ref := s.ID
	if s.Transcript != "" {
		if !filepath.IsAbs(s.Transcript) {
			return provider.Command{}, fmt.Errorf("transcript path must be absolute")
		}
		ref = s.Transcript
	}
	return provider.Command{Name: "claude", Args: []string{"--resume", ref}, Directory: s.Directory}, nil
}

func (a *Adapter) MatchesProcess(args []string, s session.Session) bool {
	args, ok := provider.ToolArgs(args, session.Claude)
	if !ok {
		return false
	}
	value, ok := provider.FlagValue(args, "--session", "-s", "--resume", "-r", "--session-id")
	return ok && (value == s.ID || (s.Transcript != "" && value == s.Transcript))
}

// Parse reads one transcript from the beginning.
func Parse(path string) (session.Session, error) {
	return jsonl.Parse(path, session.Claude, apply)
}

type record struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	Message     struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
}

func apply(s *session.Session, line []byte) error {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return err
	}
	if r.IsSidechain {
		return nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil && ts.After(s.UpdatedAt) {
		s.UpdatedAt = ts
	}
	if r.SessionID != "" {
		s.ID = r.SessionID
	}
	// Claude Code records the folder it is in at each record, and a session
	// that changes into a subfolder records that one afterwards. The project
	// is the folder the session started in (where Claude Code files its
	// transcript), so the first one is kept.
	if r.CWD != "" && s.OriginalDirectory == "" {
		s.Directory, s.OriginalDirectory = r.CWD, r.CWD
	}
	if r.CustomTitle != "" {
		s.Title = session.Text(r.CustomTitle, 100)
	}
	if r.AITitle != "" {
		s.Title = session.Text(r.AITitle, 100)
	}
	if r.IsMeta {
		return nil
	}
	var blocks []provider.Block
	var text string
	if json.Unmarshal(r.Message.Content, &text) != nil {
		_ = json.Unmarshal(r.Message.Content, &blocks)
		text = provider.BlockText(blocks)
	}
	switch r.Type {
	case "user":
		if s.Title == "" && text != "" {
			s.Title = session.Text(text, 100)
		}
		provider.SetState(s, session.Working, "Transcript: user message or tool result")
	case "assistant":
		if text != "" {
			s.Summary = session.Text(text, 600)
		}
		provider.SetState(s, session.Working, "Transcript: assistant activity")
		if r.Message.StopReason == "end_turn" || r.Message.StopReason == "stop_sequence" {
			provider.SetState(s, session.Completed, "Transcript: last turn completed")
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "AskUserQuestion" {
				provider.SetState(s, session.Waiting, "Transcript: user question requested")
			}
		}
	case "system":
		if r.Subtype == "turn_duration" {
			provider.SetState(s, session.Completed, "Transcript: last turn completed")
		}
	}
	return nil
}
