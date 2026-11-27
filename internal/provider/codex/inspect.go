package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// Codex records windows by length, not by name. Unknown lengths are not
// evidence of either account limit. The tail is bounded like Claude's.
const inspectTail = 8 << 20

var limitWindows = map[int]string{300: "five_hour", 10080: "seven_day"}

// Inspect reads the first record and the bounded tail, without running Codex
// or changing the file. The latest token_count owns limits, including nulls.
func (a *Adapter) Inspect(ctx context.Context, item session.Session) (provider.Info, error) {
	if !provider.ValidID(item.ID) || item.Transcript == "" {
		return provider.Info{}, provider.ErrSessionGone
	}
	if _, err := os.Stat(item.Transcript); errors.Is(err, os.ErrNotExist) {
		return provider.Info{}, provider.ErrSessionGone
	}
	if err := a.checkPath(item.Transcript); err != nil {
		return provider.Info{}, err
	}
	f, err := os.Open(item.Transcript)
	if errors.Is(err, os.ErrNotExist) {
		return provider.Info{}, provider.ErrSessionGone
	}
	if err != nil {
		return provider.Info{}, err
	}
	defer f.Close()
	info := provider.Info{}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(a.Home) != filepath.Join(home, ".codex") {
		info.Profile = a.Home
	}
	// A long base-instructions record must not prevent reading newer records.
	scanner := bufio.NewScanner(io.LimitReader(f, provider.MaxRecordSize+1))
	scanner.Buffer(make([]byte, provider.ReadBufferSize), provider.MaxRecordSize)
	if scanner.Scan() {
		var first struct {
			Type    string `json:"type"`
			Payload struct {
				Version string `json:"cli_version"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &first) == nil && first.Type == "session_meta" {
			info.Version = session.Text(first.Payload.Version, 40)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return provider.Info{}, err
	}
	stat, err := f.Stat()
	if err != nil {
		return provider.Info{}, err
	}
	offset := max(int64(0), stat.Size()-inspectTail)
	data, err := io.ReadAll(io.NewSectionReader(f, offset, stat.Size()-offset))
	if err != nil {
		return provider.Info{}, err
	}
	if offset > 0 {
		if n := bytes.IndexByte(data, '\n'); n >= 0 {
			data = data[n+1:]
		} else {
			data = nil
		}
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if err := ctx.Err(); err != nil {
			return provider.Info{}, err
		}
		var r struct {
			Type      string          `json:"type"`
			Sidechain bool            `json:"isSidechain"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil || r.Sidechain {
			continue
		}
		switch r.Type {
		case "turn_context":
			var turn struct {
				Model    string `json:"model"`
				Effort   string `json:"effort"`
				Approval string `json:"approval_policy"`
				Sandbox  struct {
					Type string `json:"type"`
				} `json:"sandbox_policy"`
			}
			if json.Unmarshal(r.Payload, &turn) != nil {
				continue
			}
			info.Model, info.Effort = session.Text(turn.Model, 80), session.Text(turn.Effort, 20)
			info.Mode = session.Text(strings.Join(nonempty(turn.Approval, turn.Sandbox.Type), " · "), 80)
			// A token count before this turn cannot describe its selected model.
			info.ContextKnown, info.ContextTokens, info.ContextWindow, info.ContextPercent = false, 0, 0, 0
			info.ContextSource = ""
		case "event_msg":
			var event struct {
				Type   string          `json:"type"`
				Info   json.RawMessage `json:"info"`
				Limits json.RawMessage `json:"rate_limits"`
			}
			if json.Unmarshal(r.Payload, &event) != nil || event.Type != "token_count" {
				continue
			}
			info.ContextKnown, info.ContextTokens, info.ContextWindow, info.ContextPercent = false, 0, 0, 0
			info.ContextSource = ""
			var usage struct {
				Last struct {
					Input *int `json:"input_tokens"`
				} `json:"last_token_usage"`
				Window int `json:"model_context_window"`
			}
			if json.Unmarshal(event.Info, &usage) == nil && info.Model != "" {
				info.ContextTokens, info.ContextKnown = provider.ReportedInput(usage.Last.Input)
				if usage.Window > 0 {
					info.ContextWindow = usage.Window
				}
				if info.ContextKnown {
					info.ContextSource = provider.RecordedInputSource
					if info.ContextWindow > 0 {
						info.ContextPercent = float64(info.ContextTokens) * 100 / float64(info.ContextWindow)
					}
				}
			}
			info.Limits = nil
			info.LimitsSource = "Codex session file · latest token count"
			var limits map[string]json.RawMessage
			if json.Unmarshal(event.Limits, &limits) != nil {
				continue
			}
			for _, key := range []string{"primary", "secondary"} {
				var l struct {
					Percent *float64 `json:"used_percent"`
					Minutes int      `json:"window_minutes"`
					Reset   int64    `json:"resets_at"`
				}
				if json.Unmarshal(limits[key], &l) != nil || l.Percent == nil || *l.Percent < 0 || math.IsInf(*l.Percent, 0) || math.IsNaN(*l.Percent) {
					continue
				}
				kind, ok := limitWindows[l.Minutes]
				if !ok {
					continue
				}
				limit := provider.Limit{Kind: kind, PercentUsed: *l.Percent}
				if l.Reset > 0 {
					limit.ResetsAt = time.Unix(l.Reset, 0)
				}
				info.Limits = append(info.Limits, limit)
			}
		}
	}
	return info, ctx.Err()
}

func nonempty(values ...string) []string {
	var out []string
	for _, s := range values {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
