package handoff

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/codex"
	"github.com/psicomante/agentflockr/internal/session"
)

func TestTmuxMatchesExactProviderSessionThroughProcessParents(t *testing.T) {
	t.Setenv("TMUX", "")
	s := session.Session{ID: "session-123", Provider: session.Codex}
	for _, id := range []string{"session-123", "session-1234"} {
		run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "ps" {
				return []byte("100 1 zsh\n101 100 codex resume " + id + "\n"), nil
			}
			if args[0] == "list-panes" {
				return []byte("%2\t$1\t100\n"), nil
			}
			return nil, nil
		}
		matches := func(_ int, args []string) bool { return codex.New("").MatchesProcess(args, s) }
		plan, ok, err := Tmux(context.Background(), matches, run)
		if err != nil {
			t.Fatal(err)
		}
		if ok != (id == s.ID) {
			t.Fatalf("incorrect exact match for %s", id)
		}
		if ok && !reflect.DeepEqual(plan.Args, []string{"attach-session", "-t", "$1"}) {
			t.Fatalf("unexpected target: %+v", plan)
		}
	}
}

// Lines without numeric IDs, from ps or tmux, name no process or pane; they
// are skipped and the real pane is still found.
func TestTmuxSkipsMalformedLines(t *testing.T) {
	t.Setenv("TMUX", "")
	s := session.Session{ID: "session-123", Provider: session.Codex}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ps" {
			return []byte("x 1 codex resume session-123\n101 y codex resume session-123\n100 1 zsh\n102 100 codex resume session-123\n"), nil
		}
		if args[0] == "list-panes" {
			return []byte("%1\t$0\tz\n%2\t$1\t100\n"), nil
		}
		return nil, nil
	}
	matches := func(_ int, args []string) bool { return codex.New("").MatchesProcess(args, s) }
	plan, ok, err := Tmux(context.Background(), matches, run)
	if err != nil || !ok || !reflect.DeepEqual(plan.Args, []string{"attach-session", "-t", "$1"}) {
		t.Fatalf("got %+v %v %v, want the pane of process 102", plan, ok, err)
	}
}

// When ps fails, the session may be running in a pane; opening stops with the
// reason instead of resuming a second copy. Without tmux there is no pane, so
// that is not a failure.
func TestTmuxReportsAFailedProcessLookup(t *testing.T) {
	t.Setenv("TMUX", "")
	run := func(failPS, noTmux bool) provider.Runner {
		return func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch {
			case name == "ps" && failPS:
				return nil, errors.New("ps broke")
			case name == "tmux" && args[0] == "list-panes" && noTmux:
				return nil, errors.New("no server running")
			case name == "tmux" && args[0] == "list-panes":
				return []byte("%2\t$1\t100\n"), nil
			}
			return nil, nil
		}
	}
	never := func(int, []string) bool { return false }
	if _, ok, err := Tmux(context.Background(), never, run(true, false)); err == nil || ok || !strings.Contains(err.Error(), "ps broke") {
		t.Fatalf("a failed ps with panes open must be reported: %v %v", ok, err)
	}
	if _, ok, err := Tmux(context.Background(), never, run(true, true)); err != nil || ok {
		t.Fatalf("no tmux must not be an error: %v %v", ok, err)
	}
}
