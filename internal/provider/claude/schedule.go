package claude

import (
	"context"
	"fmt"
	"time"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// promptTimeout bounds one scheduled run of Claude Code in print mode. A
// short prompt answers in seconds; this leaves room for a slow start.
const promptTimeout = 15 * time.Minute

// PromptCatalog lists the built-in models and efforts: Claude Code takes the
// same ones whichever home a session is in.
func (a *Adapter) PromptCatalog(context.Context, session.Session) (provider.Catalog, error) {
	return provider.Catalog{Models: startModels, DefaultEfforts: startEfforts}, nil
}

// Prompt runs the session's own resume command, with its profile's home and
// command, in print mode with the scheduled prompt, and waits for the turn to
// end. Its output is read and dropped: the transcript records the turn.
func (a *Adapter) Prompt(ctx context.Context, s session.Session, p provider.ScheduledPrompt) error {
	text, choice, err := checkPrompt(p)
	if err != nil {
		return err
	}
	cmd, err := a.Resume(ctx, s)
	if err != nil {
		return err
	}
	// Resume ends with "--resume REF", so the prompt follows as print mode's
	// positional argument; CleanPrompt has refused one that reads as a flag.
	cmd.Args = append(append(cmd.Args, choice...), "--print", text)
	return runPrompt(ctx, cmd)
}

// PromptProfile runs the profile's command, with its home, in print mode in
// dir, outside any session. --no-session-persistence keeps Claude Code from
// saving the conversation, so the runs never pile up as sessions in dir.
func (a *Adapter) PromptProfile(ctx context.Context, profile, dir string, p provider.ScheduledPrompt) error {
	text, choice, err := checkPrompt(p)
	if err != nil {
		return err
	}
	l, ok, err := a.findLaunch(ctx, profile)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("that profile is no longer offered")
	}
	if why := problem(l); why != "" {
		return fmt.Errorf("the profile cannot run: %s", why)
	}
	command := l.commandOrDefault()
	args := append(append([]string(nil), command[1:]...), choice...)
	args = append(args, "--no-session-persistence", "--print", text)
	cmd := provider.Command{Name: command[0], Args: args, Directory: dir}
	a.configDir(&cmd, l.home)
	return runPrompt(ctx, cmd)
}

// checkPrompt cleans the text and turns the model and effort into Claude
// Code's options, refusing a choice the catalogue does not offer: the values
// come from the index, which is read back like any other stored data.
func checkPrompt(p provider.ScheduledPrompt) (string, []string, error) {
	text, err := provider.CleanPrompt(p.Text)
	if err != nil {
		return "", nil, err
	}
	if err := provider.CheckChoice(provider.Catalog{Models: startModels, DefaultEfforts: startEfforts}, p.Model, p.Effort); err != nil {
		return "", nil, fmt.Errorf("%w: %v", provider.ErrInvalid, err)
	}
	var choice []string
	if p.Model != "" {
		choice = append(choice, "--model", p.Model)
	}
	if p.Effort != "" {
		choice = append(choice, "--effort", p.Effort)
	}
	return text, choice, nil
}

func runPrompt(ctx context.Context, cmd provider.Command) error {
	ctx, cancel := context.WithTimeout(ctx, promptTimeout)
	defer cancel()
	if _, err := provider.RunCommand(ctx, cmd); err != nil {
		return fmt.Errorf("Claude Code: %w", err)
	}
	return nil
}
