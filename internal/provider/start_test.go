package provider_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/codex"
	"github.com/psicomante/agentflockr/internal/session"
)

func TestCheckChoiceNeverSubstitutes(t *testing.T) {
	c := provider.Catalog{
		DefaultEfforts: []string{"low"},
		Models: []provider.Model{
			{ID: "opus", Efforts: []string{"low", "high"}},
			{ID: "haiku"},
		},
	}
	accepted := []struct{ model, effort string }{
		{"", ""},
		{"", "low"},
		{"opus", ""},
		{"opus", "high"},
		{"haiku", ""},
	}
	for _, tc := range accepted {
		if err := provider.CheckChoice(c, tc.model, tc.effort); err != nil {
			t.Errorf("CheckChoice(%q, %q) refused: %v", tc.model, tc.effort, err)
		}
	}
	refused := []struct{ model, effort string }{
		{"", "high"},
		{"sonnet", ""},
		{"opus", "max"},
		{"haiku", "low"},
	}
	for _, tc := range refused {
		if err := provider.CheckChoice(c, tc.model, tc.effort); err == nil {
			t.Errorf("CheckChoice(%q, %q) accepted a value the catalogue does not offer", tc.model, tc.effort)
		}
	}
	if err := provider.CheckChoice(provider.Catalog{}, "", "low"); err == nil || err.Error() != "choose a model to set effort" {
		t.Fatalf("effort without a model: %v", err)
	}
	// A profile without a default model needs one named.
	required := provider.Catalog{ModelRequired: true, Models: []provider.Model{{ID: "m1"}}}
	if err := provider.CheckChoice(required, "", ""); err == nil {
		t.Fatal("Default was accepted on a profile that has no default model")
	}
	if err := provider.CheckChoice(required, "m1", ""); err != nil {
		t.Fatalf("a named model was refused: %v", err)
	}
}

func TestValidArgumentRefusesOptionsAndSeparators(t *testing.T) {
	for _, v := range []string{"opus", "anthropic/claude-x", "gpt:5.1", "fast-1"} {
		if !provider.ValidArgument(v) {
			t.Errorf("refused %q", v)
		}
	}
	for _, v := range []string{"", "--model", "-x", "a b", "a;b", "$(id)", strings.Repeat("a", 201)} {
		if provider.ValidArgument(v) {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestCommandEnvSetsAndUnsets(t *testing.T) {
	t.Setenv("START_KEEP", "keep")
	t.Setenv("START_DROP", "drop")
	t.Setenv("START_REPLACE", "old")

	inherited := provider.Command{Name: "true"}.Exec()
	if inherited.Env != nil {
		t.Fatalf("empty Env and Unset must leave the environment nil, got %d entries", len(inherited.Env))
	}

	cmd := provider.Command{
		Name:  "true",
		Env:   []string{"START_REPLACE=new", "START_ADDED=yes"},
		Unset: []string{"START_DROP"},
	}.Exec()
	env := map[string]string{}
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		if _, dup := env[key]; dup {
			t.Fatalf("%s appears twice", key)
		}
		env[key] = value
	}
	if _, ok := env["START_DROP"]; ok {
		t.Error("unset variable reached the child")
	}
	if env["START_KEEP"] != "keep" {
		t.Error("inherited variable was lost")
	}
	if env["START_REPLACE"] != "new" {
		t.Errorf("Env did not replace the inherited value: %q", env["START_REPLACE"])
	}
	if env["START_ADDED"] != "yes" {
		t.Error("Env entry missing")
	}
}

func TestCheckFolderIsSharedByOpenAndStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	for _, dir := range []string{"relative/path", missing} {
		folderErr := provider.CheckFolder(dir)
		if folderErr == nil {
			t.Fatalf("CheckFolder accepted %q", dir)
		}
		// Opening a session reads the same rule through CheckResumable.
		_, openErr := codex.New("").Resume(context.Background(), session.Session{
			Provider: session.Codex, ID: "sess-1", Directory: dir,
		})
		if openErr == nil || openErr.Error() != folderErr.Error() {
			t.Fatalf("open says %v, start says %v", openErr, folderErr)
		}
	}
	// The dashboard adds the move hint from the error alone, without a stat.
	if err := provider.CheckFolder(missing); !errors.Is(err, provider.ErrMissingFolder) {
		t.Fatalf("a missing folder must wrap ErrMissingFolder: %v", err)
	}
	dir := t.TempDir()
	if err := provider.CheckFolder(dir); err != nil {
		t.Fatalf("existing folder refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := provider.CheckFolder(filepath.Join(dir, "file")); err == nil {
		t.Fatal("a file accepted as a folder")
	}
}

// Every deletion starts with the same checks: a cancelled request or an ID
// that reads as an option or a path never reaches an agent.
func TestCheckDeletableRefusesCancelledAndUnsafeIDs(t *testing.T) {
	if err := provider.CheckDeletable(context.Background(), session.Session{ID: "ses_1"}); err != nil {
		t.Fatalf("a valid ID was refused: %v", err)
	}
	for _, id := range []string{"", "-rf", "../x", "a/b"} {
		if err := provider.CheckDeletable(context.Background(), session.Session{ID: id}); err == nil {
			t.Errorf("ID %q was accepted", id)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := provider.CheckDeletable(ctx, session.Session{ID: "ses_1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled request: %v", err)
	}
}
