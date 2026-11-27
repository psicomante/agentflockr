package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/psicomante/agentflockr/internal/app"
)

// fakeRows are Settings rows as internal/cli builds them for a file that
// sets refresh, with --codex-home given.
func fakeRows() []Setting {
	return []Setting{
		{Key: "theme", Kind: SettingTheme, Applies: AppliesNow, InEffect: "default", Default: "default", About: "The dashboard's colors."},
		{Key: "refresh", Kind: SettingDuration, Applies: AppliesNow, File: "10s", InEffect: "10s", Default: "5s", About: "How often."},
		{Key: "recent-window", Kind: SettingDuration, Applies: AppliesNow, InEffect: "24h", Default: "24h", About: "RECENT."},
		{Key: "providers", Kind: SettingAgents, Applies: AppliesNextStart, Default: "", About: "The agents.",
			Choices: []string{"opencode", "codex", "claude"}, ChoiceNames: []string{"OpenCode", "Codex", "Claude Code"}},
		{Key: "codex.home", Kind: SettingFolder, Applies: AppliesNextStart, InEffect: "~/work/.codex", Default: "~/.codex", Source: "--codex-home", About: "Codex."},
		{Key: "data-dir", Kind: SettingFixed, Applies: AppliesNextStart, InEffect: "~/.local/share/agentflockr", How: "Change it in the settings file, or with --data-dir."},
	}
}

// saves records each call to SaveSetting and answers with err, or with the
// rows as a file holding the saved value would give them.
type saves struct {
	calls []string
	err   error
}

func (s *saves) save(key, value string, reset bool) ([]Setting, error) {
	call := key + "=" + value
	if reset {
		call = key + " reset"
	}
	s.calls = append(s.calls, call)
	if s.err != nil {
		return nil, s.err
	}
	rows := fakeRows()
	for i := range rows {
		if rows[i].Key != key {
			continue
		}
		rows[i].File = value
		if reset {
			rows[i].File = ""
			value = rows[i].Default
		}
		if rows[i].Applies == AppliesNow && rows[i].Source == "" {
			rows[i].InEffect = value
		} else if rows[i].Source == "" {
			rows[i].Pending = true
		}
	}
	return rows, nil
}

func settingsModel(t *testing.T, s *saves) *Model {
	t.Helper()
	engine := &app.Engine{Providers: registry()}
	options := Options{Refresh: 10 * time.Second, Settings: fakeRows(), SettingsFile: "~/.config/agentflockr/agentflockr.toml"}
	if s != nil {
		options.SaveSetting = s.save
	}
	m := New(context.Background(), engine, options, app.Snapshot{Sessions: fixture(), UpdatedAt: now})
	m.now = func() time.Time { return now }
	m.rebuild()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

// runSave feeds the command's message back, as Bubble Tea would, for the
// commands a save starts.
func runSave(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg, ok := cmd().(settingSavedMsg); ok {
		m.Update(msg)
	}
}

// selectSetting moves the Settings view's cursor to the row with key.
func selectSetting(t *testing.T, m *Model, key string) {
	t.Helper()
	for i, r := range m.setRows {
		if r.Key == key {
			m.setCursor = i
			return
		}
	}
	t.Fatalf("no %s row", key)
}

// TestSettingsViewOpensAndGoesBack covers REQ-DSH-019: "," opens Settings
// from Sessions, Projects and Schedules, and ",", Tab or Esc go back to the
// view shown before. The view is never saved as the remembered one.
func TestSettingsViewOpensAndGoesBack(t *testing.T) {
	for _, start := range []string{"", "tab", "s"} {
		for _, back := range []string{",", "tab", "esc"} {
			m := settingsModel(t, &saves{})
			if start != "" {
				m.Update(key(start))
			}
			projects, scheduled := m.projects, m.scheduled
			_, cmd := m.Update(key(","))
			if !m.settingsShown || cmd != nil {
				t.Fatalf("from %q: Settings not shown, or something saved", start)
			}
			if s := screen(m); !strings.Contains(s, "agentflockr   Settings") || !strings.Contains(s, "codex.home") {
				t.Fatalf("from %q:\n%s", start, s)
			}
			m.Update(key(back))
			if m.settingsShown || m.projects != projects || m.scheduled != scheduled {
				t.Fatalf("from %q, %q went elsewhere", start, back)
			}
		}
	}
}

// TestSettingsRowsShowSourceAndMarker covers REQ-DSH-019: a row set by a
// flag says so, a saved row that applies later says when, the details pane
// explains, and data-dir opens no editor.
func TestSettingsRowsShowSourceAndMarker(t *testing.T) {
	m := settingsModel(t, &saves{})
	m.Update(key(","))
	selectSetting(t, m, "codex.home")
	s := screen(m)
	for _, want := range []string{"set by --codex-home", "The file's value waits until", "every agent", "~/.config/agentflockr/agentflockr.toml"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	selectSetting(t, m, "providers")
	m.Update(key("enter"))
	m.Update(key("space"))
	run(m, func() tea.Cmd { _, c := m.Update(key("enter")); return c }())
	if s := screen(m); !strings.Contains(s, "next start") || !strings.Contains(s, "providers saved. It applies at the next start.") {
		t.Fatalf("a saved row that applies later must say so:\n%s", s)
	}
	selectSetting(t, m, "data-dir")
	m.Update(key("enter"))
	if m.mode != browsing || m.notice != "Change it in the settings file, or with --data-dir." {
		t.Fatalf("data-dir: mode %v, notice %q", m.mode, m.notice)
	}
}

// TestSettingsTextEditorSavesAndShowsFailures covers REQ-DSH-019: Enter
// saves the typed value once; an empty value is never saved; a failed save
// keeps the editor open with the reason, and Enter tries again.
func TestSettingsTextEditorSavesAndShowsFailures(t *testing.T) {
	s := &saves{err: errors.New("refresh: refresh must be at least 1s")}
	m := settingsModel(t, s)
	m.Update(key(","))
	selectSetting(t, m, "refresh")
	m.Update(key("enter"))
	if m.editor.input.Value() != "10s" {
		t.Fatalf("the field starts with %q, want the file's value", m.editor.input.Value())
	}
	m.editor.input.SetValue("")
	m.Update(key("enter"))
	if len(s.calls) != 0 || !strings.Contains(screen(m), "Type a value, or press esc and x to reset it.") {
		t.Fatalf("an empty value was saved, or not explained: %v", s.calls)
	}
	typeText(m, "1ms")
	_, cmd := m.Update(key("enter"))
	if !m.editor.saving || !strings.Contains(screen(m), "Saving…") {
		t.Fatal("the editor must show it is saving")
	}
	m.Update(key("x")) // ignored while saving
	run(m, cmd)
	if m.mode != editingSetting || !strings.Contains(screen(m), "Could not save refresh: refresh must be at least 1s.") {
		t.Fatalf("a failed save must stay open with the reason:\n%s", screen(m))
	}
	s.err = nil
	m.editor.input.SetValue("15s")
	_, cmd = m.Update(key("enter"))
	run(m, cmd)
	if m.mode != browsing || strings.Join(s.calls, " ") != "refresh=1ms refresh=15s" || m.notice != "refresh saved." {
		t.Fatalf("calls %v, mode %v, notice %q", s.calls, m.mode, m.notice)
	}
}

// TestSettingsAgentsEditorTogglesAndReorders covers REQ-DSH-019: Space
// chooses, J and K reorder, and Enter saves the chosen agents in order.
func TestSettingsAgentsEditorTogglesAndReorders(t *testing.T) {
	s := &saves{}
	m := settingsModel(t, s)
	m.Update(key(","))
	selectSetting(t, m, "providers")
	m.Update(key("enter"))
	if !strings.Contains(screen(m), "[x] OpenCode") {
		t.Fatalf("every agent starts chosen when the file names none:\n%s", screen(m))
	}
	m.Update(key("space")) // OpenCode off
	m.Update(key("down"))
	m.Update(key("J")) // Codex below Claude Code
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if strings.Join(s.calls, " ") != "providers=claude,codex" {
		t.Fatalf("saved %v", s.calls)
	}
	m.Update(key("enter"))
	for range 3 {
		if m.editor.checked[m.editor.agents[m.editor.cursor]] {
			m.Update(key("space"))
		}
		m.Update(key("down"))
	}
	m.Update(key("enter"))
	if !strings.Contains(screen(m), "Choose at least one agent.") || len(s.calls) != 1 {
		t.Fatalf("none chosen must be refused:\n%s", screen(m))
	}
}

// TestSettingsThemePreviewSaveAndCancel covers REQ-DSH-020: moving shows
// each theme at once, Esc goes back to the one before, Enter saves its ID,
// and Enter on the saved theme writes nothing.
func TestSettingsThemePreviewSaveAndCancel(t *testing.T) {
	s := &saves{}
	m := settingsModel(t, s)
	m.Update(key(","))
	m.Update(key("enter"))
	m.Update(key("down"))
	if m.theme != "tokyo-night" || m.View().BackgroundColor == nil {
		t.Fatalf("moving must preview: theme %q", m.theme)
	}
	if !strings.Contains(screen(m), "◐ working") {
		t.Fatal("the picker must show the statuses in the theme's colors")
	}
	m.Update(key("esc"))
	if m.theme != "" && m.theme != DefaultTheme {
		t.Fatalf("Esc left %q on screen", m.theme)
	}
	m.Update(key("enter"))
	m.Update(key("enter")) // Default, already in effect
	if len(s.calls) != 0 || m.mode != browsing {
		t.Fatalf("Enter on the saved theme wrote %v", s.calls)
	}
	m.Update(key("enter"))
	for range 5 {
		m.Update(key("down"))
	}
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if strings.Join(s.calls, " ") != "theme=gruvbox-dark" || m.theme != "gruvbox-dark" || m.notice != "Theme saved: Gruvbox Dark." {
		t.Fatalf("calls %v, theme %q, notice %q", s.calls, m.theme, m.notice)
	}
}

// TestSettingsResetAsksFirst covers REQ-DSH-019: x asks before a setting goes
// back to its default; y resets, Esc keeps, and a setting the file leaves out
// already uses its default.
func TestSettingsResetAsksFirst(t *testing.T) {
	s := &saves{}
	m := settingsModel(t, s)
	m.Update(key(","))
	selectSetting(t, m, "recent-window")
	m.Update(key("x"))
	if m.mode != browsing || m.notice != "recent-window already uses its default." {
		t.Fatalf("mode %v, notice %q", m.mode, m.notice)
	}
	selectSetting(t, m, "refresh")
	m.Update(key("x"))
	if !strings.Contains(screen(m), "Reset refresh to its default, 5s?") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	m.Update(key("esc"))
	if len(s.calls) != 0 || m.mode != browsing {
		t.Fatal("Esc must keep the setting")
	}
	m.Update(key("x"))
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if strings.Join(s.calls, " ") != "refresh reset" || m.notice != "refresh reset to its default." || m.interval != 5*time.Second {
		t.Fatalf("calls %v, notice %q, interval %v", s.calls, m.notice, m.interval)
	}
}

// TestSettingsAppliesRefreshAndRecentWindowAtOnce covers REQ-DSH-019: after
// a save, the refresh interval and the RECENT line change at once, unless a
// flag sets them for this run.
func TestSettingsAppliesRefreshAndRecentWindowAtOnce(t *testing.T) {
	m := settingsModel(t, &saves{})
	m.Update(key(","))
	selectSetting(t, m, "refresh")
	m.Update(key("enter"))
	m.editor.input.SetValue("30s")
	_, cmd := m.Update(key("enter"))
	run(m, cmd)
	if m.interval != 30*time.Second {
		t.Fatalf("interval %v", m.interval)
	}
	selectSetting(t, m, "recent-window")
	m.Update(key("enter"))
	m.editor.input.SetValue("1m")
	_, cmd = m.Update(key("enter"))
	run(m, cmd)
	if m.recent != time.Minute {
		t.Fatalf("recent window %v", m.recent)
	}
	m.Update(key(","))
	if !strings.Contains(screen(m), "EARLIER") {
		t.Fatal("a shorter window must move sessions under EARLIER")
	}
	row := Setting{Key: "refresh", Applies: AppliesNow, InEffect: "2s", Source: "--refresh"}
	m.applySetting(row)
	if m.interval != 30*time.Second {
		t.Fatal("a value a flag sets must not change this run")
	}
}

// TestSettingsSaveNothingInDemoMode covers REQ-DSH-019: in demo mode a
// setting that applies at once changes for the run; the others, and resets,
// say that nothing is saved.
func TestSettingsSaveNothingInDemoMode(t *testing.T) {
	m := settingsModel(t, nil)
	m.Update(key(","))
	if !strings.Contains(screen(m), "Demo mode saves nothing.") {
		t.Fatal("the view must say demo mode saves nothing")
	}
	m.Update(key("enter"))
	m.Update(key("down"))
	m.Update(key("enter"))
	if m.theme != "tokyo-night" || m.notice != "theme set for this run. Demo mode saves nothing." {
		t.Fatalf("theme %q, notice %q", m.theme, m.notice)
	}
	selectSetting(t, m, "providers")
	m.Update(key("enter"))
	if m.mode != browsing || m.notice != "Demo mode saves nothing." {
		t.Fatalf("mode %v, notice %q", m.mode, m.notice)
	}
	selectSetting(t, m, "refresh")
	m.Update(key("x"))
	if m.mode != browsing || m.notice != "Demo mode saves nothing." {
		t.Fatalf("reset in demo: mode %v, notice %q", m.mode, m.notice)
	}
}

// TestSettingsFitTheSmallestTerminal covers REQ-DSH-019: at 50 × 12 the view
// and every editor fit, with the cursor's row and the footer on screen.
func TestSettingsFitTheSmallestTerminal(t *testing.T) {
	for _, row := range fakeRows() {
		for cursor := range len(themes) {
			m := settingsModel(t, &saves{})
			m.Update(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
			m.Update(key(","))
			selectSetting(t, m, row.Key)
			m.Update(key("enter"))
			for range cursor {
				m.Update(key("down"))
			}
			lines := strings.Split(m.render(), "\n")
			if len(lines) > minHeight {
				t.Fatalf("%s, row %d: %d lines", row.Key, cursor, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > minWidth {
					t.Fatalf("%s, row %d: line %d is %d wide: %q", row.Key, cursor, i, w, ansi.Strip(l))
				}
			}
			s := screen(m)
			if m.mode == editingSetting {
				if !strings.Contains(s, "esc") || !strings.Contains(s, "╰") {
					t.Fatalf("%s, row %d: the box's footer or border is cut:\n%s", row.Key, cursor, s)
				}
				if row.Kind == SettingTheme && !strings.Contains(s, "› "+themes[cursor].Name) {
					t.Fatalf("theme row %d is not on screen:\n%s", cursor, s)
				}
			} else if !strings.Contains(s, "q quit") || !strings.Contains(s, row.Key) {
				t.Fatalf("%s: the row or the footer is not on screen:\n%s", row.Key, s)
			}
			m.Update(key("esc"))
		}
	}
}

// TestHelpNamesSettings covers REQ-DSH-014: ? lists ",".
func TestHelpNamesSettings(t *testing.T) {
	m := newModel(t, fixture())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(key("?"))
	if !strings.Contains(screen(m), "Settings: theme, refresh, agents and folders") {
		t.Fatalf("help does not name Settings:\n%s", screen(m))
	}
}
