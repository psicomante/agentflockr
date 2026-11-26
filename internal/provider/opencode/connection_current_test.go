package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCurrentConnectionUsesActiveCredentialWithoutPersistingSecrets(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch path := args[2]; {
		case path == "/api/session/active":
			return []byte(`{"data":{}}`), nil
		case strings.HasPrefix(path, "/api/session?"):
			return []byte(`{"data":[{"id":"ses_1","model":{"providerID":"openai","id":"model"},"location":{"directory":"/a"},"time":{"updated":1}}]}`), nil
		case path == "/api/provider":
			return []byte(`{"data":[{"id":"openai","integrationID":"openai"}]}`), nil
		case path == "/api/credential":
			return []byte(`{"data":[{"integrationID":"openai","label":"Work","active":true,"value":{"type":"oauth","access":"secret-token","refresh":"secret-refresh"}}]}`), nil
		case path == "/api/integration":
			return []byte(`{"data":[]}`), nil
		default:
			return nil, fmt.Errorf("unexpected request")
		}
	}
	r := (&Adapter{Run: run}).Scan(context.Background(), time.Now())
	if r.Err != nil || len(r.Sessions) != 1 || r.Sessions[0].CurrentConnection != "openai · Work (OAuth)" {
		t.Fatalf("unexpected result: %+v", r)
	}
	data, err := json.Marshal(r.Sessions[0])
	if err != nil || strings.Contains(string(data), "secret") || strings.Contains(string(data), "Work") {
		t.Fatalf("live credential metadata entered the index: %s, %v", data, err)
	}
}

func TestCurrentConnectionDoesNotGuessAmbiguousCredential(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[2] {
		case "/api/provider":
			return []byte(`{"data":[{"id":"openai"}]}`), nil
		case "/api/credential":
			return []byte(`{"data":[{"integrationID":"openai","label":"A","active":true,"value":{"type":"key"}},{"integrationID":"openai","label":"B","active":true,"value":{"type":"key"}}]}`), nil
		case "/api/integration":
			return []byte(`{"data":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected request")
	}
	if got := (&Adapter{Run: run}).currentConnections(context.Background()); got["openai"] != "" {
		t.Fatalf("ambiguous credential: %v", got)
	}
}
