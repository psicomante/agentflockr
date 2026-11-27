package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// A scheduled prompt resumes the Codex session with `codex exec resume`, in
// its folder and home, with the chosen model and effort.
func TestPromptResumesWithCodexExec(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	a := New(home)
	ran := catalogue(a, filepath.Join("testdata", "debug-models.json"))
	s := session.Session{Provider: session.Codex, ID: "019a-session", Directory: dir}
	if err := a.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: " go on ", Model: "gpt-test-large", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	last := (*ran)[len(*ran)-1]
	want := []string{"exec", "resume", "--skip-git-repo-check", "--model", "gpt-test-large", "-c", `model_reasoning_effort="high"`, "019a-session", "go on"}
	if last.Name != "codex" || !reflect.DeepEqual(last.Args, want) || last.Directory != dir || !reflect.DeepEqual(last.Env, []string{"CODEX_HOME=" + home}) {
		t.Fatalf("ran %s %v in %q env %v", last.Name, last.Args, last.Directory, last.Env)
	}
	for _, bad := range []provider.ScheduledPrompt{{Text: "-x"}, {Text: "go", Model: "unknown"}, {Text: "go", Effort: "high"}} {
		if err := a.Prompt(context.Background(), s, bad); err == nil {
			t.Fatalf("%+v was sent", bad)
		}
	}
}

// A profile run is `codex exec --ephemeral` in the given folder, so Codex
// keeps no session; on the defaults it does not read the catalogue.
func TestPromptProfileRunsAnEphemeralExec(t *testing.T) {
	fakeCodex(t)
	home, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "fast.config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := New(home)
	ran := catalogue(a, filepath.Join("testdata", "debug-models.json"))
	if err := a.PromptProfile(context.Background(), "fast", dir, provider.ScheduledPrompt{Text: "ping"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--profile", "fast", "--ephemeral", "--skip-git-repo-check", "--cd", dir, "ping"}
	if len(*ran) != 1 || !reflect.DeepEqual((*ran)[0].Args, want) || (*ran)[0].Directory != dir {
		t.Fatalf("ran %+v", *ran)
	}
	if err := a.PromptProfile(context.Background(), "", dir, provider.ScheduledPrompt{Text: "ping", Model: "gpt-test-large"}); err != nil {
		t.Fatal(err)
	}
	last := (*ran)[len(*ran)-1]
	if want := []string{"exec", "--model", "gpt-test-large", "--ephemeral", "--skip-git-repo-check", "--cd", dir, "ping"}; !reflect.DeepEqual(last.Args, want) {
		t.Fatalf("default profile ran %v", last.Args)
	}
	if err := a.PromptProfile(context.Background(), "missing", dir, provider.ScheduledPrompt{Text: "ping"}); err == nil {
		t.Fatal("ran through a profile that is not a profile file")
	}
}
