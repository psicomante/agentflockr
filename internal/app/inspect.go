package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// InspectionTimeout bounds all on-demand metadata reads, independently of a scan.
const InspectionTimeout = 10 * time.Second

// Inspect combines agent evidence with the current local worktree. Git changes
// are kept separate: another session or a person may have made them.
func (e *Engine) Inspect(ctx context.Context, item session.Session) provider.Info {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	var info provider.Info
	if e.Demo {
		info = demoInfo(time.Now())
		info.Folder = item.Directory
		return info // Demo never inspects the real filesystem or agents.
	}
	adapter := e.Providers.Get(item.Provider)
	if reporter, ok := adapter.(provider.Informer); ok {
		info, _ = reporter.Info(item)
	}
	if reader, ok := adapter.(provider.Inspector); ok {
		reported, err := reader.Inspect(ctx, item)
		if err != nil {
			info.Problems = append(info.Problems, session.Text(err.Error(), 300))
		} else {
			// A connected mod has richer live fields; on-demand reports only
			// replace the fields they own.
			if reported.SelectionRead {
				info.Mode, info.Model, info.Effort, info.Connection = reported.Mode, reported.Model, reported.Effort, reported.Connection
			} else {
				if info.Mode == "" {
					info.Mode = reported.Mode
				}
				if info.Model == "" {
					info.Model = reported.Model
				}
				if info.Effort == "" {
					info.Effort = reported.Effort
				}
			}
			if info.Version == "" {
				info.Version = reported.Version
			}
			if info.Profile == "" {
				info.Profile = reported.Profile
			}
			if len(info.Limits) == 0 {
				info.Limits, info.LimitsSource = reported.Limits, reported.LimitsSource
			}
			info.Files, info.FilesKnown, info.FilesSource = reported.Files, reported.FilesKnown, reported.FilesSource
			if reported.SelectionRead {
				info.ContextTokens, info.ContextWindow, info.ContextPercent = reported.ContextTokens, reported.ContextWindow, reported.ContextPercent
				info.ContextKnown, info.ContextSource = reported.ContextKnown, reported.ContextSource
			} else {
				// A model switch in the mod invalidates the transcript's usage.
				if !info.ContextKnown && info.ContextTokens == 0 && (info.Model == "" || info.Model == reported.Model) {
					info.ContextTokens, info.ContextKnown, info.ContextSource = reported.ContextTokens, reported.ContextKnown, reported.ContextSource
				}
				if info.ContextWindow == 0 {
					info.ContextWindow = reported.ContextWindow
				}
				if info.ContextKnown && info.ContextWindow > 0 {
					info.ContextPercent = float64(info.ContextTokens) * 100 / float64(info.ContextWindow)
				}
			}
			info.Problems = append(info.Problems, reported.Problems...)
		}
	}
	info.Folder = item.Directory
	info.Repo, info.Branch, info.Worktree = "", "", ""
	// Git facts describe the folder now, not a historical branch guessed from
	// the session's age. Never run repository hooks or a configured fsmonitor.
	gitInfo(ctx, item.Directory, &info, provider.Run)
	if len(info.Files) > provider.MaxInfoFiles {
		info.Files = info.Files[:provider.MaxInfoFiles]
		info.Problems = append(info.Problems, "Agent file list shortened; more files were reported")
	}
	if len(info.GitFiles) > provider.MaxInfoFiles {
		info.GitFiles = info.GitFiles[:provider.MaxInfoFiles]
		info.Problems = append(info.Problems, "Worktree file list shortened; more changes exist")
	}
	return info
}

func gitInfo(ctx context.Context, folder string, info *provider.Info, run provider.Runner) {
	if folder == "" || !filepath.IsAbs(folder) {
		return
	}
	git := gitIn(ctx, run, folder)
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		info.Problems = append(info.Problems, "Git information unavailable: "+session.Text(err.Error(), 300))
		return
	}
	root = strings.TrimSuffix(root, "\n")
	info.Worktree = root
	common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil {
		common = strings.TrimSuffix(common, "\n")
		if filepath.Base(common) == ".git" {
			info.Repo = filepath.Dir(common)
		}
	}
	branch, err := git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		info.Branch = strings.TrimSuffix(branch, "\n")
	} else {
		if commit, err := git("rev-parse", "--short", "HEAD"); err == nil {
			info.Branch = "Detached HEAD · " + strings.TrimSpace(commit)
		}
	}
	status, err := git("status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		info.Problems = append(info.Problems, "Worktree changes unavailable: "+session.Text(err.Error(), 300))
		return
	}
	info.GitKnown = true
	info.GitFiles = parseGitStatus(status)
}

// gitIn runs read-only Git commands in folder. They never take optional
// locks, run repository hooks or start a configured fsmonitor.
func gitIn(ctx context.Context, run provider.Runner, folder string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		base := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", folder}
		out, err := run(ctx, "git", append(base, args...)...)
		return string(out), err
	}
}

func parseGitStatus(raw string) []provider.FileChange {
	var files []provider.FileChange
	for _, e := range gitEntries(raw) {
		files = append(files, provider.FileChange{Path: e.path, Status: e.status})
	}
	return files
}

// gitEntry is one line of `git status --porcelain=v1 -z`.
type gitEntry struct {
	path, from, status string // from is a rename's or copy's source
}

func gitEntries(raw string) []gitEntry {
	var entries []gitEntry
	parts := strings.Split(raw, "\x00")
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if len(part) < 4 {
			continue
		}
		e := gitEntry{path: part[3:], status: strings.TrimSpace(part[:2])}
		// With -z, a rename/copy's destination comes first, then its source.
		if strings.ContainsAny(part[:2], "RC") && i+1 < len(parts) {
			i++
			e.from = parts[i]
		}
		entries = append(entries, e)
	}
	return entries
}
