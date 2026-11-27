package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Inspect reads reported metadata and attributes successful file tools from
// one bounded tail read. Shell commands, attempted edits and the working copy
// alone are not proof of a session's changes.
func (a *Adapter) Inspect(ctx context.Context, item session.Session) (provider.Info, error) {
	if !provider.ValidID(item.ID) {
		return provider.Info{}, provider.ErrSessionGone
	}
	path := a.transcript(item.ID)
	if path == "" {
		return provider.Info{}, provider.ErrSessionGone
	}
	data, err := readTail(path, transcriptTail)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return provider.Info{}, provider.ErrSessionGone
		}
		return provider.Info{}, err
	}
	list := messages(data)
	if len(list) > maxMessages {
		list = list[len(list)-maxMessages:]
	}
	info := provider.Info{FilesKnown: true, FilesSource: "Claude Code successful file tools · recent transcript only"}
	// The owning home is known even when the dashboard has no live Service.
	info.Profile = filepath.Dir(filepath.Dir(filepath.Dir(path)))
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if err := ctx.Err(); err != nil {
			return provider.Info{}, err
		}
		var r transcriptRecord
		if json.Unmarshal(line, &r) != nil || r.IsSidechain {
			continue
		}
		if r.Version != "" {
			info.Version = session.Text(r.Version, 40)
		}
		if r.Type == "user" && !r.IsMeta && fromPerson(r) && r.PermissionMode != "" {
			info.Mode = session.Text(r.PermissionMode, 80)
		}
		if r.Type != "assistant" {
			continue
		}
		// Never borrow usage from an earlier model when the latest reply has
		// none (or is malformed). Its model, effort and usage are one report.
		info.Model, info.Effort = session.Text(r.Message.Model, 80), session.Text(r.Effort, 20)
		info.ContextKnown, info.ContextTokens = false, 0
		info.ContextSource = ""
		var usage struct {
			Input *int `json:"input_tokens"`
			Read  *int `json:"cache_read_input_tokens"`
			Write *int `json:"cache_creation_input_tokens"`
		}
		if json.Unmarshal(r.Message.Usage, &usage) == nil {
			info.ContextTokens, info.ContextKnown = provider.ReportedInput(usage.Input, usage.Read, usage.Write)
			if info.ContextKnown {
				info.ContextSource = provider.RecordedInputSource
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range list {
		for _, call := range m.Calls {
			if call.Status != "completed" {
				continue
			}
			for _, path := range call.Files {
				if path != "" && !seen[path] {
					seen[path] = true
					info.Files = append(info.Files, provider.FileChange{Path: path, Status: "edited"})
				}
			}
		}
	}
	return info, ctx.Err()
}
