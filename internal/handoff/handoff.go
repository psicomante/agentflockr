// Package handoff finds an already running native session in tmux.
package handoff

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
)

// Tmux finds a pane whose process tree contains a process accepted by matches,
// selects it, and returns the command that attaches to its tmux session.
// matches gets each process's ID and arguments, so an agent can recognize its
// session by either. The match must identify the exact session: a project
// directory alone is ambiguous when several sessions are open.
func Tmux(ctx context.Context, matches func(pid int, args []string) bool, run provider.Runner) (provider.Command, bool, error) {
	if run == nil {
		run = provider.Run
	}
	panes, err := run(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{session_id}\t#{pane_pid}")
	// No tmux, or no tmux server, means the session is in no pane.
	if err != nil || strings.TrimSpace(string(panes)) == "" {
		return provider.Command{}, false, nil
	}
	ps, err := run(ctx, "ps", "-axo", "pid=,ppid=,args=")
	if err != nil {
		// With panes open, the session may be running in one. Resuming
		// anyway could start a second agent on it, so say what failed.
		return provider.Command{}, false, fmt.Errorf("could not list the running processes to see whether the session is already open in tmux: %w", err)
	}
	parents := map[int]int{}
	var candidates []int
	for _, line := range strings.Split(string(ps), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// ps prints whatever a process's arguments hold, so a line can be
		// odd; one without numeric IDs names no process and is skipped.
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		parents[pid] = parent
		if pid > 0 && matches(pid, fields[2:]) {
			candidates = append(candidates, pid)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(panes)), "\n") {
		p := strings.Split(line, "\t")
		if len(p) != 3 || !strings.HasPrefix(p[0], "%") || !strings.HasPrefix(p[1], "$") {
			continue
		}
		// A pane whose process ID cannot be read has no tree to search.
		root, err := strconv.Atoi(p[2])
		if err != nil {
			continue
		}
		for _, pid := range candidates {
			for hops := 0; pid > 1 && hops < 100; hops++ {
				if pid == root {
					if _, err := run(ctx, "tmux", "select-window", "-t", p[0]); err != nil {
						return provider.Command{}, false, fmt.Errorf("found its tmux pane but could not switch to it: %w", err)
					}
					if _, err := run(ctx, "tmux", "select-pane", "-t", p[0]); err != nil {
						return provider.Command{}, false, fmt.Errorf("found its tmux pane but could not switch to it: %w", err)
					}
					action := "attach-session"
					if os.Getenv("TMUX") != "" {
						action = "switch-client"
					}
					return provider.Command{Name: "tmux", Args: []string{action, "-t", p[1]}}, true, nil
				}
				pid = parents[pid]
			}
		}
	}
	return provider.Command{}, false, nil
}
