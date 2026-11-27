package jsonl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// appendFile adds text to the end of a file, as a running agent does.
func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestReadResumesFromTheLastOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	first := `{"id":"one"}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	var s session.Session
	offset, _, err := Read(path, 0, &s, apply)
	if err != nil || offset != int64(len(first)) || s.ID != "one" {
		t.Fatalf("first read: offset %d, id %q, err %v", offset, s.ID, err)
	}
	appendFile(t, path, `{"id":"two"}`+"\n")
	offset, _, err = Read(path, offset, &s, apply)
	if err != nil || s.ID != "two" {
		t.Fatalf("resumed read: id %q, err %v", s.ID, err)
	}
	if want := int64(len(first) + len(`{"id":"two"}`+"\n")); offset != want {
		t.Fatalf("offset %d, want %d", offset, want)
	}
}

func TestReadWaitsForAHalfWrittenLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	whole := `{"id":"one"}` + "\n"
	partial := `{"id":"tw`
	if err := os.WriteFile(path, []byte(whole+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	var s session.Session
	offset, _, err := Read(path, 0, &s, apply)
	if err != nil {
		t.Fatal(err)
	}
	// The partial line is not consumed, so the next scan can finish it.
	if offset != int64(len(whole)) || s.ID != "one" {
		t.Fatalf("offset %d (want %d), id %q: a partial line must not move the offset", offset, len(whole), s.ID)
	}
	appendFile(t, path, `o"}`+"\n")
	offset, _, err = Read(path, offset, &s, apply)
	if err != nil || s.ID != "two" || offset != int64(len(whole)+len(partial)+len(`o"}`+"\n")) {
		t.Fatalf("completed line: id %q, offset %d, err %v", s.ID, offset, err)
	}
}

func TestReadMovesPastARecordItCannotApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	bad := `{"id":"x"}` + "\n" + "not json at all\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	var s session.Session
	// A complete line that is not JSON is skipped with a warning, and the
	// offset moves past it, so the next scan does not read it again.
	offset, warnings, err := Read(path, 0, &s, apply)
	if err != nil || len(warnings) != 1 || s.ID != "x" || offset != int64(len(bad)) {
		t.Fatalf("bad line: offset %d, warnings %v, id %q, err %v", offset, warnings, s.ID, err)
	}
	// Valid JSON that apply rejects (an array, not an object) is skipped the same way.
	appendFile(t, path, `[1,2]`+"\n"+`{"id":"after"}`+"\n")
	offset, warnings, err = Read(path, offset, &s, apply)
	if err != nil || len(warnings) != 1 || s.ID != "after" {
		t.Fatalf("rejected record: warnings %v, id %q, err %v", warnings, s.ID, err)
	}
	if want := int64(len(bad) + len(`[1,2]`+"\n") + len(`{"id":"after"}`+"\n")); offset != want {
		t.Fatalf("offset %d, want %d", offset, want)
	}
}

func TestReadRefusesARecordOverTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	huge := strings.Repeat("a", provider.MaxRecordSize+1)
	if err := os.WriteFile(path, []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	var s session.Session
	offset, _, err := Read(path, 0, &s, apply)
	if err == nil || offset != 0 {
		t.Fatalf("an over-limit record must stop the read at its start: offset %d, err %v", offset, err)
	}
}

func TestScanReadsAShrunkFileFromTheStart(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "t.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"long-first-id","cwd":"`+dir+`"}`+"\n"+`{"id":"second"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &Source{Provider: session.Codex, Roots: []string{root}, Apply: apply}
	if got := src.Scan(context.Background(), time.Now()); len(got.Sessions) != 1 || got.Sessions[0].ID != "second" {
		t.Fatalf("first scan: %+v", got.Sessions)
	}
	// Rewritten smaller: the old offset is past the end, so the file must be
	// read again from the start, not from where the last scan stopped.
	if err := os.WriteFile(path, []byte(`{"id":"new","cwd":"`+dir+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := src.Scan(context.Background(), time.Now()); len(got.Sessions) != 1 || got.Sessions[0].ID != "new" {
		t.Fatalf("after the file shrank: %+v", got.Sessions)
	}
}

func TestScanDropsAFileThatDisappears(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "t.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"gone","cwd":"`+dir+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &Source{Provider: session.Codex, Roots: []string{root}, Apply: apply}
	if got := src.Scan(context.Background(), time.Now()); len(got.Sessions) != 1 {
		t.Fatalf("first scan: %+v", got.Sessions)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := src.Scan(context.Background(), time.Now()); len(got.Sessions) != 0 {
		t.Fatalf("a deleted transcript is still listed: %+v", got.Sessions)
	}
}
