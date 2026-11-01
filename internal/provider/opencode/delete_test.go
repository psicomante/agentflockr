package opencode

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestDeleteUsesExactIDAndPropagatesFailure(t *testing.T) {
	var calls [][]string
	a := &Adapter{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if args[1] == "get" {
			return []byte(`{"data":{}}`), nil
		}
		return nil, fmt.Errorf("native failure")
	}}
	if err := a.Delete(context.Background(), session.Session{Provider: session.OpenCode, ID: "ses_test"}); err == nil {
		t.Fatal("failure hidden")
	}
	if want := []string{"opencode", "api", "delete", "/api/session/ses_test"}; !reflect.DeepEqual(calls[len(calls)-1], want) {
		t.Fatalf("wrong command: %v", calls)
	}
}

func TestActiveAndInvalidIDsAreNotDeleted(t *testing.T) {
	a := &Adapter{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[1] != "get" {
			t.Fatal("active session deleted")
		}
		return []byte(`{"data":{"ses_active":{"type":"running"}}}`), nil
	}}
	if err := a.Delete(context.Background(), session.Session{Provider: session.OpenCode, ID: "ses_active"}); err == nil {
		t.Fatal("accepted active session")
	}
	for _, id := range []string{"--all", "../ses", "named-session"} {
		if err := a.Delete(context.Background(), session.Session{Provider: session.OpenCode, ID: id}); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
}
