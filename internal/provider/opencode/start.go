package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// A profile is a connection: an account OpenCode is signed in to. The
// connection's ID is OpenCode's provider ID. Credentials are in the same
// responses, so only the fields below are decoded.
type listedProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listedModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Variants   []struct {
		ID string `json:"id"`
	} `json:"variants"`
}

// startLists reads the connections and the models. Both are read from the
// local service, so an unreachable service is an error here.
func (o *Adapter) startLists(ctx context.Context) ([]listedProvider, []listedModel, error) {
	var providers struct {
		Data []listedProvider `json:"data"`
	}
	if err := o.request(ctx, "/api/provider", &providers); err != nil {
		return nil, nil, err
	}
	var models struct {
		Data []listedModel `json:"data"`
	}
	if err := o.request(ctx, "/api/model", &models); err != nil {
		return nil, nil, err
	}
	if providers.Data == nil || models.Data == nil {
		return nil, nil, fmt.Errorf("unexpected provider or model list; OpenCode V2 is required")
	}
	return providers.Data, models.Data, nil
}

// StartProfiles lists "OpenCode default" and then each connection that has at
// least one enabled model. When the service cannot be reached it returns the
// default alone, with the error, so the form can say why and offer only the
// default.
func (o *Adapter) StartProfiles(ctx context.Context) ([]provider.Profile, error) {
	// A missing command is found before anything is created: a connection
	// start creates the session through the API first.
	problem := ""
	if _, err := exec.LookPath("opencode"); err != nil {
		problem = "cannot find its command opencode"
	}
	profiles := []provider.Profile{{ID: "", Name: "OpenCode default", Detail: "OpenCode chooses the model", Problem: problem}}
	providers, models, err := o.startLists(ctx)
	if err != nil {
		return profiles, err
	}
	enabled := map[string]bool{}
	for _, m := range models {
		if m.Enabled {
			enabled[m.ProviderID] = true
		}
	}
	for _, p := range providers {
		if !enabled[p.ID] || !provider.ValidArgument(p.ID) {
			continue
		}
		name := session.Text(p.Name, 64)
		switch {
		case name == "" || name == p.ID:
			name = p.ID
		default:
			name = name + " (" + p.ID + ")"
		}
		profiles = append(profiles, provider.Profile{
			ID:      p.ID,
			Name:    session.Text(name, 100),
			Detail:  session.Text("connection "+p.ID, 200),
			Problem: problem,
		})
	}
	return profiles, nil
}

// StartCatalog lists the enabled models of a connection, with their variants
// as efforts. The default profile lists nothing: OpenCode picks its own model.
func (o *Adapter) StartCatalog(ctx context.Context, profile string) (provider.Catalog, error) {
	if profile == "" {
		return provider.Catalog{}, nil
	}
	_, models, err := o.startLists(ctx)
	if err != nil {
		return provider.Catalog{}, err
	}
	// A connection must name its model: OpenCode's own default may belong to
	// another connection.
	c := provider.Catalog{ModelRequired: true}
	for _, m := range models {
		if !m.Enabled || m.ProviderID != profile || !provider.ValidArgument(m.ID) {
			continue
		}
		model := provider.Model{ID: m.ID, Name: session.Text(m.Name, 100)}
		if model.Name == "" {
			model.Name = m.ID
		}
		for _, v := range m.Variants {
			if provider.ValidArgument(v.ID) {
				model.Efforts = append(model.Efforts, v.ID)
			}
		}
		c.Models = append(c.Models, model)
	}
	return c, nil
}

// Start returns the command for a new session. With the default profile and
// no model, it runs OpenCode in the folder and OpenCode creates the session.
// With a connection, it creates the session through that connection with the
// chosen model, so the session never runs on another connection.
//
// n has passed provider.CheckStart: the connection is listed now and the
// model is enabled under it, so the service is not asked again.
func (o *Adapter) Start(ctx context.Context, n provider.NewSession) (provider.Command, error) {
	// CheckStart has already refused any model on the default profile, whose
	// catalogue is empty.
	if n.Profile == "" {
		return provider.Command{Name: "opencode", Args: []string{n.Directory}, Directory: n.Directory}, nil
	}
	// The form lets a connection start on "Default" model. OpenCode would then
	// use its own default model, which may belong to another connection, so
	// nothing starts until a model of this connection is chosen.
	if n.Model == "" {
		return provider.Command{}, fmt.Errorf("choose a model to start through this connection")
	}
	id, err := o.createSession(ctx, n)
	if err != nil {
		return provider.Command{}, err
	}
	return provider.Command{Name: "opencode", Args: []string{"--session", id, n.Directory}, Directory: n.Directory, Created: id}, nil
}

// createSession asks OpenCode to create the session with the model, in the
// folder. The request names the connection and model; the reply's ID must be
// a plain OpenCode session ID before it becomes an argument.
func (o *Adapter) createSession(ctx context.Context, n provider.NewSession) (string, error) {
	model := map[string]string{"providerID": n.Profile, "id": n.Model}
	if n.Effort != "" {
		model["variant"] = n.Effort
	}
	body, err := json.Marshal(map[string]any{
		"location": map[string]string{"directory": filepath.Clean(n.Directory)},
		"model":    model,
	})
	if err != nil {
		return "", err
	}
	out, err := o.run(ctx, "api", "post", "/api/session", "--data", string(body))
	if err != nil {
		return "", err
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return "", fmt.Errorf("OpenCode session response: %w", err)
	}
	if !provider.ValidID(created.Data.ID) || !strings.HasPrefix(created.Data.ID, "ses") {
		return "", fmt.Errorf("OpenCode returned an unexpected session ID")
	}
	return created.Data.ID, nil
}
