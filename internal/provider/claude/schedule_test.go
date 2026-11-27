package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
)

// A scheduled prompt runs the session's own resume, with its profile's home,
// in print mode, in the session's folder.
func TestPromptResumesInPrintModeWithTheProfilesHome(t *testing.T) {
	tools, out := t.TempDir(), filepath.Join(t.TempDir(), "call")
	script := "#!/bin/sh\n{ pwd; echo \"$CLAUDE_CONFIG_DIR\"; for a in \"$@\"; do echo \"$a\"; done; } > " + out + "\n"
	if err := os.WriteFile(filepath.Join(tools, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := folder(t, ".claude")
	work := folder(t, ".claude-work")
	a := New(home, work)
	a.DefaultHome = home
	s := resumeSession(t, transcriptIn(work))

	if err := a.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "  keep going\n"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(s.Directory)
	want := strings.Join([]string{dir, work, "--resume", transcriptIn(work), "--print", "keep going"}, "\n") + "\n"
	if got := string(data); got != want {
		t.Fatalf("call:\n%s\nwant:\n%s", got, want)
	}
}

func TestPromptRefusesTextThatReadsAsAnOption(t *testing.T) {
	standIn(t, "claude")
	a := New(folder(t, ".claude"))
	s := resumeSession(t, "")
	for _, text := range []string{"", "  ", "--dangerously-skip-permissions"} {
		if err := a.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: text}); err == nil {
			t.Fatalf("prompt %q was sent", text)
		}
	}
}

// A model and effort are passed for the run, before the print-mode prompt.
func TestPromptPassesTheModelAndEffort(t *testing.T) {
	tools, out := t.TempDir(), filepath.Join(t.TempDir(), "call")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + out + "\n"
	if err := os.WriteFile(filepath.Join(tools, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := folder(t, ".claude")
	a := New(home)
	s := resumeSession(t, transcriptIn(home))
	if err := a.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "go", Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	want := strings.Join([]string{"--resume", transcriptIn(home), "--model", "opus", "--effort", "high", "--print", "go"}, "\n") + "\n"
	if got := string(data); got != want {
		t.Fatalf("call:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []provider.ScheduledPrompt{{Text: "go", Model: "gpt"}, {Text: "go", Model: "opus", Effort: "huge"}, {Text: "go", Model: "--yolo"}} {
		if err := a.Prompt(context.Background(), s, bad); err == nil {
			t.Fatalf("%+v was sent", bad)
		}
	}
}

// A profile run uses the profile's command and home in the given folder,
// outside any session, and asks Claude Code to keep no session.
func TestPromptProfileRunsThePrintModeOutsideASession(t *testing.T) {
	tools, out := t.TempDir(), filepath.Join(t.TempDir(), "call")
	script := "#!/bin/sh\n{ pwd; echo \"$CLAUDE_CONFIG_DIR\"; for a in \"$@\"; do echo \"$a\"; done; } > " + out + "\n"
	if err := os.WriteFile(filepath.Join(tools, "claude-work"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, work := folder(t, ".claude"), folder(t, ".claude-work")
	a := New(home, work)
	a.DefaultHome = home
	a.Profiles = []Profile{{Name: "work", Home: work, Command: []string{"claude-work", "--verbose"}}}
	dir := t.TempDir()
	if err := a.PromptProfile(context.Background(), "work", dir, provider.ScheduledPrompt{Text: "ping", Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	real, _ := filepath.EvalSymlinks(dir)
	want := strings.Join([]string{real, work, "--verbose", "--model", "haiku", "--no-session-persistence", "--print", "ping"}, "\n") + "\n"
	if got := string(data); got != want {
		t.Fatalf("call:\n%s\nwant:\n%s", got, want)
	}
	if err := a.PromptProfile(context.Background(), work, dir, provider.ScheduledPrompt{Text: "ping"}); err == nil {
		t.Fatal("ran through a home path that is not a listed profile ID")
	}
}
