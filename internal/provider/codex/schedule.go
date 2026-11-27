package codex

import (
	"context"
	"fmt"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// promptTimeout bounds one scheduled run of `codex exec`. A short prompt
// answers in seconds; this leaves room for a slow start.
const promptTimeout = 15 * time.Minute

// PromptCatalog lists the models `codex debug models` reports for the home.
func (a *Adapter) PromptCatalog(ctx context.Context, _ session.Session) (provider.Catalog, error) {
	return a.catalog(ctx)
}

// Prompt resumes the session non-interactively with `codex exec resume`, in
// its folder and home, and waits for the turn to end. The output is read and
// dropped: the rollout file records the turn.
func (a *Adapter) Prompt(ctx context.Context, s session.Session, p provider.ScheduledPrompt) error {
	text, choice, err := a.checkPrompt(ctx, p)
	if err != nil {
		return err
	}
	if err := provider.CheckResumable(s); err != nil {
		return err
	}
	if !provider.ValidArgument(s.ID) {
		return fmt.Errorf("invalid Codex session ID")
	}
	home, err := a.startHome()
	if err != nil {
		return err
	}
	// The session's folder need not be a Git repository; Codex refuses to
	// run outside one without --skip-git-repo-check. CleanPrompt has refused
	// a prompt that reads as an option.
	args := append([]string{"exec", "resume", "--skip-git-repo-check"}, choice...)
	args = append(args, s.ID, text)
	return a.runPrompt(ctx, provider.Command{Name: "codex", Args: args, Directory: s.Directory, Env: []string{"CODEX_HOME=" + home}})
}

// PromptProfile runs `codex exec` with the profile in dir, outside any
// session. --ephemeral keeps Codex from saving the run as a session.
func (a *Adapter) PromptProfile(ctx context.Context, profile, dir string, p provider.ScheduledPrompt) error {
	if _, err := a.findStartProfile(ctx, profile); err != nil {
		return err
	}
	text, choice, err := a.checkPrompt(ctx, p)
	if err != nil {
		return err
	}
	home, err := a.startHome()
	if err != nil {
		return err
	}
	args := []string{"exec"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	args = append(append(args, choice...), "--ephemeral", "--skip-git-repo-check", "--cd", dir, text)
	return a.runPrompt(ctx, provider.Command{Name: "codex", Args: args, Directory: dir, Env: []string{"CODEX_HOME=" + home}})
}

// checkPrompt cleans the text and turns the model and effort into Codex's
// options. The choice is checked against the catalogue only when one is
// made, so a run on the defaults does not run `codex debug models`.
func (a *Adapter) checkPrompt(ctx context.Context, p provider.ScheduledPrompt) (string, []string, error) {
	text, err := provider.CleanPrompt(p.Text)
	if err != nil {
		return "", nil, err
	}
	var choice []string
	if p.Model == "" && p.Effort == "" {
		return text, nil, nil
	}
	catalog, err := a.catalog(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("could not check the model: %w", err)
	}
	if err := provider.CheckChoice(catalog, p.Model, p.Effort); err != nil {
		return "", nil, fmt.Errorf("%w: %v", provider.ErrInvalid, err)
	}
	if p.Model != "" {
		choice = append(choice, "--model", p.Model)
	}
	if p.Effort != "" {
		// One argument, so the value is read as one TOML string; the
		// catalogue's efforts hold no quotes.
		choice = append(choice, "-c", `model_reasoning_effort="`+p.Effort+`"`)
	}
	return text, choice, nil
}

func (a *Adapter) runPrompt(ctx context.Context, cmd provider.Command) error {
	ctx, cancel := context.WithTimeout(ctx, promptTimeout)
	defer cancel()
	run := a.RunCommand
	if run == nil {
		run = provider.RunCommand
	}
	if _, err := run(ctx, cmd); err != nil {
		return fmt.Errorf("Codex: %w", err)
	}
	return nil
}
