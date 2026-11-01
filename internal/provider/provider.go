// Package provider defines the contract every agent tool integration
// implements. Each tool lives in its own subpackage; callers iterate a
// Registry instead of switching on session.Provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

// Meta is the provider's display vocabulary.
type Meta struct {
	Name        string // short filter label, such as "Claude"
	Title       string // product name, such as "Claude Code"
	Short       string // two-letter table code
	Source      string // diagnostics line describing where sessions come from
	DeleteScope string // what native deletion removes
}

// Adapter discovers the sessions of one agent tool and opens them in it.
type Adapter interface {
	ID() session.Provider
	Meta() Meta
	Scan(ctx context.Context, now time.Time) Result
	// Resume returns the command that opens the native session in
	// s.Directory.
	Resume(ctx context.Context, s session.Session) (Command, error)
	// MatchesProcess reports whether a process's argv is this tool running s.
	MatchesProcess(args []string, s session.Session) bool
}

// Previewer is implemented by adapters that load recent activity on demand.
type Previewer interface {
	Preview(ctx context.Context, id string) (string, error)
}

// ProcessFinder is implemented by adapters that know which process runs a
// session, such as from a status file the running agent keeps. It finds a
// session's tmux pane however the agent was started, where MatchesProcess
// only finds what its arguments name.
type ProcessFinder interface {
	// SessionProcess returns the ID of the live process running s, or false
	// when none is known.
	SessionProcess(s session.Session) (pid int, ok bool)
}

// Deleter is implemented by adapters that can permanently delete native history.
type Deleter interface {
	Delete(ctx context.Context, s session.Session) error
}

type Result struct {
	Provider session.Provider
	Sessions []session.Session
	Warnings []string
	Err      error
}

// Registry lists the configured adapters in display order.
type Registry []Adapter

func (r Registry) Get(id session.Provider) Adapter {
	for _, a := range r {
		if a.ID() == id {
			return a
		}
	}
	return nil
}

// Command is a native process launch. Arguments are never shell-interpolated.
type Command struct {
	Name      string
	Args      []string
	Directory string
	// Env entries are KEY=VALUE and override the inherited environment.
	Env []string
	// Unset names variables to drop from the inherited environment.
	Unset []string
	// Created is a session the agent created before the command runs, such
	// as an OpenCode session made with the chosen model. It is kept even if
	// the command fails, so the dashboard says so.
	Created string
}

// Exec builds the process. With no Env or Unset it leaves the environment
// nil, so the child inherits the parent's exactly as before.
func (c Command) Exec() *exec.Cmd {
	return c.command(context.Background())
}

func (c Command) command(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Directory
	if len(c.Env) > 0 || len(c.Unset) > 0 {
		cmd.Env = c.environment(os.Environ())
	}
	return cmd
}

// environment applies Unset and Env to inherited. An Env key replaces an
// inherited one outright, so a stale value never wins.
func (c Command) environment(inherited []string) []string {
	drop := map[string]bool{}
	for _, name := range c.Unset {
		drop[name] = true
	}
	for _, entry := range c.Env {
		key, _, _ := strings.Cut(entry, "=")
		drop[key] = true
	}
	env := make([]string, 0, len(inherited)+len(c.Env))
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			env = append(env, entry)
		}
	}
	return append(env, c.Env...)
}

// Runner executes a native CLI and returns its standard output. Adapters accept
// one so tests can substitute provider responses.
type Runner func(context.Context, string, ...string) ([]byte, error)

// waitDelay is how long RunCommand waits for a command's output pipes to
// close once the command has exited or been cancelled. A child that leaves a
// grandchild holding a pipe (an OpenCode server it started, say) would
// otherwise block Run until the grandchild exits. A variable so a test can
// shorten it.
var waitDelay = 5 * time.Second

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return RunCommand(ctx, Command{Name: name, Args: args})
}

// RunCommand runs c, with its environment, and returns its standard output.
// It has the output limit of Run.
func RunCommand(ctx context.Context, c Command) ([]byte, error) {
	// Output past the limit cancels the command rather than filling memory.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := c.command(ctx)
	stderr := NewHeadBuffer(MaxErrorSize)
	stdout := NewLimitedBuffer(MaxOutputSize, cancel)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	// The command succeeded but something it started still held a pipe: its
	// own output is complete, so this is not a failure.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if stdout.Exceeded() {
		return nil, fmt.Errorf("%s: output exceeds %d bytes", c.Name, MaxOutputSize)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w %s", c.Name, err, session.Text(stderr.String(), 240))
	}
	return stdout.Bytes(), nil
}
