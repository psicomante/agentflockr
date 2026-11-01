package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestFileCacheHandlesPartialAppendAndTruncation(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "09", "22")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout.jsonl")
	meta := `{"type":"session_meta","payload":{"id":"first","cwd":"/project"}}` + "\n"
	if err := os.WriteFile(path, []byte(meta+`{"type":"event_msg","timestamp":"2026-09-22T10:00:00Z","payload":`), 0600); err != nil {
		t.Fatal(err)
	}
	f := New(home)
	now := time.Date(2026, 9, 22, 10, 0, 1, 0, time.UTC)
	r := f.Scan(context.Background(), now)
	if len(r.Sessions) != 1 || r.Sessions[0].Status != session.Unknown {
		t.Fatalf("partial write: %+v", r)
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteString(`{"type":"task_complete"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	out.Close()
	r = f.Scan(context.Background(), now)
	if len(r.Sessions) != 1 || r.Sessions[0].Status != session.Completed {
		t.Fatalf("append lost event: %+v", r)
	}
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"replacement","cwd":"/other"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r = f.Scan(context.Background(), now)
	if len(r.Sessions) != 1 || r.Sessions[0].ID != "replacement" || r.Sessions[0].Status != session.Unknown {
		t.Fatalf("truncation retained old identity: %+v", r)
	}
}
