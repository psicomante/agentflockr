package provider

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func infoRow(rows []InfoRow, key string) InfoRow {
	for _, row := range rows {
		if row.Key == key {
			return row
		}
	}
	return InfoRow{}
}

func TestInfoRowsKeepOneOrderForEveryAgent(t *testing.T) {
	want := []string{"model", "context", "counts", "five_hour", "seven_day", "mode", "connection", "profile", "folder", "worktree", "branch", "repository", "mcp", "version", "files", "git_files"}
	for _, agent := range []session.Provider{session.Claude, session.Codex, session.OpenCode} {
		rows := InfoRows(agent, Info{Model: "claude-haiku-5-5", Effort: "low", Turns: 2, Connection: "Work", Profile: "/profile", Repo: "/repo", MCP: []MCPServer{{Name: "rea", Tools: 207}}, Version: "1"}, time.Time{})
		var keys []string
		for _, row := range rows {
			keys = append(keys, row.Key)
		}
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("%s: %v", agent, keys)
		}
		if rows[0].Value != "Haiku 5.5 · low effort" {
			t.Fatal(rows[0])
		}
	}
}

func TestInfoRowsShowUnavailableNotZero(t *testing.T) {
	for _, agent := range []session.Provider{session.Claude, session.Codex, session.OpenCode} {
		rows := InfoRows(agent, Info{}, time.Time{})
		for _, key := range []string{"model", "context", "five_hour", "seven_day", "mode", "folder", "worktree", "branch"} {
			if row := infoRow(rows, key); !strings.HasPrefix(row.Value, "Unavailable") || row.Percent >= 0 {
				t.Fatalf("%s: %+v", key, row)
			}
		}
	}
	rows := InfoRows(session.Claude, Info{ContextKnown: true, ContextWindow: 1000, Limits: []Limit{{Kind: "five_hour"}}}, time.Time{})
	if infoRow(rows, "context").Value != "0% · 0 of 1k" || infoRow(rows, "five_hour").Percent != 0 {
		t.Fatal(rows)
	}
}

func TestInfoRowsLeaveOutUnreportedExtras(t *testing.T) {
	rows := InfoRows(session.Codex, Info{}, time.Time{})
	for _, key := range []string{"counts", "connection", "profile", "repository", "mcp", "version"} {
		if infoRow(rows, key).Key != "" {
			t.Fatal(key)
		}
	}
}

func TestInfoRowsSayWhenALimitReset(t *testing.T) {
	now := time.Now()
	rows := InfoRows(session.Codex, Info{Limits: []Limit{{Kind: "five_hour", PercentUsed: 39, ResetsAt: now.Add(-time.Second)}}}, now)
	row := infoRow(rows, "five_hour")
	if row.Value != "Unavailable · reset since last reported" || row.Percent >= 0 || !row.ResetsAt.IsZero() {
		t.Fatal(row)
	}
}

func TestModelName(t *testing.T) {
	for id, want := range map[string]string{"claude-opus-5-5": "Opus 5.5", "claude-haiku-5-5-20261001": "Haiku 5.5", "claude-fable-5-1[1m]": "Fable 5.1 [1m]", "claude-sonnet-5": "Sonnet 5", "gpt-6": "gpt-6"} {
		if got := ModelName(id); got != want {
			t.Fatalf("%s: %s", id, got)
		}
	}
}

func TestInfoRowsCleanUntrustedValues(t *testing.T) {
	rows := InfoRows(session.Claude, Info{Model: strings.Repeat("m", 100), Effort: "low\x1b[31m", Mode: "auto\n", Version: strings.Repeat("v", 50), Profile: "/bad\x1b[31m", ContextSource: "note\x1b[31m"}, time.Time{})
	for _, row := range rows {
		if strings.ContainsAny(row.Value+row.Note, "\x1b\n") {
			t.Fatal(row)
		}
	}
	if len(infoRow(rows, "version").Value) > 43 {
		t.Fatal("unbounded version")
	}
}

func TestInfoRowsModeNamesAndContextCases(t *testing.T) {
	for mode, want := range map[string]string{"auto": "Auto", "default": "Manual", "acceptEdits": "Accept edits", "plan": "Plan", "dontAsk": "Don't ask", "bypassPermissions": "Bypass permissions", "custom": "custom"} {
		if row := infoRow(InfoRows(session.Claude, Info{Mode: mode}, time.Time{}), "mode"); row.Label != "Permission mode" || row.Value != want {
			t.Fatal(row)
		}
	}
	for mode, want := range map[string]string{"build": "Build", "plan": "Plan", "custom": "custom", "default": "default"} {
		if row := infoRow(InfoRows(session.OpenCode, Info{Mode: mode}, time.Time{}), "mode"); row.Label != "Agent mode" || row.Value != want {
			t.Fatal(row)
		}
	}
	for _, tc := range []struct {
		info Info
		want string
	}{{Info{ContextTokens: 39500}, "39.5k tokens · window unavailable"}, {Info{ContextWindow: 1000000}, "Usage unavailable · window 1M"}, {Info{ContextTokens: 39500, ContextWindow: 1000000}, "4% · 39.5k of 1M"}} {
		if row := infoRow(InfoRows(session.Codex, tc.info, time.Time{}), "context"); row.Value != tc.want {
			t.Fatal(row)
		}
	}
}
