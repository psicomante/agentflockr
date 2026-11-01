package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

const resumeID = "11111111-2222-3333-4444-555555555555"

// transcriptIn is a session file in home's project folders, the layout Scan
// reads. Resume does not open it, so it need not exist.
func transcriptIn(home string) string {
	return filepath.Join(home, "projects", "project-a", resumeID+".jsonl")
}

func resumeSession(t *testing.T, transcript string) session.Session {
	t.Helper()
	return session.Session{ID: resumeID, Provider: session.Claude, Directory: folder(t, "project"), Transcript: transcript}
}

func TestResumeUsesTheTranscriptsHome(t *testing.T) {
	standIn(t, "claude")
	home := folder(t, ".claude")
	work := folder(t, ".claude-work")
	a := New(home, work)
	a.DefaultHome = home
	s := resumeSession(t, transcriptIn(work))

	cmd, err := a.Resume(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "claude" || !reflect.DeepEqual(cmd.Args, []string{"--resume", transcriptIn(work)}) {
		t.Fatalf("command: %s %v", cmd.Name, cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"CLAUDE_CONFIG_DIR=" + work}) || cmd.Unset != nil {
		t.Fatalf("environment: env %v unset %v", cmd.Env, cmd.Unset)
	}
}

// The default home unsets the variable however it is spelled: plainly, with a
// trailing slash, or through a symlink.
func TestResumeLeavesTheDefaultHomeUnset(t *testing.T) {
	home := folder(t, ".claude")
	link := filepath.Join(t.TempDir(), "claude-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for name, spelled := range map[string]string{
		"plain":          home,
		"trailing slash": home + string(os.PathSeparator),
		"symlink":        link,
	} {
		t.Run(name, func(t *testing.T) {
			standIn(t, "claude")
			a := New(spelled)
			a.DefaultHome = home
			cmd, err := a.Resume(context.Background(), resumeSession(t, transcriptIn(spelled)))
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Env != nil || !reflect.DeepEqual(cmd.Unset, []string{"CLAUDE_CONFIG_DIR"}) {
				t.Fatalf("the default home must unset CLAUDE_CONFIG_DIR, got env %v unset %v", cmd.Env, cmd.Unset)
			}
		})
	}
}

// Without DefaultHome the variable was set at the default home on purpose, so
// it is kept, as when starting.
func TestResumeKeepsAnInheritedDefaultConfigDir(t *testing.T) {
	standIn(t, "claude")
	home := folder(t, ".claude")
	a := New(home)
	cmd, err := a.Resume(context.Background(), resumeSession(t, transcriptIn(home)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"CLAUDE_CONFIG_DIR=" + home}) || cmd.Unset != nil {
		t.Fatalf("environment: env %v unset %v", cmd.Env, cmd.Unset)
	}
}

func TestResumeUsesTheProfileCommand(t *testing.T) {
	standIn(t, "claude-work")
	work := folder(t, ".claude-work")
	a := New(folder(t, ".claude"))
	a.Profiles = []Profile{{Name: "work", Home: work, Command: []string{"claude-work", "--verbose"}}}

	cmd, err := a.Resume(context.Background(), resumeSession(t, transcriptIn(work)))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "claude-work" || !reflect.DeepEqual(cmd.Args, []string{"--verbose", "--resume", transcriptIn(work)}) {
		t.Fatalf("command: %s %v", cmd.Name, cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"CLAUDE_CONFIG_DIR=" + work}) {
		t.Fatalf("environment: env %v", cmd.Env)
	}
}

// With profiles configured, a home that is not a profile still resumes with
// its own home, so its sessions are not opened under the wrong account.
func TestResumeFindsHomesThatAreNotProfiles(t *testing.T) {
	standIn(t, "claude")
	home := folder(t, ".claude")
	work := folder(t, ".claude-work")
	a := New(home)
	a.Profiles = []Profile{{Name: "work", Home: work}}

	cmd, err := a.Resume(context.Background(), resumeSession(t, transcriptIn(home)))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "claude" || !reflect.DeepEqual(cmd.Args, []string{"--resume", transcriptIn(home)}) {
		t.Fatalf("command: %s %v", cmd.Name, cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"CLAUDE_CONFIG_DIR=" + home}) {
		t.Fatalf("environment: env %v", cmd.Env)
	}
}

func TestResumeOutsideEveryHomeIsUnchanged(t *testing.T) {
	standIn(t, "claude")
	a := New(folder(t, ".claude"))
	elsewhere := transcriptIn(folder(t, "elsewhere"))
	cmd, err := a.Resume(context.Background(), resumeSession(t, elsewhere))
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Command{Name: "claude", Args: []string{"--resume", elsewhere}, Directory: cmd.Directory}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("got %+v, want %+v", cmd, want)
	}
}
