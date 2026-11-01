package codex

import (
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestSessionArgumentMatchingSkipsOptionsButNotPrompts(t *testing.T) {
	s := session.Session{ID: "cx-1", Provider: session.Codex}
	if !New("").MatchesProcess([]string{"codex", "resume", "--cd", "/work/project", "cx-1"}, s) {
		t.Fatal("resume options hid the session ID")
	}
	if New("").MatchesProcess([]string{"codex", "resume", "other-session", "explain", "cx-1"}, s) {
		t.Fatal("matched a session ID in a prompt")
	}
}
