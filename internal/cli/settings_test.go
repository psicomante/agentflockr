package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/tui"
)

func row(t *testing.T, rows []tui.Setting, key string) tui.Setting {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no %s row", key)
	return tui.Setting{}
}

// TestSettingRowsNameTheirSource covers REQ-DSH-019: each row shows the
// file's value, the value in effect and what set it over the file.
func TestSettingRowsNameTheirSource(t *testing.T) {
	home := isolate(t)
	codex := filepath.Join(home, "work", ".codex")
	t.Setenv("CODEX_HOME", codex)
	settingsFile(t, home, "refresh = \"10s\"\nrecent-window = \"48h\"\n[codex]\nhome = \"~/other\"\n")
	o, _, err := parse(t, "--refresh", "3s")
	if err != nil {
		t.Fatal(err)
	}
	rows := settingRows(o, o.file)
	for _, want := range []tui.Setting{
		{Key: "refresh", File: "10s", InEffect: "3s", Source: "--refresh", Default: "5s"},
		{Key: "recent-window", File: "48h", InEffect: "48h", Default: "24h"},
		{Key: "codex.home", File: "~/other", InEffect: "~/work/.codex", Source: "CODEX_HOME", Default: "~/.codex"},
		{Key: "codex.stale-after", File: "", InEffect: "2m", Default: "2m"},
		{Key: "theme", File: "", InEffect: "default", Default: "default"},
	} {
		got := row(t, rows, want.Key)
		if got.File != want.File || got.InEffect != want.InEffect || got.Source != want.Source || got.Default != want.Default {
			t.Errorf("%s: got file %q, in effect %q, source %q, default %q; want %+v",
				want.Key, got.File, got.InEffect, got.Source, got.Default, want)
		}
	}
	if r := row(t, rows, "providers"); strings.Join(r.Choices, ",") != strings.Join(agentNames(), ",") {
		t.Errorf("providers offers %v", r.Choices)
	}
	if r := row(t, rows, "data-dir"); r.Kind != tui.SettingFixed || r.How == "" {
		t.Errorf("data-dir must be shown, not edited: %+v", r)
	}
}

// TestSaveSettingWritesTheFileInEffect covers REQ-CLI-016: the file named
// with --config is the one written, the default one is left alone, and the
// rows read back show the saved value in effect for a setting that applies
// at once.
func TestSaveSettingWritesTheFileInEffect(t *testing.T) {
	home := isolate(t)
	fallback := settingsFile(t, home, "")
	named := filepath.Join(t.TempDir(), "mine.toml")
	if err := os.WriteFile(named, []byte("# mine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o, _, err := parse(t, "--config", named)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := o.saveSetting("recent-window", "12h", false)
	if err != nil {
		t.Fatal(err)
	}
	if r := row(t, rows, "recent-window"); r.File != "12h" || r.InEffect != "12h" {
		t.Fatalf("read back %+v", r)
	}
	if data, _ := os.ReadFile(named); string(data) != "recent-window = \"12h\"\n# mine\n" {
		t.Fatalf("named file holds %q", data)
	}
	if data, _ := os.ReadFile(fallback); len(data) != 0 {
		t.Fatalf("the default file was written: %q", data)
	}
	if _, err := o.saveSetting("providers", "claude,codex", false); err != nil {
		t.Fatal(err)
	}
	rows, err = o.saveSetting("recent-window", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if r := row(t, rows, "recent-window"); r.File != "" || r.InEffect != "24h" {
		t.Fatalf("after a reset: %+v", r)
	}
	if r := row(t, rows, "providers"); r.File != "claude,codex" || !r.Pending {
		t.Fatalf("providers read back %+v", r)
	}
}

// TestSaveSettingRefusesAMissingHome covers REQ-CLI-016: a home must be a
// folder that exists, so the next start does not fail on it.
func TestSaveSettingRefusesAMissingHome(t *testing.T) {
	home := isolate(t)
	path := settingsFile(t, home, "")
	o, _, err := parse(t)
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.saveSetting("codex.home", "~/work/.codx", false)
	if err == nil || err.Error() != "codex.home: ~/work/.codx is not a folder" {
		t.Fatalf("got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "work", ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := o.saveSetting("codex.home", "~/work/.codex", false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "[codex]\nhome = \"~/work/.codex\"\n" {
		t.Fatalf("file holds %q", data)
	}
}
