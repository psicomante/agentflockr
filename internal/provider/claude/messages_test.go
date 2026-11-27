package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

const transcriptLines = `{"type":"user","uuid":"u1","timestamp":"2026-10-27T16:44:30Z","origin":{"kind":"human"},"message":{"role":"user","content":"Run touch a.txt"}}
{"type":"attachment","uuid":"x"}
{"type":"assistant","uuid":"a1","timestamp":"2026-10-27T16:44:31Z","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"hmm"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-10-27T16:44:31Z","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"touch a.txt","description":"Create a.txt"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-10-27T16:44:32Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"<tool_use_error>The user declined this from their phone.</tool_use_error>","is_error":true}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-10-27T16:44:33Z","message":{"id":"msg_2","content":[{"type":"text","text":"It was refused."}]}}
{"type":"user","uuid":"u3","timestamp":"2026-10-27T16:45:00Z","origin":{"kind":"plugin","name":"agentflockr","asUser":true},"message":{"role":"user","content":"What is 6 times 7?"}}
{"type":"user","uuid":"u4","timestamp":"2026-10-27T16:45:01Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>done</task-notification>"}}
{"type":"user","uuid":"u5","timestamp":"2026-10-27T16:45:01Z","message":{"role":"user","content":"<command-name>/cost</command-name>"}}
{"type":"user","uuid":"u6","isSidechain":true,"timestamp":"2026-10-27T16:45:01Z","message":{"role":"user","content":"subagent prompt"}}
{"type":"assistant","uuid":"a4","timestamp":"2026-10-27T16:45:02Z","isApiErrorMessage":true,"message":{"id":"msg_3","content":[{"type":"text","text":"API Error: overloaded"}]}}
`

func TestMessagesReadTheTranscriptsEnd(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-api")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(transcriptLines), 0o644)
	a := New(t.TempDir(), home)
	got, err := a.Messages(t.Context(), session.Session{Provider: session.Claude, ID: sid}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, m := range got {
		line := m.Role + ": " + m.Text
		for _, c := range m.Calls {
			line += " [" + c.Name + " " + c.Summary + " " + c.Status + " " + c.Output + "]"
		}
		if m.Error != "" {
			line += " !" + m.Error
		}
		lines = append(lines, line)
	}
	want := []string{
		"user: Run touch a.txt",
		"assistant:  [Bash touch a.txt error <tool_use_error>The user declined this from their phone.</tool_use_error>]",
		"assistant: It was refused.",
		"user: What is 6 times 7?",
		"command: /cost",
		"assistant:  !API Error: overloaded",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(lines, "\n"))
	}
	if got, _ := a.Messages(t.Context(), session.Session{Provider: session.Claude, ID: sid}, 2); len(got) != 2 || got[1].Error == "" {
		t.Fatalf("limit: %+v", got)
	}
	if _, err := a.Messages(t.Context(), session.Session{Provider: session.Claude, ID: "missing"}, 2); err == nil {
		t.Fatal("missing transcript")
	}
}

// Claude Code writes the person's shell commands, its slash commands and
// the end of background work as user records with tags in their text.
func TestMessagesShowShellCommandsAndBackgroundWork(t *testing.T) {
	lines := `{"type":"user","uuid":"u1","timestamp":"2026-10-27T16:44:30Z","message":{"role":"user","content":"<bash-input>ls -la</bash-input>"}}
{"type":"user","uuid":"u2","timestamp":"2026-10-27T16:44:31Z","message":{"role":"user","content":"<bash-stdout>a.txt\nb.txt\n</bash-stdout><bash-stderr>warning</bash-stderr>"}}
{"type":"user","uuid":"u3","timestamp":"2026-10-27T16:44:32Z","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>Caveat</local-command-caveat>"}}
{"type":"user","uuid":"u4","timestamp":"2026-10-27T16:44:32Z","message":{"role":"user","content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>opus</command-args>"}}
{"type":"user","uuid":"u5","timestamp":"2026-10-27T16:44:33Z","message":{"role":"user","content":"<local-command-stdout>Set model to Opus</local-command-stdout>"}}
{"type":"user","uuid":"u6","timestamp":"2026-10-27T16:44:34Z","origin":{"kind":"human"},"message":{"role":"user","content":"Build it in the background"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-10-27T16:44:35Z","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"make build\nmake test","run_in_background":true}},{"type":"tool_use","id":"toolu_2","name":"Agent","input":{"prompt":"Review","run_in_background":true}},{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"sleep 100","run_in_background":true}}]}}
{"type":"user","uuid":"u7","timestamp":"2026-10-27T16:44:36Z","toolUseResult":{"backgroundTaskId":"b1"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Command running in background with ID: b1."}]}}
{"type":"user","uuid":"u8","timestamp":"2026-10-27T16:44:36Z","toolUseResult":{"status":"async_launched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"Async agent launched."}]}}
{"type":"user","uuid":"u9","timestamp":"2026-10-27T16:44:36Z","toolUseResult":{"backgroundTaskId":"b3"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"Command running in background with ID: b3."}]}}
{"type":"user","uuid":"u10","timestamp":"2026-10-27T16:44:40Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<status>completed</status>\n<summary>Background command completed (exit code 0)</summary>\n</task-notification>"}}
{"type":"user","uuid":"u11","timestamp":"2026-10-27T16:44:41Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<tool-use-id>toolu_2</tool-use-id>\n<status>failed</status>\n<summary>Agent failed</summary>\n</task-notification>"}}
{"type":"user","uuid":"u12","timestamp":"2026-10-27T16:44:42Z","message":{"role":"user","content":"<bash-input>sleep 9</bash-input>"}}
`
	got := messages([]byte(lines))
	if len(got) != 5 {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	shell, command, prompt, reply, running := got[0], got[1], got[2], got[3], got[4]
	if shell.Role != "shell" || shell.Text != "ls -la" || shell.Output != "a.txt\nb.txt\nwarning" || !shell.Done || shell.Exit != nil {
		t.Fatalf("shell %+v", shell)
	}
	if command.Role != "command" || command.Text != "/model opus" || command.Output != "Set model to Opus" || !command.Done {
		t.Fatalf("command %+v", command)
	}
	if prompt.Role != "user" || prompt.Text != "Build it in the background" {
		t.Fatalf("prompt %+v", prompt)
	}
	build, agent, sleep := reply.Calls[0], reply.Calls[1], reply.Calls[2]
	if build.Status != "completed" || !strings.HasSuffix(build.Output, "Background command completed (exit code 0)") {
		t.Fatalf("build %+v", build)
	}
	if build.Command != "make build\nmake test" || strings.Contains(build.Input, "make build") || !strings.Contains(build.Input, "run_in_background") {
		t.Fatalf("build command %q input %q", build.Command, build.Input)
	}
	if agent.Status != "error" || !strings.HasSuffix(agent.Output, "Agent failed") || agent.Command != "" {
		t.Fatalf("agent %+v", agent)
	}
	if sleep.Status != "running" {
		t.Fatalf("a background command without a notification still runs: %+v", sleep)
	}
	if running.Role != "shell" || running.Done {
		t.Fatalf("the last shell command has not ended: %+v", running)
	}
}

// sid is a valid Claude Code session ID for fixtures.
const sid = "0b9f3c1e-5a2d-4c7e-9f00-123456789abc"
