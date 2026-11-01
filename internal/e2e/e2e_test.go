//go:build e2e

// Package e2e drives the real agentflockr binary the way a person does before
// a release: in a sandbox with a temporary HOME, fixture Claude Code and Codex
// sessions, stand-in agents that only log how they were called, and a tmux
// server of its own, so neither the real agents nor the user's tmux panes can
// be reached. Run it with `make e2e`; it needs tmux and skips without it.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// binary is the agentflockr built once for every test.
var binary string

// tmuxPath is the real tmux. The sandbox links it into its PATH, because
// opening a session looks for the agent's tmux pane.
var tmuxPath string

func TestMain(m *testing.M) {
	var err error
	if tmuxPath, err = exec.LookPath("tmux"); err != nil {
		fmt.Println("e2e: skipped, tmux is not installed")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "agentflockr-e2e-")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "agentflockr")
	build := exec.Command("go", "build", "-o", binary, "github.com/psicomante/agentflockr/cmd/agentflockr")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Printf("e2e: build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// A fixture is one sample session, written in its agent's own file format.
type fixture struct {
	agent   string // "codex" or "claude"
	id      string // a UUID, as both agents use
	project string // the folder under ~/code
	prompt  string // the request, which becomes the title
	reply   string
	ends    string // "done" or "failed"
	ago     time.Duration
}

// Each project and title word is unique, so a search for it selects one row.
var fixtures = []fixture{
	{"codex", "11111111-1111-4111-8111-111111111111", "lighthouse", "Polish the lighthouse beam.", "The beam sweeps twice as far.", "done", time.Hour},
	{"claude", "22222222-2222-4222-8222-222222222222", "harbor", "Chart the harbor depths.", "The chart marks every sandbank.", "done", 2 * time.Hour},
	{"codex", "33333333-3333-4333-8333-333333333333", "mill", "Grind the mill schedule.", "go test ./... exited with status 1", "failed", 10 * time.Minute},
	{"codex", "44444444-4444-4444-8444-444444444444", "orchard", "Prune the orchard rows.", "Every third row is pruned.", "done", 3 * time.Hour},
	{"codex", "55555555-5555-4555-8555-555555555555", "quarry", "Survey the quarry walls.", "The north wall needs bolts.", "done", 24 * time.Hour},
}

func find(id string) fixture {
	for _, f := range fixtures {
		if f.id == id || f.project == id {
			return f
		}
	}
	panic("no fixture " + id)
}

// sandbox is one test's world: its HOME, index, stand-in agents and tmux
// server.
type sandbox struct {
	t      *testing.T
	home   string // the agents' and agentflockr's HOME
	data   string // agentflockr's index folder
	bin    string // the only folder on PATH besides the system's
	calls  string // where the stand-ins log
	socket string // the tmux server's name
	files  map[string]string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	// Resolve the temporary folder's symlinks (/var is /private/var on
	// macOS), so folders compare equal to what the stand-ins log.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{
		t:      t,
		home:   filepath.Join(root, "home"),
		data:   filepath.Join(root, "data"),
		bin:    filepath.Join(root, "bin"),
		calls:  filepath.Join(root, "calls.log"),
		socket: fmt.Sprintf("agentflockr-e2e-%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "-")),
		files:  map[string]string{},
	}
	for _, dir := range []string{s.home, s.data, s.bin} {
		must(t, os.MkdirAll(dir, 0o755))
	}
	must(t, os.Symlink(tmuxPath, filepath.Join(s.bin, "tmux")))
	for _, agent := range []string{"codex", "claude"} {
		must(t, os.WriteFile(filepath.Join(s.bin, agent), []byte(standIn(agent, s.calls)), 0o755))
	}
	for _, f := range fixtures {
		s.write(f)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-L", s.socket, "kill-server").Run() })
	return s
}

// standIn is a shell script that logs its folder and arguments, one call per
// line with fields separated by "|", then waits for Enter like an interactive
// agent. `codex delete --force ID` removes the rollout file, as Codex does.
func standIn(agent, calls string) string {
	return `#!/bin/sh
line="` + agent + `|$(pwd -P)"
for a in "$@"; do line="$line|$a"; done
echo "$line" >>'` + calls + `'
if [ "` + agent + `" = codex ] && [ "${1:-}" = delete ]; then
	find "${CODEX_HOME:-$HOME/.codex}/sessions" -name "rollout-*-$3.jsonl" -delete
	exit 0
fi
echo "STAND-IN ` + agent + ` $*"
read _
`
}

// dir is a fixture project's folder.
func (s *sandbox) dir(project string) string { return filepath.Join(s.home, "code", project) }

func (s *sandbox) write(f fixture) {
	t := s.t
	dir := s.dir(f.project)
	must(t, os.MkdirAll(dir, 0o755))
	at := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339Nano) }
	var records []any
	var file string
	switch f.agent {
	case "codex":
		started := time.Now().Add(-f.ago - time.Minute).UTC()
		folder := filepath.Join(s.home, ".codex", "sessions", started.Format("2006"), started.Format("01"), started.Format("02"))
		file = filepath.Join(folder, "rollout-"+started.Format("2006-01-02T15-04-05")+"-"+f.id+".jsonl")
		event := func(d time.Duration, payload map[string]any) map[string]any {
			return map[string]any{"timestamp": at(d), "type": "event_msg", "payload": payload}
		}
		records = []any{
			map[string]any{"timestamp": at(f.ago + time.Minute), "type": "session_meta", "payload": map[string]any{
				"id": f.id, "timestamp": at(f.ago + time.Minute), "cwd": dir, "originator": "codex_cli_rs", "cli_version": "0.50.0", "source": "cli"}},
			map[string]any{"timestamp": at(f.ago + time.Minute), "type": "turn_context", "payload": map[string]any{"cwd": dir}},
			event(f.ago+time.Minute, map[string]any{"type": "task_started"}),
			event(f.ago+time.Minute, map[string]any{"type": "user_message", "message": f.prompt}),
			event(f.ago+time.Second, map[string]any{"type": "agent_message", "message": f.reply}),
		}
		if f.ends == "failed" {
			records = append(records, event(f.ago, map[string]any{"type": "error", "message": f.reply}))
		} else {
			records = append(records, event(f.ago, map[string]any{"type": "task_complete", "last_agent_message": f.reply}))
		}
	case "claude":
		// Claude Code names a project's folder after its path, with every
		// character other than a letter, a digit or a dash turned into a dash.
		name := strings.Map(func(r rune) rune {
			if r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
				return r
			}
			return '-'
		}, dir)
		file = filepath.Join(s.home, ".claude", "projects", name, f.id+".jsonl")
		base := func(kind, uuid, parent string, d time.Duration) map[string]any {
			r := map[string]any{"sessionId": f.id, "cwd": dir, "isSidechain": false, "userType": "external", "entrypoint": "cli",
				"version": "2.1.0", "type": kind, "uuid": uuid, "timestamp": at(d)}
			if parent != "" {
				r["parentUuid"] = parent
			}
			return r
		}
		user := base("user", "u-"+f.id, "", f.ago+time.Minute)
		user["message"] = map[string]any{"role": "user", "content": f.prompt}
		reply := base("assistant", "a-"+f.id, "u-"+f.id, f.ago+time.Second)
		reply["message"] = map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": f.reply}}, "stop_reason": "end_turn"}
		done := base("system", "s-"+f.id, "a-"+f.id, f.ago)
		done["subtype"], done["durationMs"], done["isMeta"] = "turn_duration", 39000, false
		records = []any{user, reply, done}
	}
	var b strings.Builder
	for _, r := range records {
		line, err := json.Marshal(r)
		must(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	must(t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(t, os.WriteFile(file, []byte(b.String()), 0o644))
	s.files[f.id] = file
}

// call is one logged run of a stand-in agent.
type call struct {
	agent, dir string
	args       []string
}

func (s *sandbox) loggedCalls() []call {
	raw, err := os.ReadFile(s.calls)
	if os.IsNotExist(err) {
		return nil
	}
	must(s.t, err)
	var out []call
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if f := strings.Split(line, "|"); len(f) >= 2 {
			out = append(out, call{agent: f[0], dir: f[1], args: f[2:]})
		}
	}
	return out
}

// waitCalls waits until the stand-ins were called n times in all.
func (s *sandbox) waitCalls(n int) []call {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		calls := s.loggedCalls()
		if len(calls) >= n || time.Now().After(deadline) {
			if len(calls) < n {
				s.t.Fatalf("want %d agent calls, got %d: %v", n, len(calls), calls)
			}
			return calls
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *sandbox) tmux(args ...string) string {
	s.t.Helper()
	out, err := exec.Command(tmuxPath, append([]string{"-L", s.socket}, args...)...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// env is the whole environment of every program in the sandbox: nothing from
// the machine running the test, such as XDG folders or a real CODEX_HOME,
// reaches them. TMUX and TMUX_PANE stay, for the tmux handoff.
func (s *sandbox) env() string {
	return fmt.Sprintf(`env -i TMUX="$TMUX" TMUX_PANE="$TMUX_PANE" HOME=%s PATH=%s TERM=xterm-256color LANG=en_US.UTF-8`,
		quote(s.home), quote(s.bin+":/usr/bin:/bin:/usr/sbin:/sbin"))
}

// dashboard is agentflockr running in the tmux session "dash".
type dashboard struct {
	s *sandbox
	t *testing.T
}

// start runs agentflockr in a fresh tmux session of the given size and waits
// for its first screen.
func (s *sandbox) start(width, height int, flags ...string) *dashboard {
	s.t.Helper()
	exec.Command(tmuxPath, "-L", s.socket, "kill-session", "-t", "dash").Run()
	args := []string{quote(binary), "--data-dir", quote(s.data), "--claude-home", quote(filepath.Join(s.home, ".claude")),
		"--codex-home", quote(filepath.Join(s.home, ".codex")), "--providers", "codex,claude"}
	args = append(args, flags...)
	// The pane outlives agentflockr, so a test can read why it quit.
	line := s.env() + " " + strings.Join(args, " ") + `; echo "EXITED $?"; sleep 600`
	s.tmux("-f", "/dev/null", "new-session", "-d", "-s", "dash", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height), "-c", s.home, line)
	d := &dashboard{s: s, t: s.t}
	d.waitFor("the dashboard", func(screen string) bool { return strings.Contains(screen, "? help") })
	return d
}

func (d *dashboard) screen() string { return d.s.tmux("capture-pane", "-p", "-t", "dash") }

// waitFor polls the screen until ok, and fails the test after 10 seconds.
func (d *dashboard) waitFor(what string, ok func(screen string) bool) string {
	d.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		screen := d.screen()
		if ok(screen) {
			return screen
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %s; the screen:\n%s", what, screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *dashboard) waitText(text string) string {
	d.t.Helper()
	return d.waitFor(fmt.Sprintf("%q", text), func(screen string) bool { return strings.Contains(screen, text) })
}

// keys presses tmux keys one at a time. A pause after Esc keeps the next key
// from being read as Alt plus that key.
func (d *dashboard) keys(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		d.s.tmux("send-keys", "-t", "dash", k)
		if k == "Escape" {
			time.Sleep(150 * time.Millisecond)
		}
	}
}

func (d *dashboard) typ(text string) { d.t.Helper(); d.s.tmux("send-keys", "-t", "dash", "-l", text) }

// selected is the selected row: the line with the selection bar.
func selected(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "▌") {
			return line
		}
	}
	return ""
}

// pick selects the session whose title contains word, by searching for it.
func (d *dashboard) pick(word string) {
	d.t.Helper()
	d.keys("/")
	d.waitText("esc cancel")
	d.keys("C-u")
	d.typ(word)
	d.keys("Enter")
	d.waitFor("a row with "+word+" selected", func(screen string) bool {
		return strings.Contains(selected(screen), word) && !strings.Contains(screen, "esc cancel")
	})
}

// clearFilters leaves search and project filters.
func (d *dashboard) clearFilters() {
	d.t.Helper()
	d.keys("Escape")
	d.waitFor("no filter", func(screen string) bool { return !strings.Contains(screen, "esc clears") })
}

// pickProject selects the project row containing word in the Projects view.
func (d *dashboard) pickProject(word string) {
	d.t.Helper()
	d.keys("Tab")
	d.waitText("tab sessions")
	d.keys("g")
	for range len(fixtures) + 1 {
		if strings.Contains(selected(d.screen()), word) {
			return
		}
		d.keys("j")
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("no project row with %q:\n%s", word, d.screen())
}

// quit presses q and waits for agentflockr to exit cleanly.
func (d *dashboard) quit() {
	d.t.Helper()
	d.keys("q")
	d.waitText("EXITED 0")
}

func TestDemoOpensAndChangesNothing(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30, "--demo")
	d.waitText("demo")
	d.keys("Enter")
	d.waitText("Demo sessions can't be opened.")
	for _, k := range []string{"d", "x", "m"} {
		d.keys("Escape", k)
		d.waitText("Demo sessions can't be changed.")
	}
	d.quit()
	if calls := s.loggedCalls(); len(calls) != 0 {
		t.Fatalf("demo mode ran agents: %v", calls)
	}
	if entries, _ := os.ReadDir(s.data); len(entries) != 0 {
		t.Fatalf("demo mode wrote to the data folder: %v", entries)
	}
}

func TestListsTheSandboxSessions(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30)
	screen := d.waitText("5 sessions")
	for _, f := range fixtures {
		if !strings.Contains(screen, f.prompt) {
			t.Errorf("%s is not listed:\n%s", f.prompt, screen)
		}
	}
	if !strings.Contains(screen, "✕ failed") || !strings.Contains(screen, "✓ done") {
		t.Errorf("statuses are missing:\n%s", screen)
	}
}

func TestOpenResumesInTheAgentAndComesBack(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30)

	codex := find("lighthouse")
	d.pick("lighthouse")
	d.keys("Enter")
	d.waitText("STAND-IN codex")
	got := s.waitCalls(1)[0]
	want := call{"codex", s.dir("lighthouse"), []string{"resume", "--cd", s.dir("lighthouse"), codex.id}}
	if !equal(got, want) {
		t.Fatalf("Codex resumed as %+v, want %+v", got, want)
	}
	d.keys("Enter")
	d.waitText("Back from Codex.")

	claude := find("harbor")
	d.clearFilters()
	d.pick("harbor")
	d.keys("Enter")
	d.waitText("STAND-IN claude")
	got = s.waitCalls(2)[1]
	want = call{"claude", s.dir("harbor"), []string{"--resume", s.files[claude.id]}}
	if !equal(got, want) {
		t.Fatalf("Claude Code resumed as %+v, want %+v", got, want)
	}
	d.keys("Enter")
	d.waitText("Back from Claude Code.")
}

func TestOpenSwitchesToTheRunningPane(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30)
	f := find("lighthouse")
	// The session already runs in a pane of another tmux session.
	s.tmux("new-session", "-d", "-s", "work", "-c", s.dir(f.project), s.env()+" codex resume "+f.id)
	s.waitCalls(1)
	// Switching needs a tmux client; attach one through a pseudo-terminal.
	attach := []string{tmuxPath, "-L", s.socket, "attach", "-t", "dash"}
	var client *exec.Cmd
	if runtime.GOOS == "darwin" {
		client = exec.Command("script", append([]string{"-q", "/dev/null"}, attach...)...)
	} else {
		client = exec.Command("script", "-qfc", strings.Join(attach, " "), "/dev/null")
	}
	must(t, client.Start())
	t.Cleanup(func() { client.Process.Kill(); client.Wait() })
	clientIn := func(session string) bool {
		out, _ := exec.Command(tmuxPath, "-L", s.socket, "list-clients", "-F", "#{session_name}").Output()
		return strings.TrimSpace(string(out)) == session
	}
	waitUntil(t, "a client on dash", func() bool { return clientIn("dash") })

	d.pick("lighthouse")
	d.keys("Enter")
	waitUntil(t, "the client to switch to work", func() bool { return clientIn("work") })
	d.waitText("Switched to the tmux pane running this Codex session.")
	if calls := s.loggedCalls(); len(calls) != 1 {
		t.Fatalf("a second agent started: %v", calls)
	}
}

func TestOpeningAMissingFolderExplainsWhy(t *testing.T) {
	s := newSandbox(t)
	// At 80 columns the message is wider than the line; it must be
	// shortened, not dropped.
	d := s.start(80, 24)
	must(t, os.RemoveAll(s.dir("orchard")))
	d.keys("r")
	d.pick("orchard")
	d.keys("Enter")
	d.waitText("Could not open: the project folder")
	if calls := s.loggedCalls(); len(calls) != 0 {
		t.Fatalf("an agent ran for a missing folder: %v", calls)
	}
}

func TestChangesLastAcrossRestarts(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30)

	// Move: the user renamed the folder, then points the project at it.
	must(t, os.Rename(s.dir("quarry"), s.dir("quarry-renamed")))
	d.pick("quarry")
	d.keys("m")
	d.waitText("Move project")
	d.keys("C-u")
	d.typ("~/code/quarry-renamed")
	d.keys("Enter")
	d.waitText("Project moved.")
	d.keys("Enter")
	d.waitText("STAND-IN codex")
	got := s.waitCalls(1)[0]
	if got.dir != s.dir("quarry-renamed") {
		t.Fatalf("a moved session resumed in %s", got.dir)
	}
	d.keys("Enter")
	d.waitText("Back from Codex.")

	// Remove: agentflockr forgets it; Codex keeps its file.
	d.clearFilters()
	d.pick("mill")
	d.keys("x")
	d.waitText("Remove from agentflockr")
	d.keys("y")
	d.waitText("Removed from agentflockr.")
	if _, err := os.Stat(s.files[find("mill").id]); err != nil {
		t.Fatalf("removing touched the agent's file: %v", err)
	}

	// Delete through Codex.
	orchard := find("orchard")
	d.clearFilters()
	d.pick("orchard")
	d.keys("d")
	d.waitText("Permanently deletes this conversation through Codex.")
	d.keys("y")
	d.waitText("Session deleted.")
	calls := s.waitCalls(2)
	if want := (call{"codex", "", []string{"delete", "--force", orchard.id}}); !slices.Equal(calls[1].args, want.args) {
		t.Fatalf("Codex was asked %v, want %v", calls[1].args, want.args)
	}

	// Delete a Claude Code transcript, which agentflockr removes itself.
	d.clearFilters()
	d.pick("harbor")
	d.keys("d")
	d.waitText("Delete session")
	d.keys("y")
	d.waitText("Session deleted.")
	if _, err := os.Stat(s.files[find("harbor").id]); !os.IsNotExist(err) {
		t.Fatalf("the Claude Code transcript is still there: %v", err)
	}

	// After a restart and a refresh, every change holds.
	d.quit()
	d = s.start(120, 30)
	screen := d.waitText("2 sessions")
	for _, gone := range []string{"mill", "orchard", "harbor"} {
		if strings.Contains(screen, gone) {
			t.Errorf("%s is listed again after a restart:\n%s", gone, screen)
		}
	}
	if !strings.Contains(screen, "quarry-renamed") {
		t.Errorf("the move did not last:\n%s", screen)
	}
}

func TestNewSessionStartsFreshInTheProject(t *testing.T) {
	s := newSandbox(t)
	d := s.start(120, 30)
	d.pickProject("lighthouse")
	d.keys("n")
	d.waitText("New session")
	// Choose Codex, whichever agent the form starts on.
	for range 3 {
		if strings.Contains(d.screen(), "‹ Codex ›") {
			break
		}
		d.keys("Right")
		time.Sleep(200 * time.Millisecond)
	}
	// The form ignores Enter until it has read Codex's profiles and models.
	d.waitFor("Codex chosen and loaded", func(screen string) bool {
		return strings.Contains(screen, "‹ Codex ›") && !strings.Contains(screen, "loading…")
	})
	d.keys("Enter")
	d.waitText("STAND-IN codex")
	calls := s.loggedCalls()
	got := calls[len(calls)-1]
	want := call{"codex", s.dir("lighthouse"), []string{"--cd", s.dir("lighthouse")}}
	if !equal(got, want) {
		t.Fatalf("a new session started as %+v, want %+v", got, want)
	}
	d.keys("Enter")
	d.waitText("Back from Codex.")
}

func equal(a, b call) bool {
	return a.agent == b.agent && a.dir == b.dir && slices.Equal(a.args, b.args)
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
