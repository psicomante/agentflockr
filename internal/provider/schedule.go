package provider

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/psicomante/agentflockr/internal/session"
)

// Prompter sends a scheduled prompt into an existing session without a
// terminal: the person wrote the prompt and chose when it runs. It returns
// once the agent has finished the turn, or has accepted the prompt when the
// agent cannot tell when the turn ends.
type Prompter interface {
	Prompt(ctx context.Context, s session.Session, p ScheduledPrompt) error
}

// ScheduledPrompt is what a scheduled run sends.
type ScheduledPrompt struct {
	Text string
	// Connection is an OpenCode credential ID to activate for this run only;
	// the connection active before is restored afterwards. Empty keeps the
	// current one. Other agents ignore it.
	Connection string
	// Model and Effort apply to this run only, as the agent spells them in
	// its catalogue. Empty leaves the choice to the session or profile.
	Model, Effort string
}

// PromptCatalog lists the models and efforts a scheduled prompt into s may
// choose. Reading it changes nothing.
type PromptCatalog interface {
	PromptCatalog(ctx context.Context, s session.Session) (Catalog, error)
}

// ProfilePrompter sends a scheduled prompt through one of the agent's
// profiles, outside any session: each run is a short new conversation that
// the agent does not keep, run in dir, a folder agentflockr owns. It exists to
// keep the profile's usage window running. profile is a StartProfiles ID.
type ProfilePrompter interface {
	Starter
	PromptProfile(ctx context.Context, profile, dir string, p ScheduledPrompt) error
}

// Connection is a saved OpenCode credential a scheduled prompt may switch to.
type Connection struct {
	ID   string // opaque credential ID; never a credential value
	Name string // label and authentication method, already made safe
	// Integration is the OpenCode integration the credential signs in to,
	// such as "anthropic". Activating it changes that integration only.
	Integration string
	Active      bool
}

// ConnectionLister lists the saved connections a scheduled prompt can use.
type ConnectionLister interface {
	Connections(ctx context.Context) ([]Connection, error)
}

// CleanPrompt makes a scheduled prompt's text safe to pass to an agent: no
// control characters but newlines and tabs, trimmed, not empty, at most
// MaxMessage runes. A text that starts with "-" is refused, because Claude
// Code takes the prompt as a command argument and would read it as an option.
func CleanPrompt(text string) (string, error) {
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, strings.ReplaceAll(text, "\r\n", "\n"))
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", fmt.Errorf("%w: the prompt is empty", ErrInvalid)
	case utf8.RuneCountInString(text) > MaxMessage:
		return "", fmt.Errorf("%w: the prompt is longer than %d characters", ErrInvalid, MaxMessage)
	case strings.HasPrefix(text, "-"):
		return "", fmt.Errorf("%w: the prompt cannot start with \"-\"", ErrInvalid)
	}
	return text, nil
}

// SendMode is how a message reaches a session. A scheduled prompt is
// always queued; the phone app also steers.
type SendMode string

const (
	// Queue delivers the message after the current turn.
	Queue SendMode = "queue"
	// Steer delivers it at the next step of the current turn, or at once
	// when the session is idle.
	Steer SendMode = "steer"
)

// Values lists every mode, for the phone app's generated types.
func (SendMode) Values() []string { return []string{string(Queue), string(Steer)} }
