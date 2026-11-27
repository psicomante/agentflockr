package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func inspectionFixture(t *testing.T, data []byte) (*Adapter, session.Session) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cc-1.jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return New(home), session.Session{ID: "cc-1", Transcript: path}
}

func TestInspectReadsModelEffortModeAndContextFromTheTranscript(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	a, item := inspectionFixture(t, data)
	info, err := a.Inspect(t.Context(), item)
	if err != nil || info.Model != "claude-haiku-5-5" || info.Effort != "low" || info.Mode != "auto" || info.Version != "2.1.316" || !info.ContextKnown || info.ContextTokens != 39500 || info.ContextWindow != 0 || info.Profile != a.Home {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestInspectKeepsUsageWithItsModel(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, usage := range []string{`{"input_tokens":9,"cache_read_input_tokens":0,"cache_creation_input_tokens":1}`, `null`, `{"input_tokens":-1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`, `{"input_tokens":9223372036854775807,"cache_read_input_tokens":1,"cache_creation_input_tokens":0}`, `{"input_tokens":99999999999999999999999999999}`} {
		a, item := inspectionFixture(t, append(append([]byte{}, data...), []byte(`{"type":"assistant","effort":"high","message":{"model":"switched","usage":`+usage+`}}`)...))
		info, err := a.Inspect(t.Context(), item)
		if err != nil || info.Model != "switched" || info.Effort != "high" {
			t.Fatalf("%+v %v", info, err)
		}
		if usage == `{"input_tokens":9,"cache_read_input_tokens":0,"cache_creation_input_tokens":1}` {
			if !info.ContextKnown || info.ContextTokens != 10 {
				t.Fatal(info)
			}
		} else if info.ContextKnown || info.ContextTokens != 0 {
			t.Fatal(info)
		}
	}
}

func TestClaudeInspectionAttributesOnlySuccessfulExplicitFileEdits(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The write's content is longer than the display input limit. Its target
	// must still survive; inspection cannot recover it from truncated JSON.
	write, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "msg1", "content": []map[string]any{{"type": "tool_use", "id": "write", "name": "Write", "input": map[string]string{"file_path": "long.go", "content": strings.Repeat("x", maxInput+1)}}}}})
	data := string(write) + "\n" + `{"type":"assistant","message":{"id":"msg2","content":[{"type":"tool_use","id":"fail","name":"Edit","input":{"file_path":"failed.go"}},{"type":"tool_use","id":"read","name":"Read","input":{"file_path":"read.go"}},{"type":"tool_use","id":"pending","name":"Edit","input":{"file_path":"pending.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"write","content":"ok"},{"type":"tool_result","tool_use_id":"fail","is_error":true,"content":"no"},{"type":"tool_result","tool_use_id":"read","content":"read"}]}}
`
	if err := os.WriteFile(filepath.Join(dir, "cc-1.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := New(home).Inspect(t.Context(), session.Session{ID: "cc-1"})
	if err != nil || !info.FilesKnown || len(info.Files) != 1 || info.Files[0].Path != "long.go" || !strings.Contains(info.FilesSource, "recent transcript") {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestClaudeIdleDoesNotHideAnAPIError(t *testing.T) {
	home := liveHome(t)
	file := filepath.Join(home, "projects", "project", "cc-1.jsonl")
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"type\":\"assistant\",\"isApiErrorMessage\":true,\"message\":{\"content\":\"Credit exhausted\"}}\n{\"type\":\"system\",\"subtype\":\"turn_duration\"}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	writeLive(t, home, os.Getpid(), `"status":"idle"`)
	s := scanOne(t, home)
	if s.Status != session.Failed || !strings.Contains(s.Evidence, "Credit exhausted") {
		t.Fatalf("%+v", s)
	}
	// A genuinely new turn, reported busy by the process, supersedes failure.
	writeLive(t, home, os.Getpid(), `"status":"busy"`)
	if s = scanOne(t, home); s.Status != session.Working {
		t.Fatalf("%+v", s)
	}
	list, err := New(home).Messages(t.Context(), session.Session{ID: "cc-1"}, 10)
	if err != nil || len(list) == 0 || list[len(list)-1].Error != "Credit exhausted" {
		t.Fatalf("messages: %+v %v", list, err)
	}
}
