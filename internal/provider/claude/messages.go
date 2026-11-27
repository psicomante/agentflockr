package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

const (
	// maxMessages bounds what one request returns.
	maxMessages = 200
	// transcriptTail is how much of a transcript's end is read: enough for
	// the latest messages, without reading a long session whole.
	transcriptTail = 8 << 20
	// Bounds for what a transcript entry carries to the phone.
	maxInput  = 2000
	maxOutput = 4000
)

// Messages returns up to limit recent prompts and replies of a session,
// oldest first, read from the end of its transcript, with the commands the
// person ran in its shell (!) and its own slash commands. Thinking, and
// prompts that came from elsewhere than the person, are left out; a task
// notification instead ends the background call it reports on. Tool calls
// carry their input and output.
func (a *Adapter) Messages(ctx context.Context, item session.Session, limit int) ([]provider.Message, error) {
	if !provider.ValidID(item.ID) {
		return nil, provider.ErrSessionGone
	}
	path := a.transcript(item.ID)
	if path == "" {
		return nil, provider.ErrSessionGone
	}
	data, err := readTail(path, transcriptTail)
	if err != nil {
		return nil, err
	}
	out := messages(data)
	limit = min(max(limit, 1), maxMessages)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, ctx.Err()
}

// transcript finds a session's transcript in any project folder of any
// home; the bare ID does not say which.
func (a *Adapter) transcript(id string) string {
	for _, home := range a.homes() {
		matches, _ := filepath.Glob(filepath.Join(home, "projects", "*", id+".jsonl"))
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// readTail returns the last limit bytes of a file, from the first whole
// line on.
func readTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(info.Size()-limit, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
			data = data[cut+1:]
		}
	}
	return data, nil
}

type transcriptRecord struct {
	Effort         string  `json:"effort"`
	PermissionMode string  `json:"permissionMode"`
	Version        string  `json:"version"`
	Type           string  `json:"type"`
	UUID           string  `json:"uuid"`
	Timestamp      string  `json:"timestamp"`
	IsMeta         bool    `json:"isMeta"`
	IsSidechain    bool    `json:"isSidechain"`
	IsAPIError     bool    `json:"isApiErrorMessage"`
	Origin         *origin `json:"origin"`
	// ToolUseResult is Claude Code's own account of a tool result: an
	// object for most tools, plain text for some.
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       struct {
		Model   string          `json:"model"`
		Usage   json.RawMessage `json:"usage"`
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Source    *imageSource    `json:"source"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// messages turns transcript lines into messages. Claude Code writes each
// block of an assistant reply as its own record, under one message ID, and
// each tool result as a user record after it.
func messages(data []byte) []provider.Message {
	var out []provider.Message
	calls := map[string][2]int{} // tool_use ID: message and call index
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var r transcriptRecord
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.IsSidechain || (r.Type != "user" && r.Type != "assistant") {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
		var text string
		var blocks []contentBlock
		if json.Unmarshal(r.Message.Content, &text) != nil {
			json.Unmarshal(r.Message.Content, &blocks)
		}
		if r.Type == "user" {
			if r.IsMeta {
				continue
			}
			if handled, kept := localRecord(r, text, at, out, calls); handled {
				out = kept
				continue
			}
			var images []provider.Image
			for n, b := range blocks {
				switch b.Type {
				case "tool_result":
					if where, ok := calls[b.ToolUseID]; ok {
						call := &out[where[0]].Calls[where[1]]
						call.Output, call.Status = provider.Tail(resultText(b.Content), maxOutput), "completed"
						if b.IsError {
							call.Status = "error"
						} else if inBackground(r.ToolUseResult) {
							// The call only started its work; a task
							// notification says when it ends.
							call.Status = "running"
						}
						call.Images = resultImages(r.UUID, n, b.Content)
					}
				case "text":
					text += b.Text
				case "image":
					if img, ok := imageOf(b, r.UUID, n, -1); ok {
						images = append(images, img)
					}
				}
			}
			if !fromPerson(r) || (text == "" && len(images) == 0) {
				continue
			}
			out = append(out, provider.Message{ID: r.UUID, Role: "user", Text: session.Lines(text, 4000), Images: images, At: at})
			continue
		}
		// Blocks of one reply join the message they continue.
		if n := len(out); n == 0 || out[n-1].Role != "assistant" || r.Message.ID == "" || out[n-1].ID != r.Message.ID {
			out = append(out, provider.Message{ID: r.Message.ID, Role: "assistant", At: at})
		}
		m := &out[len(out)-1]
		if text != "" {
			if r.IsAPIError {
				m.Error = session.Text(text, 300)
			} else {
				m.Text = strings.TrimSpace(m.Text + "\n\n" + text)
			}
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if r.IsAPIError {
					m.Error = session.Text(b.Text, 300)
					continue
				}
				m.Text = strings.TrimSpace(m.Text + "\n\n" + b.Text)
			case "tool_use":
				calls[b.ID] = [2]int{len(out) - 1, len(m.Calls)}
				m.Calls = append(m.Calls, toolCall(b))
			}
		}
		m.Text = session.Lines(m.Text, 4000)
	}
	// Drop replies that were only thinking. A shell command or slash
	// command that anything follows has ended, though its output may be
	// missing, such as when it was cancelled.
	kept := out[:0]
	for i, m := range out {
		if (m.Role == "shell" || m.Role == "command") && i < len(out)-1 {
			m.Done = true
		}
		if m.Text != "" || len(m.Calls) > 0 || m.Error != "" {
			kept = append(kept, m)
		}
	}
	return kept
}

// localRecord reads the user records Claude Code writes for what happens
// outside the conversation with the model: a command the person ran in its
// shell (!) and its output, a slash command and its output, and a task
// notification. It reports whether r was one of them, and returns out with
// it applied.
func localRecord(r transcriptRecord, text string, at time.Time, out []provider.Message, calls map[string][2]int) (bool, []provider.Message) {
	open := func(role string) *provider.Message {
		if n := len(out); n > 0 && out[n-1].Role == role && !out[n-1].Done {
			return &out[n-1]
		}
		return nil
	}
	switch {
	case strings.HasPrefix(text, "<bash-input>"):
		if command := strings.TrimSpace(tagText(text, "bash-input")); command != "" {
			out = append(out, provider.Message{ID: r.UUID, Role: "shell", Text: session.Lines(command, maxOutput), At: at})
		}
	case strings.HasPrefix(text, "<bash-stdout>"), strings.HasPrefix(text, "<bash-stderr>"):
		// Claude Code records no exit code: the entry only ends.
		if m := open("shell"); m != nil {
			output := strings.TrimRight(tagText(text, "bash-stdout"), "\n")
			if stderr := strings.TrimSpace(tagText(text, "bash-stderr")); stderr != "" {
				output = strings.TrimSpace(output + "\n" + stderr)
			}
			m.Output, m.Done = provider.Tail(output, maxOutput), true
		}
	case strings.HasPrefix(text, "<command-name>"):
		if command := strings.TrimSpace(tagText(text, "command-name") + " " + tagText(text, "command-args")); command != "" {
			out = append(out, provider.Message{ID: r.UUID, Role: "command", Text: session.Lines(command, maxOutput), At: at})
		}
	case strings.HasPrefix(text, "<local-command-stdout>"), strings.HasPrefix(text, "<local-command-stderr>"):
		if m := open("command"); m != nil {
			output := tagText(text, "local-command-stdout") + tagText(text, "local-command-stderr")
			m.Output, m.Done = provider.Tail(output, maxOutput), true
		}
	case strings.HasPrefix(text, "<task-notification>"):
		// A background command or agent has ended. Its call may sit
		// before the part of the transcript that was read.
		if where, ok := calls[tagText(text, "tool-use-id")]; ok {
			call := &out[where[0]].Calls[where[1]]
			switch tagText(text, "status") {
			case "completed":
				call.Status = "completed"
			case "failed", "killed", "error":
				call.Status = "error"
			}
			if summary := strings.TrimSpace(tagText(text, "summary")); summary != "" {
				call.Output = provider.Tail(strings.TrimSpace(call.Output+"\n\n"+summary), maxOutput)
			}
		}
	default:
		return false, out
	}
	return true, out
}

// tagText is the text inside the first <tag>…</tag> of s, or "".
func tagText(s, tag string) string {
	_, rest, ok := strings.Cut(s, "<"+tag+">")
	if !ok {
		return ""
	}
	inside, _, _ := strings.Cut(rest, "</"+tag+">")
	return inside
}

// inBackground reports whether a tool result says its work goes on in the
// background: a Bash command run with run_in_background, or an agent
// launched asynchronously.
func inBackground(raw json.RawMessage) bool {
	var result struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		Status           string `json:"status"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return false
	}
	return result.BackgroundTaskID != "" || result.Status == "async_launched"
}

// fromPerson reports whether a user record is the person's prompt: typed,
// or sent from the phone as theirs. Command records and task, teammate and
// channel notifications are not; localRecord reads commands and task
// notifications first.
func fromPerson(r transcriptRecord) bool {
	// Block content (tool results, images) fails to decode as a string and
	// leaves text empty, so only the origin decides.
	var text string
	_ = json.Unmarshal(r.Message.Content, &text)
	return r.Origin.prompt(text)
}

func toolCall(b contentBlock) provider.Call {
	c := provider.Call{Name: session.Text(b.Name, 80), Status: "running"}
	var input map[string]any
	if json.Unmarshal(b.Input, &input) == nil && input != nil {
		key := ""
		switch b.Name {
		case "Write", "Edit", "MultiEdit":
			key = "file_path"
		case "NotebookEdit":
			key = "notebook_path"
		}
		if path, ok := input[key].(string); key != "" && ok && path != "" {
			c.Files = []string{path}
		}
		c.Summary = provider.CallSummary(input)
		c.Command = session.Block(provider.CallCommand(input), maxOutput)
		if pretty, err := json.MarshalIndent(input, "", "  "); err == nil && len(input) > 0 {
			c.Input = session.Block(string(pretty), maxInput)
		}
	}
	return c
}

// resultText is a tool result's text: a string, or its text blocks.
func resultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []contentBlock
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// imageSource is an image block's content, as Claude Code records pasted
// images and the pictures tools such as Read return.
type imageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// An image's ID names where it is in the transcript: the record's UUID,
// the block's index in it, and for a tool result the index inside the
// result, which is "x" for an image block of the record itself.
func imageID(uuid string, block, inner int) string {
	if inner < 0 {
		return fmt.Sprintf("%s_%d_x", uuid, block)
	}
	return fmt.Sprintf("%s_%d_%d", uuid, block, inner)
}

func imageOf(b contentBlock, uuid string, block, inner int) (provider.Image, bool) {
	if b.Source == nil || b.Source.Type != "base64" || !provider.ValidID(uuid) {
		return provider.Image{}, false
	}
	return provider.Image{ID: imageID(uuid, block, inner), MIME: b.Source.MediaType}, true
}

// resultImages lists the images in a tool result's content blocks.
func resultImages(uuid string, block int, raw json.RawMessage) []provider.Image {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var images []provider.Image
	for k, b := range blocks {
		if b.Type == "image" {
			if img, ok := imageOf(b, uuid, block, k); ok {
				images = append(images, img)
			}
		}
	}
	return images
}
