package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

func inspectionFixture(t *testing.T, data string) (*Adapter, session.Session) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture.jsonl")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return New(home), session.Session{ID: "cx-1", Transcript: path}
}

func TestInspectReadsTheCodexSessionFile(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	a, item := inspectionFixture(t, string(data))
	info, err := a.Inspect(t.Context(), item)
	if err != nil || info.Model != "gpt-6" || info.Effort != "high" || info.Mode != "on-request · read-only" || info.Version != "0.166.0" || !info.ContextKnown || info.ContextTokens != 39500 || info.ContextWindow != 1000000 || len(info.Limits) != 2 || info.Profile != a.Home {
		t.Fatalf("%+v %v", info, err)
	}
	if info.Limits[0].Kind != "seven_day" || info.Limits[1].Kind != "five_hour" || info.Limits[1].PercentUsed != 0 || info.Limits[1].ResetsAt.Unix() != 2000001000 {
		t.Fatal(info.Limits)
	}
	if err := os.Remove(item.Transcript); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Inspect(t.Context(), item); !errors.Is(err, provider.ErrSessionGone) {
		t.Fatal(err)
	}
}

func TestInspectCodexNullLimitsAreUnavailable(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, limits := range []string{`{"primary":null,"secondary":null}`, `{"primary":{"used_percent":20,"window_minutes":60}}`, `null`} {
		a, item := inspectionFixture(t, string(data)+`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0},"model_context_window":1000},"rate_limits":`+limits+`}}`)
		info, err := a.Inspect(t.Context(), item)
		if err != nil || len(info.Limits) != 0 || !info.ContextKnown || info.ContextTokens != 0 {
			t.Fatalf("%+v %v", info, err)
		}
	}
}

func TestInspectCodexIgnoresMalformedAndOversizedRecords(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Oversized first record exceeds both the metadata bound and tail. Later
	// valid records still load; the malformed line costs only that line.
	a, item := inspectionFixture(t, strings.Repeat("x", provider.MaxRecordSize+1)+"\ninvalid\n"+string(data))
	info, err := a.Inspect(t.Context(), item)
	if err != nil || info.Version != "" || info.Model != "gpt-6" || !info.ContextKnown || len(info.Limits) != 2 {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestInspectCodexModelSwitchAndInvalidUsage(t *testing.T) {
	data, err := os.ReadFile("testdata/inspection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{`{"type":"turn_context","payload":{"model":"switched"}}`, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}`, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":99999999999999999999999999}}}}`} {
		a, item := inspectionFixture(t, string(data)+tail)
		info, err := a.Inspect(t.Context(), item)
		if err != nil || info.ContextKnown || info.ContextTokens != 0 {
			t.Fatalf("%+v %v", info, err)
		}
	}
}
