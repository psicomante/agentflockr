package opencode

import (
	"context"
	"errors"
	"net/url"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Inspect reads only the selected session. No account usage endpoint is used:
// OpenCode's experimental usage API describes its Console workspace, not the
// arbitrary connection selected by this session.
func (o *Adapter) Inspect(ctx context.Context, item session.Session) (provider.Info, error) {
	if !provider.ValidID(item.ID) {
		return provider.Info{}, provider.ErrSessionGone
	}
	base := "/api/session/" + url.PathEscape(item.ID)
	var detail struct {
		Data *struct {
			Agent string      `json:"agent"`
			Model nativeModel `json:"model"`
		} `json:"data"`
	}
	if err := o.request(ctx, base, &detail); err != nil {
		return provider.Info{}, err
	}
	if detail.Data == nil {
		return provider.Info{}, errors.New("unrecognized OpenCode session response")
	}
	info := modelInfo(detail.Data.Model, nil)
	info.SelectionRead = true
	info.Mode = session.Text(detail.Data.Agent, agentNameLimit)
	var connections struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if o.request(ctx, "/api/provider", &connections) == nil {
		for _, c := range connections.Data {
			if c.ID == detail.Data.Model.ProviderID && c.Name != "" && c.Name != c.ID {
				info.Connection = c.Name + " (" + c.ID + ")"
			}
		}
	}
	var latest struct {
		Data []struct {
			Model  nativeModel `json:"model"`
			Tokens *struct {
				Input *int `json:"input"`
				Cache struct {
					Read  *int `json:"read"`
					Write *int `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"data"`
	}
	if err := o.request(ctx, base+"/message?limit=1&order=desc&type=assistant", &latest); err != nil {
		info.Problems = append(info.Problems, "Context usage unavailable: "+err.Error())
	} else if len(latest.Data) > 0 && latest.Data[0].Tokens != nil {
		last := latest.Data[0]
		// This is the input of a recorded request, not an estimate of the
		// live conversation. A model switch invalidates the comparison.
		if detail.Data.Model.ID != "" && detail.Data.Model.ProviderID != "" && last.Model.ID == detail.Data.Model.ID && last.Model.ProviderID == detail.Data.Model.ProviderID {
			count, known := provider.ReportedInput(last.Tokens.Input, last.Tokens.Cache.Read, last.Tokens.Cache.Write)
			if known {
				info.ContextTokens, info.ContextKnown = count, true
				info.ContextSource = provider.RecordedInputSource
			}
		}
	}
	var models struct {
		Data []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Limit      struct {
				Context int `json:"context"`
			} `json:"limit"`
		} `json:"data"`
	}
	if err := o.request(ctx, "/api/model", &models); err == nil {
		for _, m := range models.Data {
			if m.ID == detail.Data.Model.ID && m.ProviderID == detail.Data.Model.ProviderID {
				info.ContextWindow = m.Limit.Context
			}
		}
	}
	if info.ContextKnown && info.ContextWindow > 0 {
		info.ContextPercent = float64(info.ContextTokens) * 100 / float64(info.ContextWindow)
	}
	var diff struct {
		Data *[]struct {
			File   string `json:"file"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := o.request(ctx, base+"/diff?context=0", &diff); err != nil {
		info.Problems = append(info.Problems, "Session files unavailable: "+err.Error())
	} else if diff.Data != nil {
		info.FilesKnown = true
		info.FilesSource = "OpenCode snapshots · latest turn (not whole session)"
		for _, file := range *diff.Data {
			info.Files = append(info.Files, provider.FileChange{Path: file.File, Status: file.Status})
		}
	} else {
		info.Problems = append(info.Problems, "Session files unavailable: unrecognized diff response")
	}
	return info, nil
}

// modelInfo is the model, effort and connection of an OpenCode model; names
// maps OpenCode provider IDs to the connection names shown for them.
func modelInfo(m nativeModel, names map[string]string) provider.Info {
	if m.ID == "" || m.ProviderID == "" {
		return provider.Info{}
	}
	connection := names[m.ProviderID]
	if connection == "" {
		connection = m.ProviderID
	}
	return provider.Info{Model: m.ProviderID + "/" + m.ID, Effort: m.Variant, Connection: connection}
}
