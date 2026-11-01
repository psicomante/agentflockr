// Package codex integrates Codex CLI sessions from local rollout files.
package codex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/jsonl"
	"github.com/psicomante/agentflockr/internal/session"
)

type Adapter struct {
	Home string
	// StaleAfter is how long a transcript's live signal counts; zero means
	// provider.DefaultStaleAfter. Set it before the first Scan.
	StaleAfter time.Duration
	// Run executes native deletion. Nil runs codex with CODEX_HOME set to Home.
	Run provider.Runner
	// RunCommand reads the model catalogue, a command with its own
	// environment (CODEX_HOME). Nil runs it for real; tests substitute it.
	RunCommand func(context.Context, provider.Command) ([]byte, error)
	files      *jsonl.Source
}

func New(home string) *Adapter { return &Adapter{Home: home} }

func (a *Adapter) ID() session.Provider { return session.Codex }

func (a *Adapter) Meta() provider.Meta {
	return provider.Meta{Name: "Codex", Title: "Codex", Short: "CX", Source: "Codex: local rollout files",
		DeleteScope: "Permanently deletes this conversation through Codex."}
}

// Scan is called by one scan worker at a time; the source keeps its parse
// cache between scans.
func (a *Adapter) Scan(ctx context.Context, now time.Time) provider.Result {
	if a.files == nil {
		a.files = &jsonl.Source{
			Provider:   session.Codex,
			Roots:      []string{filepath.Join(a.Home, "sessions"), filepath.Join(a.Home, "archived_sessions")},
			StaleAfter: a.StaleAfter,
			Apply:      apply,
		}
	}
	return a.files.Scan(ctx, now)
}

func (a *Adapter) Resume(_ context.Context, s session.Session) (provider.Command, error) {
	if err := provider.CheckResumable(s); err != nil {
		return provider.Command{}, err
	}
	return provider.Command{Name: "codex", Args: []string{"resume", "--cd", s.Directory, s.ID}, Directory: s.Directory}, nil
}

// MatchesProcess accepts only `codex resume [options] ID`. Option values are
// skipped so a path is not mistaken for the ID, and a prompt containing the ID
// after another positional argument does not match.
func (a *Adapter) MatchesProcess(args []string, s session.Session) bool {
	args, ok := provider.ToolArgs(args, session.Codex)
	if !ok {
		return false
	}
	resume := false
	for i := 0; i < len(args); i++ {
		if args[i] == "resume" {
			resume = true
			continue
		}
		switch args[i] {
		case "--cd", "-C", "--config", "-c", "--model", "-m", "--sandbox", "-s", "--profile", "-p", "--remote", "--remote-auth-token-env", "--add-dir", "--ask-for-approval", "-a", "--enable", "--disable":
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		if resume {
			return args[i] == s.ID
		}
		return false
	}
	return false
}

// Parse reads one rollout file from the beginning.
func Parse(path string) (session.Session, error) {
	return jsonl.Parse(path, session.Codex, apply)
}

type record struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Payload     struct {
		ID          string           `json:"id"`
		SessionID   string           `json:"session_id"`
		CWD         string           `json:"cwd"`
		Type        string           `json:"type"`
		Role        string           `json:"role"`
		Name        string           `json:"name"`
		Message     string           `json:"message"`
		LastMessage string           `json:"last_agent_message"`
		Content     []provider.Block `json:"content"`
		Source      json.RawMessage  `json:"source"`
	} `json:"payload"`
}

// spawnedBy reads the session_meta source of a rollout. Sessions a person
// started have a plain string source such as "cli", "vscode" or "exec".
// Codex runs others itself, and every such kind counts as spawned, so that
// new kinds stay quiet too: sub-agents, as {"subagent": …}, whose
// thread_spawn form names the parent thread, and internal jobs such as
// memory consolidation, as {"internal": …} or "internal".
func spawnedBy(source json.RawMessage) (parent string, spawned bool) {
	var kind string
	if json.Unmarshal(source, &kind) == nil {
		return "", strings.HasPrefix(kind, "subagent") || strings.HasPrefix(kind, "internal")
	}
	var tagged map[string]json.RawMessage
	if json.Unmarshal(source, &tagged) != nil {
		return "", false
	}
	if subagent, ok := tagged["subagent"]; ok {
		var s struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		}
		// A malformed source loses only the parent's ID. The run is still
		// spawned, which alone keeps it out of the list (Session.Root), so
		// nothing a person sees changes and no warning is worth adding.
		_ = json.Unmarshal(subagent, &s)
		return s.ThreadSpawn.ParentThreadID, true
	}
	_, internal := tagged["internal"]
	return "", internal
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
	p := r.Payload
	switch r.Type {
	case "session_meta":
		s.ID = p.ID
		if s.ID == "" {
			s.ID = p.SessionID
		}
		s.Directory, s.OriginalDirectory = p.CWD, p.CWD
		s.Parent, s.Spawned = spawnedBy(p.Source)
	case "turn_context":
		if p.CWD != "" {
			s.Directory, s.OriginalDirectory = p.CWD, p.CWD
		}
	case "event_msg":
		switch p.Type {
		case "task_started":
			provider.SetState(s, session.Working, "Transcript: turn started")
		case "task_complete", "task_completed":
			provider.SetState(s, session.Completed, "Transcript: last turn completed")
			if p.LastMessage != "" {
				s.Summary = session.Text(p.LastMessage, 600)
			}
		case "turn_aborted":
			provider.SetState(s, session.Idle, "Transcript: turn interrupted")
		case "error":
			provider.SetState(s, session.Failed, "Transcript: error reported")
		case "user_message":
			if s.Title == "" {
				s.Title = session.Text(p.Message, 100)
			}
		case "agent_message":
			s.Summary = session.Text(p.Message, 600)
		}
	case "response_item":
		if p.Type == "function_call" && p.Name == "request_user_input" {
			provider.SetState(s, session.Waiting, "Transcript: user input requested")
		}
		if p.Type == "function_call_output" {
			provider.SetState(s, session.Working, "Transcript: tool result received")
		}
		if p.Role == "assistant" {
			if text := provider.BlockText(p.Content); text != "" {
				s.Summary = session.Text(text, 600)
			}
		}
	}
	return nil
}
