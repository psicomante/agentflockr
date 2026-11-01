package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/claude"
	"github.com/psicomante/agentflockr/internal/provider/codex"
	"github.com/psicomante/agentflockr/internal/provider/opencode"
	"github.com/psicomante/agentflockr/internal/session"
)

func TestResumeKeepsPathsAsArguments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project ; with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	adapters := provider.Registry{opencode.New(), codex.New(""), claude.New("")}
	for _, tt := range []struct {
		provider session.Provider
		args     []string
	}{
		{session.Codex, []string{"resume", "--cd", dir, "cx-1"}},
		{session.Claude, []string{"--resume", "/transcripts/a b.jsonl"}},
		{session.OpenCode, []string{"--session", "cx-1", dir}},
	} {
		s := session.Session{ID: "cx-1", Provider: tt.provider, Directory: dir, OriginalDirectory: dir, Transcript: "/transcripts/a b.jsonl"}
		plan, err := adapters.Get(tt.provider).Resume(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plan.Args, tt.args) || plan.Directory != dir {
			t.Fatalf("bad plan: %+v", plan)
		}
	}
}

func TestMissingDirectoryCannotLaunch(t *testing.T) {
	err := provider.CheckResumable(session.Session{ID: "a", Provider: session.Codex, Directory: filepath.Join(t.TempDir(), "missing")})
	if err == nil {
		t.Fatal("expected reconnect error")
	}
}

func TestProcessMatchingRequiresExactSessionFlag(t *testing.T) {
	transcript := "/home/me/.claude/projects/p/cc-1.jsonl"
	for _, tt := range []struct {
		adapter provider.Adapter
		args    []string
		want    bool
	}{
		{opencode.New(), []string{"opencode", "--session", "ses_a", "/work"}, true},
		{opencode.New(), []string{"/usr/local/bin/opencode", "-s", "ses_a"}, true},
		{opencode.New(), []string{"opencode", "--session", "ses_ab"}, false},
		{opencode.New(), []string{"claude", "--session", "ses_a"}, false},
		{claude.New(""), []string{"node", "/opt/claude", "--resume", "cc-1"}, true},
		{claude.New(""), []string{"claude", "-r", transcript}, true},
		{claude.New(""), []string{"claude", "explain", "cc-1"}, false},
		{claude.New(""), []string{"claude", "--session-id", "cc-1"}, true},
		{claude.New(""), []string{"claude", "--session-id", "cc-12"}, false},
	} {
		id := "ses_a"
		if tt.adapter.ID() == session.Claude {
			id = "cc-1"
		}
		s := session.Session{ID: id, Provider: tt.adapter.ID(), Transcript: transcript}
		if got := tt.adapter.MatchesProcess(tt.args, s); got != tt.want {
			t.Errorf("%s %v: got %v, want %v", tt.adapter.ID(), tt.args, got, tt.want)
		}
	}
}

// An agent's stale-after setting decides how long a working signal counts.
func TestObserveUsesTheStaleWindow(t *testing.T) {
	now := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	working := session.Session{ID: "a", Directory: "/p", Status: session.Working, UpdatedAt: now.Add(-5 * time.Minute)}
	if got := provider.Observe(working, now, 0).Status; got != session.Unknown {
		t.Errorf("default window: %s, want unknown after 5 minutes", got)
	}
	if got := provider.Observe(working, now, 10*time.Minute).Status; got != session.Working {
		t.Errorf("10-minute window: %s, want working after 5 minutes", got)
	}
}

func TestRunRefusesOutputOverTheLimit(t *testing.T) {
	// yes prints forever; the limit must stop it instead of filling memory.
	if _, err := provider.Run(context.Background(), "yes"); err == nil {
		t.Fatal("endless output must be refused")
	}
}
