package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

const testUUID = "11111111-2222-4333-8444-555555555555"

func TestDeleteUsesExactIDAndPropagatesFailure(t *testing.T) {
	var calls [][]string
	a := &Adapter{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, fmt.Errorf("native failure")
	}}
	if err := a.Delete(context.Background(), session.Session{Provider: session.Codex, ID: testUUID}); err == nil {
		t.Fatal("failure hidden")
	}
	if want := []string{"codex", "delete", "--force", testUUID}; len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("wrong command: %v", calls)
	}
}

func TestDeleteRejectsNonUUIDs(t *testing.T) {
	a := &Adapter{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("non-UUID reached the native CLI")
		return nil, nil
	}}
	for _, id := range []string{"--all", "../other", "named-session"} {
		if err := a.Delete(context.Background(), session.Session{Provider: session.Codex, ID: id}); err == nil {
			t.Fatalf("accepted non-UUID %q", id)
		}
	}
}

func TestCodexDeletionHonorsConfiguredHome(t *testing.T) {
	tools := t.TempDir()
	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "home")
	code := "#!/bin/sh\nprintf '%s' \"$CODEX_HOME\" > \"$DELETE_HOME_LOG\"\n"
	if err := os.WriteFile(filepath.Join(tools, "codex"), []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", "/incorrect/inherited/home")
	t.Setenv("DELETE_HOME_LOG", log)
	d := New(home)
	if err := d.Delete(context.Background(), session.Session{Provider: session.Codex, ID: testUUID}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != home {
		t.Fatalf("wrong deletion home: %q %v", data, err)
	}
}

func TestCodexRefusesCachedTranscriptFromAnotherHome(t *testing.T) {
	d := &Adapter{Home: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("wrong home was used to delete a session")
		return nil, nil
	}}
	s := session.Session{Provider: session.Codex, ID: testUUID, Transcript: filepath.Join(t.TempDir(), "sessions", "rollout.jsonl")}
	if err := d.Delete(context.Background(), s); err == nil {
		t.Fatal("accepted another provider home")
	}
}
