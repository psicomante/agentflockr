package claude

import "strings"

// The dashboard names a session after the person's first prompt (claude.go),
// and the phone's conversation view (messages.go) tells prompts from Claude
// Code's own records. The check lives in its own file so the dashboard does
// not depend on a file that exists only for the phone.

// origin says who wrote a user record. Older transcripts leave it out.
type origin struct {
	Kind string `json:"kind"`
}

// prompt reports whether a user record with this origin and text is a
// prompt the person wrote: typed, sent from the phone through the plugin as
// theirs, or written before Claude Code recorded an origin (nil), and not a
// record Claude Code writes itself.
func (o *origin) prompt(text string) bool {
	person := o == nil || o.Kind == "human" || o.Kind == "plugin"
	return person && !writtenByClaude(text)
}

// claudeTags open the user records Claude Code writes itself.
var claudeTags = []string{"<command-", "<local-command-", "<bash-", "<task-notification>"}

// writtenByClaude reports whether a user record's text is one Claude Code
// writes itself rather than a prompt: a slash command or its output, a
// shell (!) command or its output, or a task notification. A new session
// after /clear starts with such a record.
func writtenByClaude(text string) bool {
	text = strings.TrimLeft(text, " \t\r\n")
	for _, tag := range claudeTags {
		if strings.HasPrefix(text, tag) {
			return true
		}
	}
	return false
}
