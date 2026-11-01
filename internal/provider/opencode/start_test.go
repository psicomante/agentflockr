package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
)

// standInOpenCode puts an opencode stand-in on PATH, so the command check
// passes without the installed OpenCode. The tests' Run never executes it.
func standInOpenCode(t *testing.T) {
	t.Helper()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "opencode"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeService answers the listing requests like OpenCode's local service and
// records every request, so a test can prove no session was created.
type fakeService struct {
	providers, models string
	down              bool
	calls             []string
	created           map[string]any
}

const serviceProviders = `{"data":[
  {"id":"work","name":"Work","activation":"enabled","settings":{"apiKey":"secret-key"}},
  {"id":"empty","name":"Empty"},
  {"id":"home","name":""}
]}`

const serviceModels = `{"data":[
  {"id":"m1","providerID":"work","name":"Model One","enabled":true,"variants":[{"id":"low"},{"id":"high"}]},
  {"id":"m2","providerID":"work","name":"Model Two","enabled":false,"variants":[]},
  {"id":"e1","providerID":"empty","name":"Disabled","enabled":false,"variants":[]},
  {"id":"h1","providerID":"home","name":"","enabled":true,"variants":[]}
]}`

func (f *fakeService) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if f.down {
		return nil, fmt.Errorf("opencode: service unreachable")
	}
	if name != "opencode" || len(args) < 2 || args[0] != "api" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	switch {
	case args[1] == "get" && args[2] == "/api/provider":
		return []byte(f.providers), nil
	case args[1] == "get" && args[2] == "/api/model":
		return []byte(f.models), nil
	case args[1] == "post" && args[2] == "/api/session":
		if f.created == nil {
			f.created = map[string]any{}
		}
		if err := json.Unmarshal([]byte(args[4]), &f.created); err != nil {
			return nil, err
		}
		return []byte(`{"data":{"id":"ses_new1","location":{"directory":"/x"}}}`), nil
	}
	return nil, fmt.Errorf("unexpected request %v", args)
}

func (f *fakeService) adapter() *Adapter {
	return &Adapter{Run: f.run}
}

func (f *fakeService) postCount() int {
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, " post ") {
			n++
		}
	}
	return n
}

func TestConnectionsAreProfiles(t *testing.T) {
	standInOpenCode(t)
	f := &fakeService{providers: serviceProviders, models: serviceModels}
	profiles, err := f.adapter().StartProfiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range profiles {
		got = append(got, p.ID+"|"+p.Name)
		if strings.Contains(p.Name, "secret") || strings.Contains(p.Detail, "secret") {
			t.Fatalf("a credential reached the form: %+v", p)
		}
	}
	// "empty" has no enabled model, so it is not a connection. "home" has no
	// name, so its ID is its name.
	want := []string{"|OpenCode default", "work|Work (work)", "home|home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles %q, want %q", got, want)
	}
	// A connection has no default model: OpenCode's own may belong to
	// another connection. OpenCode's default profile keeps Default.
	connection, err := f.adapter().StartCatalog(context.Background(), "work")
	if err != nil || !connection.ModelRequired || len(connection.Models) == 0 {
		t.Fatalf("connection catalogue %+v %v, want a required model", connection, err)
	}
	if def, err := f.adapter().StartCatalog(context.Background(), ""); err != nil || def.ModelRequired {
		t.Fatalf("default catalogue %+v %v", def, err)
	}
}

func TestStartCreatesTheSessionWithTheModel(t *testing.T) {
	standInOpenCode(t)
	f := &fakeService{providers: serviceProviders, models: serviceModels}
	dir := t.TempDir()
	n := provider.NewSession{Directory: dir, Profile: "work", Model: "m1", Effort: "high"}
	a := f.adapter()
	if err := provider.CheckStart(context.Background(), a, n); err != nil {
		t.Fatal(err)
	}
	// Once checked, the start asks the service only to create the session:
	// the connection and its models are not listed again.
	f.calls = nil
	cmd, err := a.Start(context.Background(), n)
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "post /api/session") {
		t.Fatalf("the start asked the service %v, want only the create request", f.calls)
	}
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "opencode" || !reflect.DeepEqual(cmd.Args, []string{"--session", "ses_new1", dir}) || cmd.Directory != dir || cmd.Created != "ses_new1" {
		t.Fatalf("command %s %v in %q", cmd.Name, cmd.Args, cmd.Directory)
	}
	location, _ := f.created["location"].(map[string]any)
	model, _ := f.created["model"].(map[string]any)
	if location["directory"] != dir || model["providerID"] != "work" || model["id"] != "m1" || model["variant"] != "high" {
		t.Fatalf("session request %v", f.created)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "--server") || strings.Contains(c, "--standalone") {
			t.Fatalf("a private or remote server was named: %s", c)
		}
	}
}

func TestStartRefusesAnotherConnection(t *testing.T) {
	standInOpenCode(t)
	dir := t.TempDir()
	cases := map[string]provider.NewSession{
		// A model that is disabled under the connection.
		"disabled model": {Directory: dir, Profile: "work", Model: "m2"},
		// A model that belongs to another connection.
		"other connection's model": {Directory: dir, Profile: "work", Model: "h1"},
		// A connection on "Default" model would run OpenCode's own default.
		"default model on a connection": {Directory: dir, Profile: "work"},
		// The connection is no longer listed.
		"connection gone": {Directory: dir, Profile: "vanished", Model: "m1"},
	}
	for name, n := range cases {
		f := &fakeService{providers: serviceProviders, models: serviceModels}
		if _, err := providertest.Start(context.Background(), f.adapter(), n); err == nil {
			t.Errorf("%s: started", name)
		}
		if f.postCount() != 0 {
			t.Errorf("%s: a session was created: %v", name, f.calls)
		}
	}
	// A connection the service cannot list is refused with the service's error.
	down := &fakeService{down: true}
	if _, err := providertest.Start(context.Background(), down.adapter(), provider.NewSession{Directory: dir, Profile: "work", Model: "m1"}); err == nil || down.postCount() != 0 {
		t.Fatalf("connection with the service down: %v, posts %d", err, down.postCount())
	}
}

func TestStartWithDefaultsRunsOpenCodeInTheFolder(t *testing.T) {
	standInOpenCode(t)
	dir := t.TempDir()
	f := &fakeService{providers: serviceProviders, models: serviceModels}
	cmd, err := providertest.Start(context.Background(), f.adapter(), provider.NewSession{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name != "opencode" || !reflect.DeepEqual(cmd.Args, []string{dir}) || cmd.Directory != dir {
		t.Fatalf("command %s %v in %q", cmd.Name, cmd.Args, cmd.Directory)
	}
	if f.postCount() != 0 {
		t.Fatalf("the default started through the API: %v", f.calls)
	}
	// OpenCode's default needs no connection, so it starts with the service down.
	down := &fakeService{down: true}
	if _, err := providertest.Start(context.Background(), down.adapter(), provider.NewSession{Directory: dir}); err != nil {
		t.Fatalf("default with the service down: %v", err)
	}
	// And it is the only profile the form then offers.
	profiles, err := down.adapter().StartProfiles(context.Background())
	if err == nil || len(profiles) != 1 || profiles[0].ID != "" {
		t.Fatalf("service down: %+v %v", profiles, err)
	}
}

func TestMissingOpenCodeCommandCreatesNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	f := &fakeService{providers: serviceProviders, models: serviceModels}
	dir := t.TempDir()
	if _, err := providertest.Start(context.Background(), f.adapter(), provider.NewSession{Directory: dir, Profile: "work", Model: "m1"}); err == nil || !strings.Contains(err.Error(), "cannot find its command opencode") {
		t.Fatalf("missing command: %v", err)
	}
	if f.postCount() != 0 {
		t.Fatal("a session was created before the command was found")
	}
}
