package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const commented = `# My settings.
refresh = "10s"  # laptop

[codex]
# Long test suites.
home = "~/work/.codex"
`

// TestSetChangesOnlyTheKeysLine covers REQ-CLI-016: a top-level key, a key
// in a table and providers each change their own line, or gain one, and
// every other byte stays, comments and line endings included.
func TestSetChangesOnlyTheKeysLine(t *testing.T) {
	for _, c := range []struct {
		name, file, key string
		value           any
		want            string
	}{
		{"existing line keeps its comment", commented, "refresh", "20s",
			strings.Replace(commented, `refresh = "10s"`, `refresh = "20s"`, 1)},
		{"top-level key is added first", commented, "theme", "tokyo-night",
			"theme = \"tokyo-night\"\n" + commented},
		{"table key is added after its header", commented, "codex.stale-after", "5m",
			strings.Replace(commented, "[codex]\n", "[codex]\nstale-after = \"5m\"\n", 1)},
		{"table key replaces its line", commented, "codex.home", "~/other",
			strings.Replace(commented, `home = "~/work/.codex"`, `home = "~/other"`, 1)},
		{"a missing table is added at the end", commented, "serve.public-url", "https://mac.example.ts.net",
			commented + "\n[serve]\npublic-url = \"https://mac.example.ts.net\"\n"},
		{"providers as an array", "providers = ['codex']\n", "providers", []string{"claude", "codex"},
			"providers = [\"claude\", \"codex\"]\n"},
		{"CRLF line endings", "refresh = \"10s\"\r\n[codex]\r\nhome = \"~/c\"\r\n", "codex.stale-after", "5m",
			"refresh = \"10s\"\r\n[codex]\r\nstale-after = \"5m\"\r\nhome = \"~/c\"\r\n"},
		{"same key name in another table", "[claude]\nhome = \"~/a\"\n[codex]\n", "codex.home", "~/b",
			"[claude]\nhome = \"~/a\"\n[codex]\nhome = \"~/b\"\n"},
		{"a table after array tables", "[[claude.profiles]]\nname = \"w\"\nhome = \"~/w\"\n", "claude.stale-after", "5m",
			"[[claude.profiles]]\nname = \"w\"\nhome = \"~/w\"\n\n[claude]\nstale-after = \"5m\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.file)
			if err := Set(path, c.key, c.value); err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); got != c.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, c.want)
			}
		})
	}
}

// TestUnsetRemovesOnlyTheKeysLine covers REQ-CLI-016's reset: the key's line
// goes, the rest stays, and a file that does not set the key is untouched.
func TestUnsetRemovesOnlyTheKeysLine(t *testing.T) {
	path := write(t, commented)
	if err := Unset(path, "codex.home"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, path), strings.Replace(commented, "home = \"~/work/.codex\"\n", "", 1); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	before := read(t, path)
	if err := Unset(path, "recent-window"); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != before {
		t.Fatal("a key the file does not set must leave the file alone")
	}
}

// TestSetCreatesAMissingFile covers REQ-CLI-016: with no settings file, a
// save creates it, and its folder, private to the person.
func TestSetCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "agentflockr", FileName)
	if err := Set(path, "theme", "gruvbox-light"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "theme = \"gruvbox-light\"\n" {
		t.Fatalf("got %q", got)
	}
	for p, want := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", p, info.Mode().Perm(), err, want)
		}
	}
}

// TestSetFollowsSymlinksAndKeepsTheMode covers REQ-CLI-016: a settings file
// reached through a link, as dotfiles often are, is written at its target,
// the link survives, and the target keeps its permissions.
func TestSetFollowsSymlinksAndKeepsTheMode(t *testing.T) {
	target := write(t, commented)
	if err := os.Chmod(target, 0640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), FileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Set(link, "refresh", "30s"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if !strings.Contains(read(t, target), `refresh = "30s"`) {
		t.Fatal("the target was not changed")
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0640 {
		t.Fatalf("mode %v, want 0640", info.Mode().Perm())
	}
}

// TestSetRefusesWhatItCannotVerify covers REQ-CLI-016: whenever the edit
// cannot be shown to change that key alone, or the value is bad, the file is
// left exactly as it was.
func TestSetRefusesWhatItCannotVerify(t *testing.T) {
	for _, c := range []struct {
		name, file, key string
		value           any
		want            string
	}{
		{"an invalid file", "refesh = \"5s\"\n", "refresh", "6s", `unknown setting "refesh"`},
		{"a multi-line array", "providers = [\n  \"codex\",\n]\n", "providers", []string{"claude"}, "could not change providers's line safely"},
		{"a dotted key", "codex.home = \"~/c\"\n", "codex.home", "~/d", "could not change codex.home's line safely"},
		{"a key Settings can't change", "", "data-dir", "~/d", "data-dir can't be changed from Settings"},
		{"a bad duration", "", "refresh", "soon", `"soon" is not a duration`},
		{"a value out of range", "", "refresh", "1ms", "refresh must be at least 1s"},
		{"an unknown theme", "", "theme", "tokio-night", "theme must be one of"},
		{"a listen address off loopback", "", "serve.listen", "0.0.0.0:7433", "loopback"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.file)
			err := Set(path, c.key, c.value)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if read(t, path) != c.file {
				t.Fatal("the file changed")
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Fatalf("files left beside the settings file: %v", entries)
			}
		})
	}
	if err := Unset(write(t, "codex.home = \"~/c\"\n"), "codex.home"); err == nil || !strings.Contains(err.Error(), "safely") {
		t.Fatalf("unset of a dotted key: %v", err)
	}
	if err := Set("", "refresh", "5s"); err == nil || !strings.Contains(err.Error(), "no settings file location") {
		t.Fatalf("empty path: %v", err)
	}
}
