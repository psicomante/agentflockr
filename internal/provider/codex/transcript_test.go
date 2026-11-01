package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

func TestCodexLifecycleAndPartialAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","timestamp":"2026-09-22T10:00:00Z","payload":{"id":"cx-1","cwd":"/old/project"}}
{"type":"event_msg","timestamp":"2026-09-22T10:01:00Z","payload":{"type":"task_started"}}
{"type":"event_msg","timestamp":"2026-09-22T10:01:01Z","payload":{"type":"user_message","message":"Fix checkout"}}
{"type":"event_msg","timestamp":"2026-09-22T10:02:00Z","payload":{"type":"task_complete","last_agent_message":"Tests pass"}}
`
	if err := os.WriteFile(p, []byte(data+`{"type":`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cx-1" || s.OriginalDirectory != "/old/project" || s.Status != session.Completed || s.Title != "Fix checkout" || s.Summary != "Tests pass" {
		t.Fatalf("unexpected session: %+v", s)
	}
	if err := os.WriteFile(p, []byte(data+`{"type":"event_msg","timestamp":"2026-09-22T10:03:00Z","payload":{"type":"task_started"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Parse(p)
	if err != nil || s.Status != session.Working {
		t.Fatalf("new turn: %+v %v", s, err)
	}
	s = provider.Observe(s, time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC), 0)
	if s.Status != session.Unknown {
		t.Fatal("stale incomplete turn must not be reported as live")
	}
}

func TestCompleteFinalJSONRecordDoesNotRequireNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"no-newline","cwd":"/project"}}`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(p)
	if err != nil || s.ID != "no-newline" {
		t.Fatalf("valid final record lost: %+v %v", s, err)
	}
}

func TestSourceMarksRunsCodexStartedItself(t *testing.T) {
	for _, tc := range []struct {
		name, source, parent string
		spawned              bool
	}{
		{"started by a person", `"cli"`, "", false},
		{"started by a script", `"exec"`, "", false},
		{"spawned by another thread", `{"subagent":{"thread_spawn":{"parent_thread_id":"cx-root","depth":1,"agent_role":"worker"}}}`, "cx-root", true},
		{"a review names no parent", `{"subagent":"review"}`, "", true},
		{"memory consolidation", `{"subagent":"memory_consolidation"}`, "", true},
		{"other sub-agent kinds", `{"subagent":{"other":"guardian"}}`, "", true},
		{"an internal job", `{"internal":"memory_consolidation"}`, "", true},
		{"an internal job as a string", `"internal"`, "", true},
		{"no source", `null`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "rollout.jsonl")
			data := `{"type":"session_meta","payload":{"id":"cx-1","cwd":"/project","source":` + tc.source + `}}` + "\n"
			if err := os.WriteFile(p, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := Parse(p)
			if err != nil || s.Parent != tc.parent || s.Spawned != tc.spawned || s.Root() == tc.spawned {
				t.Fatalf("parent %q spawned %v, want %q %v (%v)", s.Parent, s.Spawned, tc.parent, tc.spawned, err)
			}
		})
	}
}
