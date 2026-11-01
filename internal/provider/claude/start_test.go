package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
)

// standIn writes an executable named name into a new folder, and puts that
// folder first on PATH, so LookPath finds it and nothing real runs.
func standIn(t *testing.T, name string) {
	t.Helper()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func folder(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{t.TempDir()}, parts...)...)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func profileByID(t *testing.T, a *Adapter, id string) provider.Profile {
	t.Helper()
	profiles, err := a.StartProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no profile %q in %+v", id, profiles)
	return provider.Profile{}
}

func TestStartUsesTheProfileHomeAndCommand(t *testing.T) {
	standIn(t, "claude-work")
	work := folder(t, ".claude-work")
	project := folder(t, "project")
	a := New(folder(t, ".claude"))
	a.DefaultHome = filepath.Join(t.TempDir(), ".claude")
	a.Profiles = []Profile{{Name: "work", Home: work, Command: []string{"claude-work", "--verbose"}}}

	cmd, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: project, Profile: "work", Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "claude-work" || !reflect.DeepEqual(cmd.Args, []string{"--verbose", "--model", "opus", "--effort", "high"}) {
		t.Fatalf("command: %s %v", cmd.Name, cmd.Args)
	}
	if cmd.Directory != project {
		t.Fatalf("started in %q, want the project folder %q", cmd.Directory, project)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"CLAUDE_CONFIG_DIR=" + work}) || cmd.Unset != nil {
		t.Fatalf("environment: env %v unset %v", cmd.Env, cmd.Unset)
	}
}

func TestStartLeavesTheDefaultHomeUnset(t *testing.T) {
	standIn(t, "claude")
	home := folder(t, ".claude")
	a := New(home)
	a.DefaultHome = home
	a.Profiles = []Profile{{Name: "default", Home: home}}

	cmd, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: folder(t, "p"), Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "claude" || len(cmd.Args) != 0 {
		t.Fatalf("command: %s %v", cmd.Name, cmd.Args)
	}
	if cmd.Env != nil || !reflect.DeepEqual(cmd.Unset, []string{"CLAUDE_CONFIG_DIR"}) {
		t.Fatalf("the default home must unset CLAUDE_CONFIG_DIR, got env %v unset %v", cmd.Env, cmd.Unset)
	}
}

// The default home spelled another way, with a trailing slash or through a
// symlink, is still the default home: setting CLAUDE_CONFIG_DIR there would
// switch Claude Code to the account file inside the folder.
func TestTheDefaultHomeIsRecognizedHoweverSpelled(t *testing.T) {
	standIn(t, "claude")
	home := folder(t, ".claude")
	link := filepath.Join(t.TempDir(), "claude-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{home + "/", link} {
		a := New(home)
		a.DefaultHome = home
		a.Profiles = []Profile{{Name: "default", Home: spelling}}
		cmd, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: folder(t, "p"), Profile: "default"})
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Env != nil || !reflect.DeepEqual(cmd.Unset, []string{"CLAUDE_CONFIG_DIR"}) {
			t.Errorf("%s: got env %v unset %v, want CLAUDE_CONFIG_DIR unset", spelling, cmd.Env, cmd.Unset)
		}
	}
}

func TestProfilesWithoutSettingsAreTheHomesRead(t *testing.T) {
	standIn(t, "claude")
	root := t.TempDir()
	defaultHome := filepath.Join(root, ".claude")
	personal := filepath.Join(root, ".claude-personal")
	for _, home := range []string{defaultHome, personal} {
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := New(defaultHome, personal)
	profiles, err := a.StartProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Name != ".claude" || profiles[1].Name != ".claude-personal" {
		t.Fatalf("profiles: %+v", profiles)
	}
	for _, p := range profiles {
		if p.Problem != "" || !strings.HasSuffix(p.Detail, " · claude") {
			t.Fatalf("fallback profile %+v", p)
		}
	}
}

func TestUnusableProfilesSayWhy(t *testing.T) {
	standIn(t, "claude")
	present := folder(t, ".claude-ok")
	a := New(present)
	a.Profiles = []Profile{
		{Name: "gone", Home: filepath.Join(t.TempDir(), "missing")},
		{Name: "nocmd", Home: present, Command: []string{"no-such-claude-binary"}},
		{Name: "fine", Home: present},
	}
	if p := profileByID(t, a, "gone"); !strings.HasPrefix(p.Problem, "its home folder does not exist: ") {
		t.Errorf("missing home: %q", p.Problem)
	}
	if p := profileByID(t, a, "nocmd"); p.Problem != "cannot find its command no-such-claude-binary" {
		t.Errorf("missing command: %q", p.Problem)
	}
	if p := profileByID(t, a, "fine"); p.Problem != "" {
		t.Errorf("usable profile reported %q", p.Problem)
	}
	// Start refuses a profile that is broken, even when the form had not
	// noticed.
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: folder(t, "p"), Profile: "nocmd"}); err == nil {
		t.Fatal("an unusable profile started")
	}
}

func TestStartRefusesAnUnknownProfileAndAnOptionShapedModel(t *testing.T) {
	standIn(t, "claude")
	a := New(folder(t, ".claude"))
	a.Profiles = []Profile{{Name: "fine", Home: a.Home}}
	dir := folder(t, "p")
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Profile: "/etc"}); err == nil {
		t.Fatal("a profile that is not listed was started")
	}
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Profile: "fine", Model: "opus"}); err != nil {
		t.Fatalf("offered model refused: %v", err)
	}
	if _, err := providertest.Start(context.Background(), a, provider.NewSession{Directory: dir, Profile: "fine", Model: "--dangerous"}); err == nil {
		t.Fatal("a model that reads as an option was accepted")
	}
}

func TestProfileHomesAreScanned(t *testing.T) {
	shared := folder(t, ".claude")
	work := folder(t, ".claude-work")
	for i, home := range []string{shared, work} {
		project := filepath.Join(home, "projects", "project")
		if err := os.MkdirAll(project, 0700); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"cc-%d\",\"cwd\":\"/project\",\"message\":{\"content\":\"Hello\"}}\n", i)
		if err := os.WriteFile(filepath.Join(project, fmt.Sprintf("cc-%d.jsonl", i)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The work profile's home is also the shared one, so it is read once.
	a := New(shared)
	a.Profiles = []Profile{{Name: "shared", Home: shared}, {Name: "work", Home: work}}
	result := a.Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != 2 {
		t.Fatalf("scan: %+v", result)
	}
	ids := map[string]bool{}
	for _, s := range result.Sessions {
		ids[s.ID] = true
	}
	if !ids["cc-0"] || !ids["cc-1"] {
		t.Fatalf("a profile home was not read: %v", ids)
	}
}
