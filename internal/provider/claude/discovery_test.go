package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanMultipleHomes(t *testing.T) {
	homes := []string{filepath.Join(t.TempDir(), ".claude"), filepath.Join(t.TempDir(), ".claude-personal")}
	for i, home := range homes {
		project := filepath.Join(home, "projects", "project")
		if err := os.MkdirAll(project, 0700); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"cc-%d\",\"cwd\":\"/project\",\"message\":{\"content\":\"Hello\"}}\n", i)
		if err := os.WriteFile(filepath.Join(project, fmt.Sprintf("cc-%d.jsonl", i)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(homes[0], homes[1])
	result := a.Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != 2 {
		t.Fatalf("scan: %+v", result)
	}
	// Deletion must use the owning home, not always the default home.
	for _, s := range result.Sessions {
		if err := a.Delete(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.Transcript); !os.IsNotExist(err) {
			t.Fatalf("transcript still exists: %v", err)
		}
	}
}

func TestExtraHomeDeletionChecksOwningHomeLiveSession(t *testing.T) {
	owner, s, _ := claudeFixture(t)
	a := New(t.TempDir(), owner.Home)
	liveDir := filepath.Join(owner.Home, "sessions")
	if err := os.Mkdir(liveDir, 0700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"sessionId":%q,"pid":%d}`, s.ID, os.Getpid())
	if err := os.WriteFile(filepath.Join(liveDir, "live.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(context.Background(), s); err == nil {
		t.Fatal("live transcript in additional home deleted")
	}
	if _, err := os.Stat(s.Transcript); err != nil {
		t.Fatal(err)
	}
}

// Subagent transcripts live one level below the project folder and are not
// sessions of their own.
func TestScanSkipsSubagentFolders(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "projects", "project")
	nested := filepath.Join(project, "cc-1", "subagents")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for path, id := range map[string]string{filepath.Join(project, "cc-1.jsonl"): "cc-1", filepath.Join(nested, "agent.jsonl"): "agent"} {
		data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":\"/project\",\"message\":{\"content\":\"Hello\"}}\n", id)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := New(home).Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != 1 || result.Sessions[0].ID != "cc-1" {
		t.Fatalf("scan: %+v", result)
	}
}

// Only ~/.claude-* folders with a projects folder are Claude Code homes.
func TestProfilesNeedAProjectsFolder(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".claude-work/projects", ".claude-backup", ".claude/projects"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := Profiles(home), []string{filepath.Join(home, ".claude-work")}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
