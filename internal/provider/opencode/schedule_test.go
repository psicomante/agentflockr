package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// scheduleService fakes the calls a scheduled prompt makes. The turn shows as
// active for the first activeFor polls after the prompt is posted.
type scheduleService struct {
	mu          sync.Mutex
	credentials string
	activeFor   int
	posted      bool
	calls       []string
	body        string
	// model is the session's model as GET /api/session/ses_1 reports it,
	// and models the bodies posted to set it.
	model  string
	models []string
}

func (f *scheduleService) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	verb, path := args[1], args[2]
	f.calls = append(f.calls, verb+" "+path)
	switch {
	case verb == "get" && path == "/api/credential":
		return []byte(f.credentials), nil
	case verb == "post" && strings.HasSuffix(path, "/activate"):
		return []byte(`{}`), nil
	case verb == "post" && path == "/api/session/ses_1/prompt":
		f.posted, f.body = true, args[4]
		return []byte(`{"data":{}}`), nil
	case verb == "get" && path == "/api/session/ses_1":
		return []byte(`{"data":{"agent":"build","model":` + f.model + `}}`), nil
	case verb == "post" && path == "/api/session/ses_1/model":
		f.models = append(f.models, args[4])
		return []byte(`{"data":{}}`), nil
	case verb == "get" && path == "/api/session/active":
		if f.posted && f.activeFor > 0 {
			f.activeFor--
			return []byte(`{"data":{"ses_1":{}}}`), nil
		}
		return []byte(`{"data":{}}`), nil
	}
	return nil, fmt.Errorf("unexpected %s %s", verb, path)
}

func shortPolls(t *testing.T) {
	previous := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = previous })
}

const twoCredentials = `{"data":[
 {"id":"cred_work","integrationID":"anthropic","label":"Work","active":true,"value":{"type":"oauth","access":"secret"}},
 {"id":"cred_home","integrationID":"anthropic","label":"Home","active":false,"value":{"type":"oauth","access":"secret"}}]}`

func TestPromptSwitchesConnectionAndRestoresItAfterTheTurn(t *testing.T) {
	shortPolls(t)
	f := &scheduleService{credentials: twoCredentials, activeFor: 2}
	o := &Adapter{Run: f.run}
	s := session.Session{Provider: session.OpenCode, ID: "ses_1"}
	if err := o.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "ping", Connection: "cred_home"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"get /api/credential",
		"post /api/credential/cred_home/activate",
		"post /api/session/ses_1/prompt",
		"get /api/session/active", "get /api/session/active", "get /api/session/active",
		"post /api/credential/cred_work/activate",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n%v\nwant:\n%v", f.calls, want)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(f.body), &body); err != nil || body["text"] != "ping" || body["delivery"] != "queue" || !strings.HasPrefix(body["id"], "msg_") {
		t.Fatalf("prompt body: %s %v", f.body, err)
	}
}

func TestPromptRestoresTheConnectionWhenThePromptFails(t *testing.T) {
	f := &scheduleService{credentials: twoCredentials}
	o := &Adapter{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[2] == "/api/session/ses_1/prompt" {
			f.calls = append(f.calls, "post prompt (refused)")
			return nil, fmt.Errorf("refused")
		}
		return f.run(ctx, name, args...)
	}}
	err := o.Prompt(context.Background(), session.Session{Provider: session.OpenCode, ID: "ses_1"}, provider.ScheduledPrompt{Text: "ping", Connection: "cred_home"})
	if err == nil {
		t.Fatal("a refused prompt reported success")
	}
	if last := f.calls[len(f.calls)-1]; last != "post /api/credential/cred_work/activate" {
		t.Fatalf("previous connection not restored: %v", f.calls)
	}
}

// With nothing saved active, there would be nothing to put back: refuse.
func TestPromptRefusesToSwitchWithoutAConnectionToRestore(t *testing.T) {
	f := &scheduleService{credentials: `{"data":[{"id":"cred_home","integrationID":"anthropic","label":"Home","active":false,"value":{"type":"key"}}]}`}
	o := &Adapter{Run: f.run}
	err := o.Prompt(context.Background(), session.Session{Provider: session.OpenCode, ID: "ses_1"}, provider.ScheduledPrompt{Text: "ping", Connection: "cred_home"})
	if err == nil || !reflect.DeepEqual(f.calls, []string{"get /api/credential"}) {
		t.Fatalf("switched with nothing to restore: %v %v", err, f.calls)
	}
}

// Already on the chosen connection: post and switch nothing.
func TestPromptOnTheActiveConnectionSwitchesNothing(t *testing.T) {
	shortPolls(t)
	f := &scheduleService{credentials: twoCredentials}
	o := &Adapter{Run: f.run}
	if err := o.Prompt(context.Background(), session.Session{Provider: session.OpenCode, ID: "ses_1"}, provider.ScheduledPrompt{Text: "ping", Connection: "cred_work"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/activate") {
			t.Fatalf("switched although already active: %v", f.calls)
		}
	}
}

func TestConnectionsListOnlyPublicIdentity(t *testing.T) {
	f := &scheduleService{credentials: twoCredentials}
	list, err := (&Adapter{Run: f.run}).Connections(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("connections: %+v %v", list, err)
	}
	if list[0] != (provider.Connection{ID: "cred_work", Name: "Work (OAuth)", Integration: "anthropic", Active: true}) {
		t.Fatalf("first connection: %+v", list[0])
	}
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), "secret") {
		t.Fatalf("a secret was listed: %s", data)
	}
}

func TestPromptSwitchesTheModelAndRestoresItAfterTheTurn(t *testing.T) {
	shortPolls(t)
	f := &scheduleService{activeFor: 1, model: `{"id":"claude-sonnet","providerID":"anthropic","variant":"high"}`}
	o := &Adapter{Run: f.run}
	s := session.Session{Provider: session.OpenCode, ID: "ses_1"}
	if err := o.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "ping", Model: "anthropic/claude-opus", Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"model":{"id":"claude-opus","providerID":"anthropic","variant":"max"}}`,
		`{"model":{"id":"claude-sonnet","providerID":"anthropic","variant":"high"}}`,
	}
	if !reflect.DeepEqual(f.models, want) {
		t.Fatalf("models set:\n%v\nwant:\n%v", f.models, want)
	}
	if f.calls[len(f.calls)-1] != "post /api/session/ses_1/model" {
		t.Fatalf("the model was not restored last: %v", f.calls)
	}
}

func TestPromptOnTheSessionsOwnModelSwitchesNothing(t *testing.T) {
	f := &scheduleService{model: `{"id":"claude-opus","providerID":"anthropic"}`}
	o := &Adapter{Run: f.run}
	s := session.Session{Provider: session.OpenCode, ID: "ses_1"}
	if err := o.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "ping", Model: "anthropic/claude-opus"}); err != nil {
		t.Fatal(err)
	}
	if len(f.models) != 0 || !f.posted {
		t.Fatalf("models set %v, posted %v", f.models, f.posted)
	}
}

func TestPromptRefusesAModelItCannotRestore(t *testing.T) {
	f := &scheduleService{model: `{}`}
	o := &Adapter{Run: f.run}
	s := session.Session{Provider: session.OpenCode, ID: "ses_1"}
	if err := o.Prompt(context.Background(), s, provider.ScheduledPrompt{Text: "ping", Model: "anthropic/claude-opus"}); err == nil {
		t.Fatal("switched a model with nothing to restore")
	}
	if len(f.models) != 0 || f.posted {
		t.Fatalf("models set %v, posted %v", f.models, f.posted)
	}
}

// profileService fakes the calls a run through a connection makes: the
// connection and model lists, a session created for the run, its turn, and
// its deletion.
type profileService struct {
	mu        sync.Mutex
	calls     []string
	created   string
	activeFor int
	posted    bool
}

func (f *profileService) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	verb, path := args[1], args[2]
	f.calls = append(f.calls, verb+" "+path)
	switch {
	case verb == "get" && path == "/api/provider":
		return []byte(`{"data":[{"id":"anthropic","name":"Anthropic"}]}`), nil
	case verb == "get" && path == "/api/model":
		return []byte(`{"data":[{"id":"claude-opus","providerID":"anthropic","name":"Opus","enabled":true,"variants":[{"id":"max"}]}]}`), nil
	case verb == "post" && path == "/api/session":
		f.created = args[4]
		return []byte(`{"data":{"id":"ses_run"}}`), nil
	case verb == "post" && path == "/api/session/ses_run/prompt":
		f.posted = true
		return []byte(`{"data":{}}`), nil
	case verb == "get" && path == "/api/session/active":
		if f.posted && f.activeFor > 0 {
			f.activeFor--
			return []byte(`{"data":{"ses_run":{}}}`), nil
		}
		return []byte(`{"data":{}}`), nil
	case verb == "delete" && path == "/api/session/ses_run":
		return []byte(`{}`), nil
	}
	return nil, fmt.Errorf("unexpected %s %s", verb, path)
}

func TestPromptProfileRunsInASessionItDeletesAfterTheTurn(t *testing.T) {
	shortPolls(t)
	f := &profileService{activeFor: 1}
	o := &Adapter{Run: f.run, lookup: func(string) (string, error) { return "opencode", nil }}
	dir := t.TempDir()
	if err := o.PromptProfile(context.Background(), "anthropic", dir, provider.ScheduledPrompt{Text: "ping", Model: "claude-opus", Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(f.created), &created); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"location": map[string]any{"directory": dir},
		"model":    map[string]any{"id": "claude-opus", "providerID": "anthropic", "variant": "max"},
	}
	if !reflect.DeepEqual(created, want) {
		t.Fatalf("created with %v, want %v", created, want)
	}
	if !f.posted || f.calls[len(f.calls)-1] != "delete /api/session/ses_run" {
		t.Fatalf("calls: %v", f.calls)
	}

	// A connection needs one of its own models; nothing is created without.
	f = &profileService{}
	o.Run = f.run
	if err := o.PromptProfile(context.Background(), "anthropic", dir, provider.ScheduledPrompt{Text: "ping"}); err == nil || f.created != "" {
		t.Fatalf("ran without a model: %v %q", err, f.created)
	}
}
