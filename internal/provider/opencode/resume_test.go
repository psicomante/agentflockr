package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestOpenCodeRelocationUsesSupportedAPI(t *testing.T) {
	dir := t.TempDir()
	s := session.Session{ID: "ses_a", Provider: session.OpenCode, Directory: dir, OriginalDirectory: "/missing"}
	var got []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[1] == "get" {
			return json.Marshal(map[string]any{"data": map[string]any{"location": map[string]string{"directory": dir}}})
		}
		got = append([]string{name}, args...)
		return nil, nil
	}
	if _, err := (&Adapter{Run: run}).Resume(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got[3] != "/api/session/ses_a/move" {
		t.Fatalf("unexpected move: %v", got)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got[5]), &body); err != nil {
		t.Fatal(err)
	}
	if body["directory"] != dir {
		t.Fatal("wrong destination")
	}
}

func TestRelocatedOpenCodeMustConfirmNewLocation(t *testing.T) {
	dir := t.TempDir()
	s := session.Session{ID: "ses_a", Provider: session.OpenCode, Directory: dir, OriginalDirectory: "/missing"}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[1] == "get" {
			return []byte(`{"data":{"location":{"directory":"/missing"}}}`), nil
		}
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Adapter{Run: run}).Resume(ctx, s); err == nil {
		t.Fatal("must not launch while the move is still pending")
	}
}

func TestOpenCodeMoveAcceptsEquivalentResolvedDirectory(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	s := session.Session{ID: "ses_symlink", Provider: session.OpenCode, Directory: alias, OriginalDirectory: "/missing"}
	run := func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"data": map[string]any{"location": map[string]string{"directory": real}}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := (&Adapter{Run: run}).Resume(ctx, s); err != nil {
		t.Fatal(err)
	}
}
