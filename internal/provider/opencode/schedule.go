package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

const (
	// turnTimeout bounds the wait for a scheduled prompt's turn to end before
	// the previous connection is restored anyway.
	turnTimeout = 15 * time.Minute
	// turnStartGrace is how long a queued prompt may take to show up as an
	// active turn. A prompt that never does (it failed at once, or ended
	// between two polls) is taken as finished after it.
	turnStartGrace = 30 * time.Second
	// restoreTimeout bounds restoring the previous connection, which runs
	// even after the run's own context has ended.
	restoreTimeout = 30 * time.Second
)

// pollInterval is how often a run looks at the active turns; tests shorten it.
var pollInterval = 2 * time.Second

// savedCredential is the public identity of one saved credential. The
// service's response also holds secrets; they are never decoded.
type savedCredential struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integrationID"`
	Label         string `json:"label"`
	Active        bool   `json:"active"`
	Value         struct {
		Type string `json:"type"`
	} `json:"value"`
}

func (o *Adapter) credentials(ctx context.Context) ([]savedCredential, error) {
	var response struct {
		Data []savedCredential `json:"data"`
	}
	if err := o.request(ctx, "/api/credential", &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("OpenCode listed no credentials")
	}
	return response.Data, nil
}

// Connections lists every saved credential with a known method, for the
// scheduled prompt form.
func (o *Adapter) Connections(ctx context.Context) ([]provider.Connection, error) {
	credentials, err := o.credentials(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.Connection
	for _, c := range credentials {
		method := connectionMethod(c.Value.Type)
		if !provider.ValidArgument(c.ID) || c.IntegrationID == "" || method == "" {
			continue
		}
		out = append(out, provider.Connection{
			ID: c.ID, Name: connectionLabel(c.Label, method),
			Integration: session.Text(c.IntegrationID, 100), Active: c.Active,
		})
	}
	return out, nil
}

// Prompt queues the scheduled prompt in the session. With a connection, it
// first makes that credential the active one of its integration, remembering
// the one active before, and puts that one back once the turn has ended:
// OpenCode reads the active credential while the turn runs, so restoring it
// earlier would run the turn on the old one.
func (o *Adapter) Prompt(ctx context.Context, s session.Session, p provider.ScheduledPrompt) (err error) {
	text, err := provider.CleanPrompt(p.Text)
	if err != nil {
		return err
	}
	if !provider.ValidID(s.ID) || !strings.HasPrefix(s.ID, "ses") {
		return fmt.Errorf("invalid OpenCode session ID")
	}
	previous := ""
	if p.Connection != "" {
		if previous, err = o.switchTo(ctx, p.Connection); err != nil {
			return err
		}
		if previous != "" {
			defer func() {
				// The run's context may be over; restoring must still happen.
				restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
				defer cancel()
				if rerr := o.activate(restore, previous); rerr != nil {
					err = errors.Join(err, fmt.Errorf("could not restore the previous OpenCode connection: %w", rerr))
				}
			}()
		}
	}
	// The model is switched after the connection and, by the deferred calls'
	// order, restored before it: the session's model belongs to the
	// connection that is active while it is set back.
	var previousModel *nativeModel
	if p.Model != "" {
		if previousModel, err = o.switchModel(ctx, s.ID, p.Model, p.Effort); err != nil {
			return err
		}
		if previousModel != nil {
			defer func() {
				restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
				defer cancel()
				if rerr := o.setModel(restore, s.ID, *previousModel); rerr != nil {
					err = errors.Join(err, fmt.Errorf("could not restore the session's previous OpenCode model: %w", rerr))
				}
			}()
		}
	}
	body, err := json.Marshal(map[string]string{"id": o.ids.next(time.Now()), "text": text, "delivery": string(provider.Queue)})
	if err != nil {
		return err
	}
	if _, err := o.run(ctx, "api", "post", "/api/session/"+url.PathEscape(s.ID)+"/prompt", "--data", string(body)); err != nil {
		return err
	}
	if previous == "" && previousModel == nil {
		// Nothing to restore, so there is no reason to wait for the turn.
		return nil
	}
	return o.waitTurn(ctx, s.ID)
}

// PromptProfile runs the prompt through a connection outside any of the
// person's sessions: it creates a session in dir on the connection and model,
// queues the prompt, waits for the turn to end, and deletes the session, so
// the runs never pile up. OpenCode has no way to run a prompt it does not
// keep. A turn that does not end in time keeps its session, and the error
// names it. profile is a StartProfiles ID: "" is OpenCode's default.
func (o *Adapter) PromptProfile(ctx context.Context, profile, dir string, p provider.ScheduledPrompt) (err error) {
	text, err := provider.CleanPrompt(p.Text)
	if err != nil {
		return err
	}
	profiles, err := o.StartProfiles(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, pr := range profiles {
		if pr.ID == profile {
			found = true
			if pr.Problem != "" {
				return fmt.Errorf("the connection cannot run: %s", pr.Problem)
			}
		}
	}
	if !found {
		return fmt.Errorf("that OpenCode connection is no longer offered")
	}
	catalog, err := o.StartCatalog(ctx, profile)
	if err != nil {
		return fmt.Errorf("could not check the model: %w", err)
	}
	if err := provider.CheckChoice(catalog, p.Model, p.Effort); err != nil {
		return fmt.Errorf("%w: %v", provider.ErrInvalid, err)
	}
	request := map[string]any{"location": map[string]string{"directory": filepath.Clean(dir)}}
	if profile != "" {
		// The connection's catalogue required a model; it names the
		// connection, so the run never lands on another one.
		request["model"] = modelRef(profile+"/"+p.Model, p.Effort)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	out, err := o.run(ctx, "api", "post", "/api/session", "--data", string(body))
	if err != nil {
		return err
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return fmt.Errorf("OpenCode session response: %w", err)
	}
	id := created.Data.ID
	if !provider.ValidID(id) || !strings.HasPrefix(id, "ses") {
		return fmt.Errorf("OpenCode returned an unexpected session ID")
	}
	ended := false
	defer func() {
		if !ended {
			err = errors.Join(err, fmt.Errorf("OpenCode session %s is kept: its turn did not end", id))
			return
		}
		// The run's context may be over; the session is deleted anyway.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if _, derr := o.run(cleanup, "api", "delete", "/api/session/"+url.PathEscape(id)); derr != nil {
			err = errors.Join(err, fmt.Errorf("could not delete OpenCode session %s after the run: %w", id, derr))
		}
	}()
	prompt, err := json.Marshal(map[string]string{"id": o.ids.next(time.Now()), "text": text, "delivery": string(provider.Queue)})
	if err != nil {
		ended = true
		return err
	}
	if _, err := o.run(ctx, "api", "post", "/api/session/"+url.PathEscape(id)+"/prompt", "--data", string(prompt)); err != nil {
		// Nothing was queued, so there is no turn to wait for.
		ended = true
		return err
	}
	if err := o.waitTurn(ctx, id); err != nil {
		return err
	}
	ended = true
	return nil
}

// PromptCatalog lists the enabled models; a model's variants are its
// efforts. IDs are "provider/model", as OpenCode spells them. OpenCode's own
// default needs no effort list: effort is chosen with a model.
func (o *Adapter) PromptCatalog(ctx context.Context, _ session.Session) (provider.Catalog, error) {
	var list struct {
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Name       string `json:"name"`
			Enabled    bool   `json:"enabled"`
			Variants   []struct {
				ID string `json:"id"`
			} `json:"variants"`
		} `json:"data"`
	}
	if err := o.request(ctx, "/api/model", &list); err != nil {
		return provider.Catalog{}, err
	}
	var models []provider.Model
	for _, m := range list.Data {
		id := m.ProviderID + "/" + m.ID
		if !m.Enabled || m.ID == "" || m.ProviderID == "" || !provider.ValidArgument(id) {
			continue
		}
		model := provider.Model{ID: id, Name: session.Text(m.Name, 100), Connection: session.Text(m.ProviderID, 100)}
		for _, v := range m.Variants {
			if provider.ValidArgument(v.ID) {
				model.Efforts = append(model.Efforts, v.ID)
			}
		}
		models = append(models, model)
	}
	return provider.Catalog{Models: models}, nil
}

// switchModel sets the session's model for a run and returns the one it had,
// or nil when the session already ran the chosen model and effort. It
// refuses to switch when the session's model cannot be read, since nothing
// could be restored.
func (o *Adapter) switchModel(ctx context.Context, id, model, effort string) (*nativeModel, error) {
	providerID, modelID, ok := strings.Cut(model, "/")
	if !ok || providerID == "" || modelID == "" || !provider.ValidArgument(model) || (effort != "" && !provider.ValidArgument(effort)) {
		return nil, fmt.Errorf("%w: invalid OpenCode model", provider.ErrInvalid)
	}
	var detail struct {
		Data *struct {
			Model nativeModel `json:"model"`
		} `json:"data"`
	}
	if err := o.request(ctx, "/api/session/"+url.PathEscape(id), &detail); err != nil {
		return nil, fmt.Errorf("could not read the session's model: %w", err)
	}
	if detail.Data == nil || detail.Data.Model.ID == "" || detail.Data.Model.ProviderID == "" {
		return nil, fmt.Errorf("the session's model is unknown, so it could not be restored")
	}
	previous := detail.Data.Model
	chosen := nativeModel{ID: modelID, ProviderID: providerID, Variant: effort}
	if previous == chosen {
		return nil, nil
	}
	if err := o.setModel(ctx, id, chosen); err != nil {
		return nil, err
	}
	return &previous, nil
}

// setModel sets the session's model for its next turns.
func (o *Adapter) setModel(ctx context.Context, id string, m nativeModel) error {
	body, err := json.Marshal(map[string]any{"model": modelRef(m.ProviderID+"/"+m.ID, m.Variant)})
	if err != nil {
		return err
	}
	_, err = o.run(ctx, "api", "post", "/api/session/"+url.PathEscape(id)+"/model", "--data", string(body))
	return err
}

// switchTo activates the credential id and returns the credential of the
// same integration that was active before, or "" when id already was. It
// refuses to switch when nothing saved is active there, because there would
// be nothing to restore afterwards.
func (o *Adapter) switchTo(ctx context.Context, id string) (string, error) {
	if !provider.ValidArgument(id) {
		return "", fmt.Errorf("invalid OpenCode connection ID")
	}
	credentials, err := o.credentials(ctx)
	if err != nil {
		return "", err
	}
	var target *savedCredential
	for i := range credentials {
		if credentials[i].ID == id {
			target = &credentials[i]
		}
	}
	if target == nil || target.IntegrationID == "" {
		return "", fmt.Errorf("the chosen OpenCode connection is no longer saved")
	}
	if target.Active {
		return "", nil
	}
	previous := ""
	for _, c := range credentials {
		if c.IntegrationID != target.IntegrationID || !c.Active {
			continue
		}
		if previous != "" {
			return "", fmt.Errorf("OpenCode has more than one active connection for %s", session.Text(target.IntegrationID, 100))
		}
		previous = c.ID
	}
	if previous == "" || !provider.ValidArgument(previous) {
		return "", fmt.Errorf("no saved OpenCode connection is active for %s, so it could not be restored", session.Text(target.IntegrationID, 100))
	}
	if err := o.activate(ctx, id); err != nil {
		return "", err
	}
	return previous, nil
}

func (o *Adapter) activate(ctx context.Context, id string) error {
	_, err := o.run(ctx, "api", "post", "/api/credential/"+url.PathEscape(id)+"/activate")
	return err
}

// waitTurn waits until the session has no active turn, once its turn was seen
// running or the grace for it to start has passed.
func (o *Adapter) waitTurn(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	started, seen := time.Now(), false
	for {
		var active struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := o.request(ctx, "/api/session/active", &active); err != nil {
			return fmt.Errorf("could not tell when the turn ended: %w", err)
		}
		_, running := active.Data[id]
		switch {
		case running:
			seen = true
		case seen || time.Since(started) > turnStartGrace:
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the turn did not end in time: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// modelRef is a model as OpenCode's API takes it.
func modelRef(model, effort string) map[string]string {
	providerID, id, _ := strings.Cut(model, "/")
	ref := map[string]string{"id": id, "providerID": providerID}
	if effort != "" {
		ref["variant"] = effort
	}
	return ref
}
