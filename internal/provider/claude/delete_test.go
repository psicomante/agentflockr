package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

const testUUID = "11111111-2222-4333-8444-555555555555"

func claudeFixture(t *testing.T) (*Adapter, session.Session, string) {
	t.Helper()
	home := t.TempDir()
	project := filepath.Join(home, "projects", "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, testUUID+".jsonl")
	data, _ := json.Marshal(map[string]string{"type": "user", "sessionId": testUUID, "cwd": "/project"})
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return New(home), session.Session{Provider: session.Claude, ID: testUUID, Transcript: path}, project
}

func TestClaudeDeletesOnlyOwnedTranscriptAndSubagents(t *testing.T) {
	d, s, project := claudeFixture(t)
	child := filepath.Join(project, s.ID, "subagents", "agent.jsonl")
	if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(project, "another.jsonl")
	if err := os.WriteFile(sibling, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.Transcript, child} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("history remains: %s %v", path, err)
		}
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "keep" {
		t.Fatal("sibling changed")
	}
	if err := d.Delete(context.Background(), s); err != nil {
		t.Fatalf("retry after success: %v", err)
	}
}

func TestClaudeRefusesWrongRootIdentityAndSymlinks(t *testing.T) {
	for _, kind := range []string{"outside", "identity", "symlink", "project-symlink"} {
		t.Run(kind, func(t *testing.T) {
			d, s, project := claudeFixture(t)
			outside := filepath.Join(t.TempDir(), s.ID+".jsonl")
			if err := os.WriteFile(outside, []byte(`{"sessionId":"`+testUUID+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "outside":
				s.Transcript = outside
			case "identity":
				if err := os.WriteFile(s.Transcript, []byte(`{"sessionId":"other"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(s.Transcript); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, s.Transcript); err != nil {
					t.Fatal(err)
				}
			case "project-symlink":
				s.Transcript = filepath.Join(filepath.Dir(project), "escape", s.ID+".jsonl")
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(s.Transcript)); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Delete(context.Background(), s); err == nil {
				t.Fatal("unsafe deletion accepted")
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestClaudeRefusesLivePID(t *testing.T) {
	d, s, _ := claudeFixture(t)
	if err := os.Mkdir(filepath.Join(d.Home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"sessionId": s.ID, "pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(d.Home, "sessions", "live.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(context.Background(), s); err == nil {
		t.Fatal("live transcript deleted")
	}
	if _, err := os.Stat(s.Transcript); err != nil {
		t.Fatal(err)
	}
}

// A session that writes to its transcript while it is being deleted keeps
// everything: the check runs before the subagent folder is removed.
func TestClaudeDeletesNothingWhenTheTranscriptChanges(t *testing.T) {
	d, s, project := claudeFixture(t)
	child := filepath.Join(project, s.ID, "subagents", "agent.jsonl")
	if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}
	afterVerify = func() {
		f, err := os.OpenFile(s.Transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("{\"type\":\"user\"}\n")
		f.Close()
	}
	defer func() { afterVerify = func() {} }()
	err := d.Delete(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "transcript changed") {
		t.Fatalf("got %v, want a refusal because the transcript changed", err)
	}
	for _, path := range []string{s.Transcript, child} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(path), err)
		}
	}
}
