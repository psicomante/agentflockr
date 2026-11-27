package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
)

type manyFileInspector struct{ *providertest.Fake }

func (m *manyFileInspector) Inspect(context.Context, session.Session) (provider.Info, error) {
	info := provider.Info{FilesKnown: true}
	for range provider.MaxInfoFiles + 1 {
		info.Files = append(info.Files, provider.FileChange{Path: "fixture", Status: "edited"})
	}
	return info, nil
}

type partialInspector struct {
	*providertest.Fake
	selection bool
}

type mergedInspector struct {
	*providertest.Fake
	live, file provider.Info
}

func (p *mergedInspector) Info(session.Session) (provider.Info, bool) { return p.live, true }
func (p *mergedInspector) Inspect(context.Context, session.Session) (provider.Info, error) {
	return p.file, nil
}

func TestInspectLetsTheModOverrideTheTranscript(t *testing.T) {
	p := &mergedInspector{Fake: &providertest.Fake{Provider: session.Claude}, live: provider.Info{Model: "new", Mode: "auto", Version: "live", ContextKnown: true, ContextTokens: 0, ContextWindow: 1000, Limits: []provider.Limit{{Kind: "five_hour", PercentUsed: 0}}}, file: provider.Info{Model: "old", Mode: "plan", Effort: "low", Version: "file", ContextKnown: true, ContextTokens: 500, Profile: "/profile", FilesKnown: true}}
	e := Engine{Providers: provider.Registry{p}}
	info := e.Inspect(t.Context(), session.Session{Provider: session.Claude})
	if info.Model != "new" || info.Mode != "auto" || info.Version != "live" || info.Effort != "low" || info.Profile != "/profile" || info.ContextTokens != 0 || !info.ContextKnown || info.ContextWindow != 1000 || len(info.Limits) != 1 || !info.FilesKnown {
		t.Fatal(info)
	}
	p.live = provider.Info{Model: "old", ContextWindow: 1000}
	info = e.Inspect(t.Context(), session.Session{Provider: session.Claude})
	if !info.ContextKnown || info.ContextTokens != 500 || info.ContextWindow != 1000 || info.ContextPercent != 50 {
		t.Fatal(info)
	}
	p.live.Model = "switched"
	info = e.Inspect(t.Context(), session.Session{Provider: session.Claude})
	if info.ContextKnown || info.ContextTokens != 0 {
		t.Fatal("borrowed usage across a model switch", info)
	}
	p.file.SelectionRead = true
	info = e.Inspect(t.Context(), session.Session{Provider: session.Claude})
	if info.Model != "old" || info.Mode != "plan" || info.ContextTokens != 500 || info.ContextWindow != 0 {
		t.Fatal("authoritative selection changed", info)
	}
}

func (p *partialInspector) Info(session.Session) (provider.Info, bool) {
	return provider.Info{Mode: "previous", Model: "previous", Connection: "previous", Version: "reported-version", ContextTokens: 100, ContextKnown: true}, true
}
func (p *partialInspector) Inspect(context.Context, session.Session) (provider.Info, error) {
	return provider.Info{SelectionRead: p.selection, FilesKnown: true}, nil
}
func TestInspectionHonorsFieldOwnershipAndClearsUnavailableSelection(t *testing.T) {
	for _, selection := range []bool{false, true} {
		engine := Engine{Providers: provider.Registry{&partialInspector{Fake: &providertest.Fake{Provider: session.Codex}, selection: selection}}}
		info := engine.Inspect(t.Context(), session.Session{Provider: session.Codex})
		if selection {
			if info.Mode != "" || info.Model != "" || info.Connection != "" || info.ContextKnown || info.ContextTokens != 0 {
				t.Fatalf("stale selection preserved: %+v", info)
			}
		} else if info.Mode != "previous" || info.ContextTokens != 100 {
			t.Fatalf("file evidence erased live metadata: %+v", info)
		}
		if info.Version != "reported-version" || !info.FilesKnown {
			t.Fatalf("unrelated fields lost: %+v", info)
		}
	}
}

func TestInspectionBoundsFilesWithoutClaimingTheListIsComplete(t *testing.T) {
	engine := Engine{Providers: provider.Registry{&manyFileInspector{&providertest.Fake{Provider: session.Codex}}}}
	info := engine.Inspect(t.Context(), session.Session{Provider: session.Codex})
	if len(info.Files) != provider.MaxInfoFiles || !strings.Contains(strings.Join(info.Problems, " "), "shortened") {
		t.Fatalf("unbounded or silently incomplete: %+v", info)
	}
}

func TestInspectReadsALinkedWorktreeWithoutAttributingGitChanges(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked")
	git("commit", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked tree")
	git("worktree", "add", "-b", "feature", linked)
	if err := os.WriteFile(filepath.Join(linked, "untracked file"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{}
	info := engine.Inspect(t.Context(), session.Session{Directory: linked, Provider: session.Codex, ID: "fixture"})
	if !session.SameDirectory(info.Worktree, linked) || info.Branch != "feature" || !info.GitKnown || len(info.GitFiles) != 1 || info.GitFiles[0].Path != "untracked file" {
		t.Fatalf("info: %+v", info)
	}
	if info.FilesKnown || len(info.Files) != 0 || info.ContextKnown || len(info.Limits) != 0 {
		t.Fatalf("invented attribution or usage: %+v", info)
	}
	git("-C", linked, "checkout", "--detach")
	info = engine.Inspect(t.Context(), session.Session{Directory: linked})
	if !strings.HasPrefix(info.Branch, "Detached HEAD") {
		t.Fatalf("detached: %+v", info)
	}
}

func TestGitStatusKeepsRenameDestinationsAndUnusualNames(t *testing.T) {
	got := parseGitStatus("R  new name\x00old name\x00 M tab\tand\nnewline\x00?? untracked\x00D  gone\x00")
	want := []provider.FileChange{{Path: "new name", Status: "R"}, {Path: "tab\tand\nnewline", Status: "M"}, {Path: "untracked", Status: "??"}, {Path: "gone", Status: "D"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestGitInspectionIsReadOnlyAndDisablesHooks(t *testing.T) {
	var info provider.Info
	gitInfo(context.Background(), "/fixture", &info, func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if name != "git" || !strings.Contains(joined, "core.fsmonitor=false") || !strings.Contains(joined, "core.hooksPath=/dev/null") || !strings.Contains(joined, "--no-optional-locks") {
			t.Fatalf("unsafe command %s %v", name, args)
		}
		switch {
		case strings.Contains(joined, "--show-toplevel"):
			return []byte("/fixture\n"), nil
		case strings.Contains(joined, "--git-common-dir"):
			return []byte("/fixture/.git\n"), nil
		case strings.Contains(joined, "symbolic-ref"):
			return []byte("main\n"), nil
		case strings.Contains(joined, "status --porcelain=v1 -z"):
			return nil, nil
		default:
			t.Fatalf("unexpected Git operation %v", args)
			return nil, nil
		}
	})
	if !info.GitKnown || len(info.GitFiles) != 0 {
		t.Fatalf("clean: %+v", info)
	}
}

func TestInspectMissingGitDoesNotInventZeroUsageOrCleanWorktree(t *testing.T) {
	engine := &Engine{}
	info := engine.Inspect(t.Context(), session.Session{Directory: t.TempDir()})
	if info.GitKnown || info.ContextKnown || info.Worktree != "" || len(info.Problems) == 0 {
		t.Fatalf("missing metadata: %+v", info)
	}
}

func TestDemoInspectionNeverReadsTheFilesystem(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	info := (&Engine{Demo: true}).Inspect(t.Context(), session.Session{Directory: "/does-not-exist", Provider: session.Codex, ID: "demo"})
	if info.Folder != "/does-not-exist" || len(info.Problems) != 0 {
		t.Fatalf("demo did external work: %+v", info)
	}
}
