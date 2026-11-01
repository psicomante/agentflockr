package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

var uuid = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

// Delete uses `codex delete` with an exact UUID, then confirms the transcript
// is gone.
func (a *Adapter) Delete(ctx context.Context, s session.Session) error {
	if err := provider.CheckDeletable(ctx, s); err != nil {
		return err
	}
	if !uuid.MatchString(s.ID) {
		return fmt.Errorf("Codex deletion requires an exact session UUID")
	}
	if s.Transcript != "" {
		if err := a.checkPath(s.Transcript); err != nil {
			return err
		}
	}
	if _, err := a.run(ctx, "codex", "delete", "--force", s.ID); err != nil {
		return err
	}
	if s.Transcript != "" {
		if _, err := os.Lstat(s.Transcript); err == nil {
			return fmt.Errorf("Codex reported deletion but its transcript still exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// run pins CODEX_HOME so the native CLI deletes from the configured home, not
// one inherited from the environment.
func (a *Adapter) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.Run != nil {
		return a.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if a.Home != "" {
		home, err := filepath.Abs(a.Home)
		if err != nil {
			return nil, err
		}
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CODEX_HOME=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	}
	combined := provider.NewLimitedBuffer(provider.MaxOutputSize, cancel)
	cmd.Stdout, cmd.Stderr = combined, combined
	err := cmd.Run()
	out := combined.Bytes()
	if combined.Exceeded() {
		return nil, fmt.Errorf("%s deletion: output exceeds %d bytes", name, provider.MaxOutputSize)
	}
	if err != nil {
		return out, fmt.Errorf("%s deletion: %w %s", name, err, session.Text(string(out), 400))
	}
	return out, nil
}

func (a *Adapter) checkPath(path string) error {
	if a.Home == "" {
		return fmt.Errorf("Codex home is not configured")
	}
	home, err := filepath.Abs(a.Home)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// A cached session from a different configured home must not delete the
	// same UUID in the currently configured provider's storage.
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real
	}
	rel, err := filepath.Rel(home, target)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) < 2 || (parts[0] != "sessions" && parts[0] != "archived_sessions") || filepath.Ext(target) != ".jsonl" {
		return fmt.Errorf("transcript is outside the configured Codex session directories")
	}
	return nil
}
