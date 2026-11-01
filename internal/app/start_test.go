package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/provider/providertest"
	"github.com/psicomante/agentflockr/internal/session"
)

func startingEngine(demo bool, starter *providertest.Starter) Engine {
	fake := &providertest.Fake{Provider: session.Claude}
	return Engine{Demo: demo, Providers: provider.Registry{providertest.WithStarter(fake, starter)}}
}

func TestStartAgentsListsOnlyStarters(t *testing.T) {
	starter := &providertest.Starter{}
	plain := &providertest.Fake{Provider: session.Codex}
	e := Engine{Providers: provider.Registry{plain, providertest.WithStarter(&providertest.Fake{Provider: session.Claude}, starter)}}
	agents := e.StartAgents()
	if len(agents) != 1 || agents[0].ID() != session.Claude {
		t.Fatalf("wrong starter agents: %v", agents)
	}
	demo := Engine{Demo: true, Providers: e.Providers}
	if got := demo.StartAgents(); len(got) != 0 {
		t.Fatalf("demo mode offered agents: %v", got)
	}
}

func TestStartRefusesDemoAndMissingFolders(t *testing.T) {
	starter := &providertest.Starter{Profiles: []provider.Profile{{ID: "", Name: "default"}}}
	n := provider.NewSession{Directory: t.TempDir(), Profile: ""}

	demo := startingEngine(true, starter)
	if _, err := demo.Start(context.Background(), session.Claude, n); err == nil || !errors.Is(err, errStartDemo) {
		t.Fatalf("demo start: %v", err)
	}
	if _, err := demo.StartProfiles(context.Background(), session.Claude); err == nil {
		t.Fatal("demo mode listed profiles")
	}

	e := startingEngine(false, starter)
	gone := provider.NewSession{Directory: filepath.Join(t.TempDir(), "gone")}
	if _, err := e.Start(context.Background(), session.Claude, gone); err == nil {
		t.Fatal("a missing folder was started")
	}
	if _, err := e.Start(context.Background(), session.Claude, provider.NewSession{Directory: "relative"}); err == nil {
		t.Fatal("a relative folder was started")
	}
	if calls := starter.Calls(); len(calls) != 0 {
		t.Fatalf("agent was asked after a refused folder: %v", calls)
	}
	if got := starter.Started(); len(got) != 0 {
		t.Fatalf("session started: %v", got)
	}
}

func TestStartRechecksTheCatalogue(t *testing.T) {
	starter := &providertest.Starter{
		Profiles: []provider.Profile{{ID: "work", Name: "work"}},
		Catalogs: map[string]provider.Catalog{"work": {Models: []provider.Model{{ID: "opus", Efforts: []string{"high"}}}}},
	}
	e := startingEngine(false, starter)
	n := provider.NewSession{Directory: t.TempDir(), Profile: "work", Model: "opus", Effort: "high"}

	if _, err := e.Start(context.Background(), session.Claude, n); err != nil {
		t.Fatalf("offered choice refused: %v", err)
	}

	// The model was dropped after the form loaded: the start must refuse it
	// without asking the agent to start.
	starter.Catalogs["work"] = provider.Catalog{Models: []provider.Model{{ID: "haiku"}}}
	before := len(starter.Started())
	if _, err := e.Start(context.Background(), session.Claude, n); err == nil {
		t.Fatal("a model no longer offered was started")
	}
	if got := len(starter.Started()); got != before {
		t.Fatalf("refused choice still reached Start: %d new starts", got-before)
	}
}

func TestStartRefusesAnUnusableProfile(t *testing.T) {
	starter := &providertest.Starter{
		Profiles: []provider.Profile{{ID: "work", Name: "work", Problem: "cannot find its command claude-work"}},
		Catalogs: map[string]provider.Catalog{"work": {}},
	}
	e := startingEngine(false, starter)
	_, err := e.Start(context.Background(), session.Claude, provider.NewSession{Directory: t.TempDir(), Profile: "work"})
	if err == nil || err.Error() != "cannot find its command claude-work" {
		t.Fatalf("unusable profile: %v", err)
	}
	if got := starter.Started(); len(got) != 0 {
		t.Fatal("an unusable profile started a session")
	}
}

// REQ-OPN-008: when the catalogue cannot be read, Default model and effort
// still start, since they pass nothing to check; an explicit model does not.
func TestStartWithDefaultsNeedsNoCatalogue(t *testing.T) {
	starter := &providertest.Starter{
		Profiles:   []provider.Profile{{ID: "", Name: "default"}},
		CatalogErr: errors.New("could not read models"),
	}
	e := startingEngine(false, starter)
	dir := t.TempDir()
	if _, err := e.Start(context.Background(), session.Claude, provider.NewSession{Directory: dir}); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
	if _, err := e.Start(context.Background(), session.Claude, provider.NewSession{Directory: dir, Model: "opus"}); err == nil {
		t.Fatal("a model was started without a catalogue to check it against")
	}
	if got := starter.Started(); len(got) != 1 || got[0].Model != "" {
		t.Fatalf("started %+v, want only the defaults", got)
	}
}

// Every check runs once, in the engine: the profiles and the catalogue are
// read once each before the agent's Start, which reads neither again.
func TestStartChecksOnce(t *testing.T) {
	starter := &providertest.Starter{
		Profiles: []provider.Profile{{ID: "work", Name: "work"}},
		Catalogs: map[string]provider.Catalog{"work": {Models: []provider.Model{{ID: "opus", Efforts: []string{"high"}}}}},
	}
	e := startingEngine(false, starter)
	if _, err := e.Start(context.Background(), session.Claude, provider.NewSession{Directory: t.TempDir(), Profile: "work", Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if got := starter.Calls(); !reflect.DeepEqual(got, []string{"profiles", "catalog:work", "start"}) {
		t.Fatalf("calls %v, want each check once, then the start", got)
	}
}
