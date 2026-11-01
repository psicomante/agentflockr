package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayTextCannotInjectTerminalCommands(t *testing.T) {
	for _, s := range []string{
		"hello\x1b[31m world\x1b[0m",
		"hello\x1b]52;c;secret\a world",
		"hello\x1b]8;;https://example.com\x1b\\ world\x1b]8;;\x1b\\",
		"hello\r\nworld",
	} {
		if got := Text(s, 100); got != "hello world" {
			t.Fatalf("unsafe or damaged text: %q", got)
		}
	}
}

func TestLinesKeepsLineBreaksOnly(t *testing.T) {
	got := Lines("\n\x1b[31mnpm   test\x1b[0m\r\n\nrm -rf‮ build\n\n", 0)
	if got != "npm test\n\nrm -rf build" {
		t.Fatalf("lines: %q", got)
	}
	if got := Lines("abcdef", 3); got != "abc…" {
		t.Fatalf("bound: %q", got)
	}
}

func TestBlockKeepsSpacingButNotEscapes(t *testing.T) {
	got := Block("\n{\n  \"command\":\t\"ls\"\x1b[31m\r\n}   \n\n", 0)
	if got != "{\n  \"command\":    \"ls\"\n}" {
		t.Fatalf("block: %q", got)
	}
	if got := Block("ok\x1b]52;c;secret\a done", 0); got != "ok done" {
		t.Fatalf("escape kept: %q", got)
	}
	if got := Block("abcdef", 3); got != "abc…" {
		t.Fatalf("bound: %q", got)
	}
}

func TestSplitKeyInvertsKey(t *testing.T) {
	s := Session{Provider: OpenCode, ID: "ses_a:b"}
	if p, id, ok := SplitKey(s.Key()); !ok || p != OpenCode || id != "ses_a:b" {
		t.Fatalf("split: %q %q %v", p, id, ok)
	}
	for _, key := range []string{"", "opencode", ":id", "opencode:"} {
		if _, _, ok := SplitKey(key); ok {
			t.Fatalf("accepted %q", key)
		}
	}
}

func TestMovedIgnoresSymlinksAndUncleanPaths(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if Moved(Session{Directory: dir, OriginalDirectory: alias}) || Moved(Session{Directory: dir, OriginalDirectory: dir + "/./"}) {
		t.Fatal("the same folder reached another way is not a move")
	}
	if !Moved(Session{Directory: dir, OriginalDirectory: t.TempDir()}) {
		t.Fatal("a different folder is a move")
	}
}
