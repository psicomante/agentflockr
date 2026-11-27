package provider

import (
	"slices"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

// The parts of a session's transcript that agentflockr reads: the
// dashboard's session information counts their file changes, and the phone
// app shows them.

// Message is one entry of a session's transcript.
type Message struct {
	ID string
	// Role is "user", "assistant", "shell" for a command the person ran
	// in the agent's shell, or "command" for one of the agent's own
	// commands, such as Claude Code's /model.
	Role string
	// Text is what was said; for a shell entry or a command, the command.
	Text string
	// Calls are the tool calls an assistant message made, in order.
	Calls []Call
	// Output and Exit belong to shell entries and commands. Exit is nil
	// while running, and also when the agent records no exit code; Done
	// then says the command has ended.
	Output string
	Exit   *int
	Done   bool
	// Error says why the turn failed, such as a provider quota.
	Error string
	// Images are pictures in the message, such as a pasted screenshot.
	Images []Image
	At     time.Time
}

// Call is one tool call as the phone shows it: a one-line summary that
// opens into the full input and output.
type Call struct {
	// Files names explicit file-edit targets before display input is shortened.
	// Only a completed call is evidence that the edit took effect.
	Files   []string
	Name    string
	Summary string // the command, file or pattern the call acted on
	// Command is the shell command a call ran, whole, so it can be read
	// as written rather than as an escaped JSON string; Input then leaves
	// it out.
	Command string
	Status  string // "running", "completed" or "error"
	Input   string // the arguments, as indented JSON
	Output  string // what the tool returned, or its error
	// Images are pictures the tool returned, such as a file it read.
	Images []Image
}

// Image is a picture in a transcript. Its bytes are fetched on their own,
// by ID, from an ImageReader, so a transcript stays small.
type Image struct {
	// ID is opaque to everyone but its provider, and made only of letters,
	// digits, '-' and '_', so it fits in a URL path as it is.
	ID   string
	MIME string
	Name string // such as the file's name, when known
}

// summaryKeys are the arguments that best say what a call acted on, in
// order of preference.
var summaryKeys = []string{"command", "cmd", "filePath", "file_path", "path", "pattern", "query", "url", "description", "prompt"}

// CallSummary picks the argument that names what a call acted on, such
// as a shell command or a file, falling back to the first question of a
// question call and then to any short text argument.
func CallSummary(input map[string]any) string {
	for _, key := range summaryKeys {
		if v, ok := input[key].(string); ok && v != "" {
			return session.Text(v, 160)
		}
	}
	if questions, ok := input["questions"].([]any); ok && len(questions) > 0 {
		if q, ok := questions[0].(map[string]any); ok {
			if text, ok := q["question"].(string); ok {
				return session.Text(text, 160)
			}
		}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if v, ok := input[key].(string); ok && v != "" {
			return session.Text(v, 160)
		}
	}
	return ""
}

// CallCommand takes a shell call's command out of its arguments, so it
// can be shown on its own, and returns it. A command given as a list of
// words, as Codex writes it, is joined with spaces. Input without a
// command is left as it is.
func CallCommand(input map[string]any) string {
	for _, key := range []string{"command", "cmd"} {
		var command string
		switch v := input[key].(type) {
		case string:
			command = v
		case []any:
			words := make([]string, 0, len(v))
			for _, w := range v {
				s, ok := w.(string)
				if !ok {
					return ""
				}
				words = append(words, s)
			}
			command = strings.Join(words, " ")
		}
		if strings.TrimSpace(command) != "" {
			delete(input, key)
			return command
		}
	}
	return ""
}

// Tail keeps the last limit runes of multi-line text, marking the cut.
func Tail(text string, limit int) string {
	runes := []rune(strings.TrimRight(text, "\n"))
	if len(runes) > limit {
		text = "…" + string(runes[len(runes)-limit:])
	} else {
		text = string(runes)
	}
	return session.Block(text, 0)
}
