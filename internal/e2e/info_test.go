//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

func TestSessionInformationFromFixtureFiles(t *testing.T) {
	s := newSandbox(t)
	appendRecords := func(project, records string) {
		t.Helper()
		f, err := os.OpenFile(s.files[find(project).id], os.O_APPEND|os.O_WRONLY, 0600)
		must(t, err)
		_, err = f.WriteString(records + "\n")
		must(t, err)
		must(t, f.Close())
	}
	appendRecords("harbor", `{"type":"user","permissionMode":"auto","message":{"content":"Inspect this session"}}
{"type":"assistant","version":"2.1.316","effort":"low","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":1000,"cache_read_input_tokens":38000,"cache_creation_input_tokens":500}}}`)
	appendRecords("lighthouse", `{"type":"turn_context","payload":{"model":"gpt-6","effort":"high","approval_policy":"on-request","sandbox_policy":{"type":"read-only"}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0},"model_context_window":1000000},"rate_limits":{"primary":{"used_percent":0,"window_minutes":300},"secondary":null}}}`)
	d := s.start(120, 50)
	d.pick("harbor")
	d.keys("i")
	for _, want := range []string{"Model: Haiku 5.5 · low effort", "Context: 39.5k tokens · window unavailable", "Permission mode: Auto", "Profile:", "Version: 2.1.316", "Weekly: Unavailable"} {
		d.waitText(want)
	}
	d.keys("Escape")
	d.pick("lighthouse")
	d.keys("i")
	for _, want := range []string{"Model: gpt-6 · high effort", "Context: 0% · 0 of 1M", "Approval policy: on-request · read-only", "5-hour: 0%", "Weekly: Unavailable"} {
		d.waitText(want)
	}
	if strings.Contains(d.screen(), "7-day") {
		t.Fatal("old label remains")
	}
	if calls := s.loggedCalls(); len(calls) != 0 {
		t.Fatalf("inspection ran an agent: %v", calls)
	}
	d.keys("Escape", "q")
	d.waitText("EXITED 0")
}
