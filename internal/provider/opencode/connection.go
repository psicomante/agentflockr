package opencode

import (
	"context"
	"strings"

	"github.com/psicomante/agentflockr/internal/session"
)

// The dashboard shows each session's current connection, and `serve` also
// lists and switches connections (service_connections.go). Only the
// dashboard's part lives here, so it does not depend on the service.

// connectionMethod names a credential's authentication method, or returns ""
// for a method agentflockr does not know.
func connectionMethod(method string) string {
	switch method {
	case "key":
		return "API key"
	case "oauth":
		return "OAuth"
	case "external":
		return "external"
	default:
		return ""
	}
}

// connectionLabel names a saved credential by its label and method.
func connectionLabel(label, method string) string {
	label = session.Text(label, 100)
	if label == "" {
		label = "Unnamed connection"
	}
	return label + " (" + method + ")"
}

// integrationOf returns the integration a provider's credentials belong to:
// its own ID when OpenCode names none.
func integrationOf(providerID, integrationID string) string {
	if integrationID == "" {
		return providerID
	}
	return integrationID
}

// currentConnections reads only public credential metadata. OpenCode's
// credential response also contains secrets; they are never decoded into
// fields, logged, returned, or written to the index.
func (o *Adapter) currentConnections(ctx context.Context) map[string]string {
	var providers struct {
		Data []struct {
			ID            string `json:"id"`
			IntegrationID string `json:"integrationID"`
		} `json:"data"`
	}
	var credentials struct {
		Data []struct {
			IntegrationID string `json:"integrationID"`
			Label         string `json:"label"`
			Active        bool   `json:"active"`
			Value         struct {
				Type string `json:"type"`
			} `json:"value"`
		} `json:"data"`
	}
	var integrations struct {
		Data []struct {
			ID          string `json:"id"`
			Connections []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"connections"`
		} `json:"data"`
	}
	if o.request(ctx, "/api/provider", &providers) != nil || providers.Data == nil ||
		o.request(ctx, "/api/credential", &credentials) != nil || credentials.Data == nil {
		return nil
	}
	// Environment connections are fallbacks only when no saved credential is active.
	_ = o.request(ctx, "/api/integration", &integrations)
	active := map[string]string{}
	ambiguous := map[string]bool{}
	for _, c := range credentials.Data {
		if !c.Active || c.IntegrationID == "" {
			continue
		}
		if _, exists := active[c.IntegrationID]; exists {
			ambiguous[c.IntegrationID] = true
			continue
		}
		method := connectionMethod(c.Value.Type)
		if method == "" {
			continue
		}
		active[c.IntegrationID] = connectionLabel(c.Label, method)
	}
	for _, i := range integrations.Data {
		if active[i.ID] != "" || ambiguous[i.ID] {
			continue
		}
		for _, c := range i.Connections {
			if c.Type != "env" {
				continue
			}
			if active[i.ID] != "" {
				ambiguous[i.ID] = true
				break
			}
			active[i.ID] = "Environment (" + session.Text(c.Name, 100) + ")"
		}
	}
	result := map[string]string{}
	for _, p := range providers.Data {
		id := integrationOf(p.ID, p.IntegrationID)
		if !ambiguous[id] && strings.TrimSpace(active[id]) != "" {
			result[p.ID] = active[id]
		}
	}
	return result
}

// nativeModel is the model OpenCode records for a session: Scan reads it, and
// the service names its connection (modelInfo).
type nativeModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}
