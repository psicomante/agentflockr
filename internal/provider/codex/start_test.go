package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
)

// fakeCodex puts a codex stand-in first on PATH, so the check that Codex is
// installed passes. It fails if run: the catalogue comes from catalogue.
func fakeCodex(t *testing.T) {
	t.Helper()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "codex"), []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// catalogue answers `codex debug models` for a with the fixture at path, or
// with an error when path is empty, and records the command it was given.
func catalogue(a *Adapter, path string) *[]provider.Command {
	var ran []provider.Command
	a.RunCommand = func(_ context.Context, c provider.Command) ([]byte, error) {
		ran = append(ran, c)
		if path == "" {
			return nil, fmt.Errorf("codex: not signed in")
		}
		return os.ReadFile(path)
	}
	return &ran
}

func profileNames(profiles []provider.Profile) []string {
	var names []string
	for _, p := range profiles {
		names = append(names, p.ID)
	}
	return names
}

func TestProfilesAreCodexProfileFiles(t *testing.T) {
	fakeCodex(t)
	home := t.TempDir()
	target := filepath.Join(home, "target.txt")
	for _, name := range []string{"work.config.toml", "fast.config.toml", "-option.config.toml", "notes.txt", "target.txt"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("model = \"x\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, filepath.Join(home, "linked.config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "folder.config.toml"), 0700); err != nil {
		t.Fatal(err)
	}
	profiles, err := New(home).StartProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "fast", "work"}; !reflect.DeepEqual(profileNames(profiles), want) {
		t.Fatalf("profiles %q, want %q", profileNames(profiles), want)
	}
	if profiles[0].Name != "Codex default" {
		t.Fatalf("default profile named %q", profiles[0].Name)
	}
	for _, p := range profiles {
		if p.Problem != "" {
			t.Fatalf("usable profile %q reported %q", p.ID, p.Problem)
		}
	}
}

func TestMissingCodexHomeSaysWhy(t *testing.T) {
	fakeCodex(t)
	profiles, err := New(filepath.Join(t.TempDir(), "missing")).StartProfiles(context.Background())
	if err != nil || len(profiles) != 1 || profiles[0].Problem == "" {
		t.Fatalf("missing home: %+v %v", profiles, err)
	}
}

func TestCatalogueFromDebugModels(t *testing.T) {
	fakeCodex(t)
	home := t.TempDir()
	a := New(home)
	ran := catalogue(a, filepath.Join("testdata", "debug-models.json"))
	catalog, err := a.StartCatalog(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// The internal model is hidden from Codex's own picker, so it is not offered.
	if len(catalog.Models) != 2 {
		t.Fatalf("models %+v", catalog.Models)
	}
	large := catalog.Models[0]
	if large.ID != "gpt-test-large" || large.Name != "GPT Test Large" || large.DefaultEffort != "medium" ||
		!reflect.DeepEqual(large.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("large model %+v", large)
	}
	if small := catalog.Models[1]; small.Name != "gpt-test-small" || len(small.Efforts) != 0 {
		t.Fatalf("model without efforts %+v", small)
	}
	if len(catalog.DefaultEfforts) != 0 {
		t.Fatalf("default-model efforts should be empty: %v", catalog.DefaultEfforts)
	}
	if len(*ran) != 1 || !reflect.DeepEqual((*ran)[0].Args, []string{"debug", "models"}) ||
		!reflect.DeepEqual((*ran)[0].Env, []string{"CODEX_HOME=" + home}) {
		t.Fatalf("catalogue read with %+v, want `codex debug models` with CODEX_HOME=%s", *ran, home)
	}
}

func TestStartPassesProfileModelAndEffort(t *testing.T) {
	fakeCodex(t)
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "fast.config.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := New(home)
	catalogue(a, filepath.Join("testdata", "debug-models.json"))
	cmd, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Profile: "fast", Model: "gpt-test-large", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--profile", "fast", "--model", "gpt-test-large", "-c", `model_reasoning_effort="high"`, "--cd", dir}
	if cmd.Name != "codex" || !reflect.DeepEqual(cmd.Args, wantArgs) || cmd.Directory != dir ||
		!reflect.DeepEqual(cmd.Env, []string{"CODEX_HOME=" + home}) {
		t.Fatalf("command %s %v in %q env %v", cmd.Name, cmd.Args, cmd.Directory, cmd.Env)
	}

	// "Default" model and profile: no profile or model flags at all.
	cmd, err = providertest.Start(context.Background(), a, provider.NewSession{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Args, []string{"--cd", dir}) {
		t.Fatalf("default args %v", cmd.Args)
	}

	// An effort without a model is refused rather than guessed.
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Effort: "high"}); err == nil {
		t.Fatal("effort without a model was started")
	}
	// A profile that is not a profile file is refused.
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Profile: "missing"}); err == nil {
		t.Fatal("an unlisted profile was started")
	}
}

// REQ-OPN-008: an unreadable model list leaves only Default to offer, and
// Default starts without it.
func TestStartWithDefaultsNeedsNoModelList(t *testing.T) {
	fakeCodex(t)
	home := t.TempDir()
	project := t.TempDir()
	a := New(home)
	catalogue(a, "")
	if _, err := a.StartCatalog(context.Background(), ""); err == nil {
		t.Fatal("the fixture should make the model list unreadable")
	}
	cmd, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: project})
	if err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
	if want := []string{"--cd", project}; !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args %q, want %q", cmd.Args, want)
	}
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: project, Model: "gpt-6.1-sol"}); err == nil {
		t.Fatal("a model was started without a model list to check it against")
	}
}
