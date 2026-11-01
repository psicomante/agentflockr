package opencode

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestOpenCodePaginationAndPendingInput(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		path := args[2]
		switch {
		case strings.Contains(path, "/permission"):
			return []byte(`{"data":[{"id":"per_1","sessionID":"ses_a","action":"shell"}]}`), nil
		case strings.Contains(path, "/form"):
			return []byte(`{"data":[]}`), nil
		case path == "/api/session/active":
			return []byte(`{"data":{"ses_a":{"type":"running"}}}`), nil
		case strings.Contains(path, "cursor=page2"):
			return []byte(`{"data":[{"id":"ses_b","title":"Older","outcome":"succeeded","location":{"directory":"/b"},"time":{"updated":1000}}],"cursor":{"next":null}}`), nil
		case strings.HasPrefix(path, "/api/session?"):
			return []byte(`{"data":[{"id":"ses_a","title":"Current","location":{"directory":"/a"},"time":{"updated":2000}}],"cursor":{"next":"page2"}}`), nil
		}
		return nil, fmt.Errorf("unexpected request %s", path)
	}
	result := (&Adapter{Run: run}).Scan(context.Background(), time.Now())
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(result.Sessions) != 2 {
		t.Fatalf("pagination lost history: %+v", result)
	}
	if result.Sessions[0].Status != session.Waiting || result.Sessions[1].Status != session.Completed {
		t.Fatalf("incorrect state: %+v", result.Sessions)
	}
}

func TestOpenCodeFailureDoesNotPretendEmptySuccess(t *testing.T) {
	r := (&Adapter{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("service unavailable")
	}}).Scan(context.Background(), time.Now())
	if r.Err == nil {
		t.Fatal("failure must be exposed")
	}
}

func TestOpenCodeRejectsUnexpectedResponseShape(t *testing.T) {
	r := (&Adapter{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"error":"wrong API version"}`), nil
	}}).Scan(context.Background(), time.Now())
	if r.Err == nil {
		t.Fatal("unrecognized response was treated as an empty portfolio")
	}
}

// A listing that never ends must stop, keeping what it read, with a warning.
func TestOpenCodeStopsAfterTooManyPages(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		path := args[2]
		if path == "/api/session/active" {
			return []byte(`{"data":{}}`), nil
		}
		calls++
		return []byte(fmt.Sprintf(`{"data":[{"id":"ses_%d","title":"t","location":{"directory":"/a"},"time":{"updated":1}}],"cursor":{"next":"c%d"}}`, calls, calls)), nil
	}
	result := (&Adapter{Run: run}).Scan(context.Background(), time.Now())
	if result.Err != nil || len(result.Sessions) != maxPages || len(result.Warnings) != 1 {
		t.Fatalf("got %d sessions, %d warnings, err %v", len(result.Sessions), len(result.Warnings), result.Err)
	}
}
