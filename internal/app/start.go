package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// errStartDemo refuses a start in demo mode. The dashboard never gets this
// far there: it says so itself (REQ-OPN-012).
var errStartDemo = errors.New("new sessions cannot be started in demo mode")

// StartAgents lists the watched agents that can start sessions, in display
// order. Demo mode starts nothing, so it lists none.
func (e *Engine) StartAgents() []provider.Adapter {
	if e.Demo {
		return nil
	}
	var agents []provider.Adapter
	for _, a := range e.Providers {
		if _, ok := a.(provider.Starter); ok {
			agents = append(agents, a)
		}
	}
	return agents
}

func (e *Engine) starter(agent session.Provider) (provider.Starter, error) {
	if e.Demo {
		return nil, errStartDemo
	}
	adapter := e.Providers.Get(agent)
	if adapter == nil {
		return nil, fmt.Errorf("%s is not among the agents being watched", agent)
	}
	starter, ok := adapter.(provider.Starter)
	if !ok {
		return nil, fmt.Errorf("%s sessions cannot be started from agentflockr", agent)
	}
	return starter, nil
}

// StartProfiles lists the launch contexts an agent offers. It reads only.
func (e *Engine) StartProfiles(ctx context.Context, agent session.Provider) ([]provider.Profile, error) {
	starter, err := e.starter(agent)
	if err != nil {
		return nil, err
	}
	return starter.StartProfiles(ctx)
}

// StartCatalog lists the models and efforts a profile can run. It reads only.
func (e *Engine) StartCatalog(ctx context.Context, agent session.Provider, profile string) (provider.Catalog, error) {
	starter, err := e.starter(agent)
	if err != nil {
		return provider.Catalog{}, err
	}
	return starter.StartCatalog(ctx, profile)
}

// Start returns the command that starts a new session, once
// provider.CheckStart has passed. Every check runs before the agent is asked
// to start anything, and runs once: the adapters trust what reaches them.
func (e *Engine) Start(ctx context.Context, agent session.Provider, n provider.NewSession) (provider.Command, error) {
	starter, err := e.starter(agent)
	if err != nil {
		return provider.Command{}, err
	}
	if err := provider.CheckStart(ctx, starter, n); err != nil {
		return provider.Command{}, err
	}
	return starter.Start(ctx, n)
}
