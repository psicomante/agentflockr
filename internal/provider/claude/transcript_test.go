package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestClaudeCustomTitleWinsInEitherOrder(t *testing.T) {
	for _, titles := range [][]string{{`{"customTitle":"My title"}`, `{"aiTitle":"AI title"}`}, {`{"aiTitle":"AI title"}`, `{"customTitle":"My title"}`}} {
		var s session.Session
		for _, line := range titles {
			if err := apply(&s, []byte(line)); err != nil {
				t.Fatal(err)
			}
			// Incremental reads and index round trips must keep title precedence.
			data, _ := json.Marshal(s)
			if err := json.Unmarshal(data, &s); err != nil {
				t.Fatal(err)
			}
		}
		if s.Title != "My title" {
			t.Fatalf("title = %q", s.Title)
		}
	}
}

func TestClaudeAPIErrorSurvivesTurnDuration(t *testing.T) {
	var s session.Session
	for _, line := range []string{
		`{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"Usage limit reached"}],"stop_reason":"end_turn"}}`,
		`{"type":"system","subtype":"turn_duration"}`,
	} {
		if err := apply(&s, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != session.Failed || s.Evidence != "Transcript: API error: Usage limit reached" {
		t.Fatalf("error: %+v", s)
	}
	// An ordinary message quoting an error is not evidence of failure.
	if err := apply(&s, []byte(`{"type":"user","message":{"content":"Usage limit reached yesterday"}}`)); err != nil {
		t.Fatal(err)
	}
	if s.Status != session.Working {
		t.Fatalf("recovery: %+v", s)
	}
}

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

// A session started with /clear begins with Claude Code's own records; the
// person's first prompt names it.
func TestClaudeTitleSkipsRecordsClaudeWrites(t *testing.T) {
	var s session.Session
	for _, line := range []string{
		`{"type":"user","message":{"content":"<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>"}}`,
		`{"type":"user","message":{"content":"<local-command-stdout></local-command-stdout>"}}`,
		`{"type":"user","message":{"content":"<bash-input>ls</bash-input>"}}`,
		`{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"Background command completed"}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"content":"Fix the login page"}}`,
	} {
		if err := apply(&s, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if s.Title != "Fix the login page" {
		t.Fatalf("title = %q", s.Title)
	}
}
