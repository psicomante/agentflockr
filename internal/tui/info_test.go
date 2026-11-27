package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/app"
	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

func TestSessionInformationShowsUnknownInsteadOfZeroAndRejectsOldReads(t *testing.T) {
	m := newModel(t, fixture())
	it, _ := m.selected()
	m.Update(infoMsg{key: it.session.Key(), seq: m.infoSeq, info: provider.Info{Folder: "/fixture", Branch: "feature\x1b[31m", Worktree: "/fixture", Connection: "Work"}})
	m.Update(key("i"))
	got := screen(m) + "\n" + strings.Join(m.informationBody(it.session), "\n")
	for _, want := range []string{"Session information", "Worktree (current): /fixture", "Branch (current): feature", "Connection: Work", "Context: Unavailable", "5-hour: Unavailable", "Weekly: Unavailable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "0%") {
		t.Fatal("missing usage shown as zero")
	}
	old := m.infoSeq
	m.Update(scanMsg(app.Snapshot{Sessions: fixture(), Generation: m.minGeneration}))
	m.Update(infoMsg{key: it.session.Key(), seq: old, info: provider.Info{Branch: "old"}})
	if got := m.infos[it.session.Key()]; got.Branch == "old" {
		t.Fatal("accepted an old inspection")
	}
	// The refresh must not blank the panel: the last inspection stays until
	// the next one replaces it.
	if got, ok := m.infos[it.session.Key()]; !ok || got.Branch != "feature\x1b[31m" {
		t.Fatalf("refresh dropped the last inspection: %+v", got)
	}
	for _, size := range [][2]int{{50, 12}, {80, 20}, {120, 30}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, line := range strings.Split(m.render(), "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("line too wide: %q", line)
			}
		}
	}
	m.Update(key("esc"))
	if m.mode != browsing {
		t.Fatal("could not close information")
	}
}

func TestSessionInformationDistinguishesReportedZeroUsage(t *testing.T) {
	lines := strings.Join(infoLines(session.Claude, provider.Info{ContextKnown: true, ContextWindow: 1000, Limits: []provider.Limit{{Kind: "five_hour", PercentUsed: 0}}}, time.Time{}), "\n")
	if !strings.Contains(lines, "Context: 0% · 0 of 1k") || !strings.Contains(lines, "5-hour: 0%") || !strings.Contains(lines, "Weekly: Unavailable") {
		t.Fatal(lines)
	}
}

func TestSessionInformationRendersTheSharedRows(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, agent := range []session.Provider{session.Claude, session.Codex, session.OpenCode} {
		info := provider.Info{Model: "claude-haiku-5-5", Effort: "low", ContextKnown: true, ContextTokens: 39500, Limits: []provider.Limit{{Kind: "five_hour", PercentUsed: 39, ResetsAt: now.Add(142 * time.Minute)}}, FilesKnown: true, Files: []provider.FileChange{{Path: "fixture"}}, GitKnown: true}
		lines := strings.Join(infoLines(agent, info, now), "\n")
		last := -1
		for _, row := range provider.InfoRows(agent, info, now) {
			want := row.Value
			if row.Label != "" {
				want = row.Label + ": " + want
			}
			pos := strings.Index(lines, want)
			if pos <= last {
				t.Fatalf("%s: missing or out of order %s\n%s", agent, want, lines)
			}
			last = pos
		}
		for _, want := range []string{"Model: Haiku 5.5 · low effort", "Context: 39.5k tokens · window unavailable", "5-hour: 39% · resets in 2 h 22 min", "Files reported by the agent: 1", "Current worktree changes: none"} {
			if !strings.Contains(lines, want) {
				t.Fatalf("missing %s\n%s", want, lines)
			}
		}
	}
}

func TestResetsInMatchesThePhone(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		minutes float64
		want    string
	}{{.2, "now"}, {42, "in 42 min"}, {95, "in 1 h 35 min"}, {120, "in 2 h"}, {80 * 60, "in 3 d 8 h"}, {24 * 60, "in 1 d"}, {-.5, "now"}, {.5, "in 1 min"}} {
		if got := provider.ResetsIn(now.Add(time.Duration(tc.minutes*float64(time.Minute))), now); got != tc.want {
			t.Fatalf("%v: %s", tc.minutes, got)
		}
	}
	if provider.ResetsIn(time.Time{}, now) != "" {
		t.Fatal("missing reset time")
	}
}

func TestInformationDebounceSkipsSelectionsAndOldGenerations(t *testing.T) {
	m := newModel(t, fixture())
	it, _ := m.selected()
	m.move(1)
	if _, cmd := m.Update(infoRequest{item: it.session, seq: m.infoSeq}); cmd != nil {
		t.Fatal("inspected a session already scrolled past")
	}
	it, _ = m.selected()
	if _, cmd := m.Update(infoRequest{item: it.session, seq: m.infoSeq + 1}); cmd != nil {
		t.Fatal("inspected an old generation")
	}
	if _, cmd := m.Update(infoRequest{item: it.session, seq: m.infoSeq}); cmd == nil {
		t.Fatal("did not inspect the settled selection")
	}
	if _, cmd := m.Update(infoRequest{item: it.session, seq: m.infoSeq}); cmd != nil {
		t.Fatal("started a duplicate inspection")
	}
}
