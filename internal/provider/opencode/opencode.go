// Package opencode integrates OpenCode V2 sessions through the authenticated
// `opencode api` CLI, which handles service discovery and authentication.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

type Adapter struct{ Run provider.Runner }

func New() *Adapter { return &Adapter{} }

func (o *Adapter) ID() session.Provider { return session.OpenCode }

func (o *Adapter) Meta() provider.Meta {
	return provider.Meta{Name: "OpenCode", Title: "OpenCode", Short: "OC", Source: "OpenCode V2: authenticated CLI/API",
		DeleteScope: "Deletes this conversation and its child sessions."}
}

// Resume moves the session to its project's new folder first when the
// project was moved, since OpenCode resumes in the directory it recorded.
func (o *Adapter) Resume(ctx context.Context, s session.Session) (provider.Command, error) {
	if err := provider.CheckResumable(s); err != nil {
		return provider.Command{}, err
	}
	if session.Moved(s) {
		if err := o.move(ctx, s); err != nil {
			return provider.Command{}, err
		}
	}
	return provider.Command{Name: "opencode", Args: []string{"--session", s.ID, s.Directory}, Directory: s.Directory}, nil
}

func (o *Adapter) move(ctx context.Context, s session.Session) error {
	body, err := json.Marshal(map[string]string{"directory": s.Directory, "delivery": "queue"})
	if err != nil {
		return err
	}
	if _, err := o.run(ctx, "api", "post", "/api/session/"+url.PathEscape(s.ID)+"/move", "--data", string(body)); err != nil {
		return err
	}
	// Moves may be queued behind an active turn. Wait for OpenCode to confirm
	// the new directory before handing the terminal over.
	for {
		data, err := o.run(ctx, "api", "get", "/api/session/"+url.PathEscape(s.ID))
		if err != nil {
			return fmt.Errorf("move requested; could not verify its completion: %w", err)
		}
		var response struct {
			Data struct {
				Location struct {
					Directory string `json:"directory"`
				} `json:"location"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return fmt.Errorf("verify moved session: %w", err)
		}
		if session.SameDirectory(response.Data.Location.Directory, s.Directory) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("move requested but still pending; finish the active turn and try opening again: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Delete refuses a session with a running turn, then uses the native API,
// which also deletes child sessions.
func (o *Adapter) Delete(ctx context.Context, s session.Session) error {
	if err := provider.CheckDeletable(ctx, s); err != nil {
		return err
	}
	if !strings.HasPrefix(s.ID, "ses") {
		return fmt.Errorf("invalid OpenCode session ID")
	}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := o.request(ctx, "/api/session/active", &active); err != nil || active.Data == nil {
		return fmt.Errorf("could not determine active OpenCode sessions")
	}
	if _, ok := active.Data[s.ID]; ok {
		return fmt.Errorf("finish or stop this OpenCode turn before deleting it")
	}
	_, err := o.run(ctx, "api", "delete", "/api/session/"+url.PathEscape(s.ID))
	return err
}

func (o *Adapter) MatchesProcess(args []string, s session.Session) bool {
	args, ok := provider.ToolArgs(args, session.OpenCode)
	if !ok {
		return false
	}
	value, ok := provider.FlagValue(args, "--session", "-s", "--resume", "-r")
	return ok && value == s.ID
}

// Preview loads bounded public assistant text only for the selected session.
func (o *Adapter) Preview(ctx context.Context, id string) (string, error) {
	var response struct {
		Data []struct {
			Content []provider.Block `json:"content"`
		} `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(id) + "/message?limit=5&order=desc&type=assistant"
	if err := o.request(ctx, path, &response); err != nil {
		return "", err
	}
	for _, message := range response.Data {
		if text := provider.BlockText(message.Content); text != "" {
			return session.Text(text, 600), nil
		}
	}
	return "", nil
}

func (o *Adapter) run(ctx context.Context, args ...string) ([]byte, error) {
	if o.Run != nil {
		return o.Run(ctx, "opencode", args...)
	}
	return provider.Run(ctx, "opencode", args...)
}

func (o *Adapter) request(ctx context.Context, path string, target any) error {
	data, err := o.run(ctx, "api", "get", path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("OpenCode V2 response: %w", err)
	}
	return nil
}

// maxPages caps how many pages of 100 sessions one scan follows, so an
// endless listing cannot keep a scan running. It allows 100,000 sessions.
const maxPages = 1000

func (o *Adapter) Scan(ctx context.Context, now time.Time) provider.Result {
	result := provider.Result{Provider: session.OpenCode}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	activeErr := o.request(ctx, "/api/session/active", &active)
	if activeErr == nil && active.Data == nil {
		activeErr = fmt.Errorf("unexpected active-session response; OpenCode V2 is required")
	}
	if activeErr != nil {
		result.Warnings = append(result.Warnings, "Live status: "+activeErr.Error())
	}
	cursor := ""
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			result.Warnings = append(result.Warnings, fmt.Sprintf("OpenCode listing stopped after %d pages; older sessions are not shown", maxPages))
			break
		}
		q := url.Values{"limit": {"100"}, "order": {"desc"}, "parentID": {"null"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Data []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Outcome  string `json:"outcome"`
				Location struct {
					Directory string `json:"directory"`
				} `json:"location"`
				Time struct {
					Updated int64 `json:"updated"`
				} `json:"time"`
			} `json:"data"`
			Cursor struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		if err := o.request(ctx, "/api/session?"+q.Encode(), &page); err != nil {
			result.Err = err
			return result
		}
		if page.Data == nil {
			result.Err = fmt.Errorf("unexpected session-list response; OpenCode V2 is required")
			return result
		}
		for _, item := range page.Data {
			s := session.Session{ID: item.ID, Provider: session.OpenCode, Directory: item.Location.Directory, OriginalDirectory: item.Location.Directory, Title: session.Text(item.Title, 100), Summary: session.Text(item.Title, 600), UpdatedAt: time.UnixMilli(item.Time.Updated), Status: session.Idle, Evidence: "OpenCode API: no active turn"}
			if activeErr != nil {
				provider.SetState(&s, session.Unknown, "OpenCode live status unavailable")
			} else if _, ok := active.Data[item.ID]; ok {
				provider.SetState(&s, session.Working, "OpenCode API: running turn")
				for _, endpoint := range []string{"permission", "form"} {
					var pending struct {
						Data []json.RawMessage `json:"data"`
					}
					if err := o.request(ctx, "/api/session/"+url.PathEscape(item.ID)+"/"+endpoint, &pending); err != nil {
						result.Warnings = append(result.Warnings, "Pending "+endpoint+": "+err.Error())
						continue
					}
					if len(pending.Data) > 0 {
						provider.SetState(&s, session.Waiting, "OpenCode API: pending "+endpoint)
					}
				}
			} else {
				switch item.Outcome {
				case "succeeded":
					provider.SetState(&s, session.Completed, "OpenCode API: last turn succeeded")
				case "failed":
					provider.SetState(&s, session.Failed, "OpenCode API: last turn failed")
				case "interrupted":
					provider.SetState(&s, session.Idle, "OpenCode API: last turn interrupted")
				}
			}
			result.Sessions = append(result.Sessions, s)
		}
		if len(page.Data) == 0 || page.Cursor.Next == nil || *page.Cursor.Next == "" {
			break
		}
		cursor = *page.Cursor.Next
		if seen[cursor] {
			result.Err = fmt.Errorf("OpenCode repeated a pagination cursor")
			return result
		}
		seen[cursor] = true
	}
	return result
}
