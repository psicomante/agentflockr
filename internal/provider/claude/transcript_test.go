package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestClaudeQuestionAndCompletion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cc-1.jsonl")
	data := `{"type":"user","sessionId":"cc-1","cwd":"/project","timestamp":"2026-09-22T10:00:00Z","message":{"content":"Build it"}}
{"type":"assistant","sessionId":"cc-1","timestamp":"2026-09-22T10:01:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[{"question":"Which color?"}]}}]}}
`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(p)
	if err != nil || s.Status != session.Waiting {
		t.Fatalf("question: %+v %v", s, err)
	}
	data += `{"type":"user","sessionId":"cc-1","timestamp":"2026-09-22T10:02:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"q1","content":"Blue"}]}}
{"type":"assistant","sessionId":"cc-1","timestamp":"2026-09-22T10:03:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"Done\u001b[31m safely"}]}}
`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Parse(p)
	if err != nil || s.Status != session.Completed || s.Summary != "Done safely" {
		t.Fatalf("completion: %+v %v", s, err)
	}
}

// A session that changes into a subfolder belongs to the project it started
// in, whatever folder its later records name.
func TestClaudeSessionKeepsTheFolderItStartedIn(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cc-1.jsonl")
	data := `{"type":"user","sessionId":"cc-1","cwd":"/a","message":{"content":"Build it"}}
{"type":"assistant","sessionId":"cc-1","cwd":"/a/sub","message":{"content":"ok"}}
`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Directory != "/a" || s.OriginalDirectory != "/a" {
		t.Fatalf("folder %q (original %q), want the starting folder /a", s.Directory, s.OriginalDirectory)
	}
}
