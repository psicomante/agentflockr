package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Profile is a launch context from the settings file: a Claude Code home and
// the command that runs Claude Code with it. Home is absolute; Command is the
// executable and its arguments, or nil for ["claude"].
type Profile struct {
	Name    string
	Home    string
	Command []string
}

// launch is a profile as Start needs it: the raw home and command, which the
// form never sees.
type launch struct {
	id      string
	name    string
	home    string
	command []string
}

// Models and efforts are the aliases and levels `claude --help` names, plus
// haiku (see docs/notes/hardcoded-rules.md). Claude Code has no command that
// lists them.
var (
	startEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	startModels  = []provider.Model{
		{ID: "fable", Name: "Fable", Efforts: startEfforts},
		{ID: "opus", Name: "Opus", Efforts: startEfforts},
		{ID: "sonnet", Name: "Sonnet", Efforts: startEfforts},
		{ID: "haiku", Name: "Haiku", Efforts: startEfforts},
	}
)

// launches lists the profiles. The configured ones come in file order. With
// none, each home the adapter reads is a profile named by its folder, which
// is the home's own path as its ID: those names can repeat across folders.
func (a *Adapter) launches() []launch {
	if len(a.Profiles) > 0 {
		var out []launch
		for _, p := range a.Profiles {
			out = append(out, launch{id: p.Name, name: p.Name, home: p.Home, command: p.Command})
		}
		return out
	}
	var out []launch
	for _, home := range a.homes() {
		out = append(out, launch{id: home, name: filepath.Base(home), home: home, command: []string{"claude"}})
	}
	return out
}

// StartProfiles reports each profile and why it cannot be used, if it cannot.
// It reads folders and looks up commands; it starts nothing.
func (a *Adapter) StartProfiles(context.Context) ([]provider.Profile, error) {
	var out []provider.Profile
	for _, l := range a.launches() {
		out = append(out, provider.Profile{
			ID:      l.id,
			Name:    session.Text(l.name, 64),
			Detail:  session.Text(l.home+" · "+strings.Join(l.commandOrDefault(), " "), 200),
			Problem: problem(l),
		})
	}
	return out, nil
}

func (l launch) commandOrDefault() []string {
	if len(l.command) == 0 {
		return []string{"claude"}
	}
	return l.command
}

// problem says why a profile cannot start now, or "" when it can. Start
// checks it again, so a profile broken since the form loaded is refused.
func problem(l launch) string {
	if info, err := os.Stat(l.home); err != nil || !info.IsDir() {
		return "its home folder does not exist: " + session.Text(l.home, 200)
	}
	if _, err := exec.LookPath(l.commandOrDefault()[0]); err != nil {
		return "cannot find its command " + session.Text(l.commandOrDefault()[0], 100)
	}
	return ""
}

// StartCatalog lists the built-in models and efforts for a known profile.
func (a *Adapter) StartCatalog(ctx context.Context, profile string) (provider.Catalog, error) {
	if _, ok, _ := a.findLaunch(ctx, profile); !ok {
		return provider.Catalog{}, fmt.Errorf("that profile is no longer offered")
	}
	return provider.Catalog{Models: startModels, DefaultEfforts: startEfforts}, nil
}

// Start returns the profile's command with the chosen model and effort. The
// home is passed as CLAUDE_CONFIG_DIR, except for the default home: Claude
// Code keeps its account file beside that folder when the variable is unset,
// but inside it when set, so setting it there would look like a signed-out
// install.
//
// n has passed provider.CheckStart; only the profile's home and command are
// looked up here.
func (a *Adapter) Start(ctx context.Context, n provider.NewSession) (provider.Command, error) {
	l, ok, err := a.findLaunch(ctx, n.Profile)
	if err != nil {
		return provider.Command{}, err
	}
	if !ok {
		return provider.Command{}, fmt.Errorf("that profile is no longer offered")
	}
	command := l.commandOrDefault()
	args := append([]string(nil), command[1:]...)
	if n.Model != "" {
		args = append(args, "--model", n.Model)
	}
	if n.Effort != "" {
		args = append(args, "--effort", n.Effort)
	}
	cmd := provider.Command{Name: command[0], Args: args, Directory: n.Directory}
	// Compared as folders, not strings: "~/.claude/" or a symlink to it is
	// still the default home, and setting the variable there would switch
	// Claude Code to another account file.
	if a.DefaultHome != "" && session.SameDirectory(l.home, a.DefaultHome) {
		cmd.Unset = []string{"CLAUDE_CONFIG_DIR"}
	} else {
		cmd.Env = []string{"CLAUDE_CONFIG_DIR=" + l.home}
	}
	return cmd, nil
}

// findLaunch looks the ID up among the profiles. It never treats the ID as a
// path, so a value from the form cannot select a folder that is not listed.
func (a *Adapter) findLaunch(ctx context.Context, id string) (launch, bool, error) {
	if err := ctx.Err(); err != nil {
		return launch{}, false, err
	}
	for _, l := range a.launches() {
		if l.id == id {
			return l, true, nil
		}
	}
	return launch{}, false, nil
}
