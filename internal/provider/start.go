package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// Starter is implemented by adapters that can start a new interactive
// session. Listing profiles and catalogues must not start a session or
// change any agent setting.
type Starter interface {
	// StartProfiles lists the launch contexts the agent offers, the
	// default first. A profile that cannot be used is still listed, with
	// its Problem, so the form can say why.
	StartProfiles(ctx context.Context) ([]Profile, error)
	// StartCatalog lists the models and efforts available under a profile.
	StartCatalog(ctx context.Context, profile string) (Catalog, error)
	// Start returns the command that starts a new interactive session. It
	// may prepare the session in the agent first (OpenCode creates it), but
	// never sends a prompt. n has passed CheckStart, so Start does not list
	// profiles or read the catalogue again.
	Start(ctx context.Context, n NewSession) (Command, error)
}

// CheckStart runs every check a start needs before the agent is asked to
// start anything: the folder, the profile (listed now, and usable), the
// model and effort as plain tokens, and, unless both are Default, the
// choice against a fresh catalogue. It is the one place these checks live;
// a refused check changes nothing.
func CheckStart(ctx context.Context, s Starter, n NewSession) error {
	if err := CheckFolder(n.Directory); err != nil {
		return err
	}
	// An error can come back with profiles still listed (OpenCode lists its
	// default even when its service is down), so a listed profile is checked
	// on its own terms. An unlisted one reports the error, if there was one.
	profiles, err := s.StartProfiles(ctx)
	listed := false
	for _, p := range profiles {
		if p.ID != n.Profile {
			continue
		}
		if p.Problem != "" {
			return errors.New(p.Problem)
		}
		listed = true
		break
	}
	if !listed {
		if err != nil {
			return err
		}
		return errors.New("that profile is no longer offered")
	}
	for _, token := range []string{n.Model, n.Effort} {
		if token != "" && !ValidArgument(token) {
			return fmt.Errorf("%q cannot be passed to the agent", token)
		}
	}
	// Default model and effort pass no value to check, so they start even
	// when the catalogue cannot be read (REQ-OPN-008).
	if Defaults(n) {
		return nil
	}
	catalog, err := s.StartCatalog(ctx, n.Profile)
	if err != nil {
		return err
	}
	return CheckChoice(catalog, n.Model, n.Effort)
}

// Profile is a named launch context: a Claude Code home and command, a
// Codex profile, an OpenCode connection.
type Profile struct {
	ID      string // opaque, chosen by the adapter; never read as a path
	Name    string // shown in the form, already made safe
	Detail  string // shown under it: home and command, or connection ID
	Problem string // why it cannot be used now; empty when usable
}

// Catalog is what a profile can run.
type Catalog struct {
	Models []Model
	// DefaultEfforts are the efforts accepted with the agent's default
	// model. Empty when effort needs an explicit model.
	DefaultEfforts []string
	// ModelRequired means the profile cannot start on the agent's default
	// model, so the form offers no "Default" model. An OpenCode connection
	// sets it: OpenCode's own default may belong to another connection.
	ModelRequired bool
}

type Model struct {
	ID            string   // what Start takes, such as "opus" or "anthropic/claude-x"
	Name          string   // shown in the form, already made safe
	Efforts       []string // empty when the model takes no effort setting
	DefaultEffort string   // the agent's own default for this model, if known
}

// NewSession is a session to start. Empty Model or Effort means the
// agent's (or profile's) own default.
type NewSession struct {
	Directory string // absolute; the project's current folder
	Profile   string // a Profile.ID from StartProfiles
	Model     string // a Model.ID from the catalogue, or ""
	Effort    string // one of the model's efforts (or DefaultEfforts), or ""
}

// Defaults reports whether n leaves both the model and the effort to the
// agent. Such a start passes no model or effort, so it needs no catalogue,
// and an unreadable one must not block it (REQ-OPN-008).
func Defaults(n NewSession) bool { return n.Model == "" && n.Effort == "" }

// CheckChoice accepts a model and effort only when the catalogue offers
// them. It never substitutes another value.
func CheckChoice(c Catalog, model, effort string) error {
	if model == "" && c.ModelRequired {
		return fmt.Errorf("choose a model; this profile has no default")
	}
	if model == "" {
		if effort == "" {
			return nil
		}
		if len(c.DefaultEfforts) == 0 {
			return fmt.Errorf("choose a model to set effort")
		}
		if !contains(c.DefaultEfforts, effort) {
			return fmt.Errorf("effort %s is not offered", effort)
		}
		return nil
	}
	for _, m := range c.Models {
		if m.ID != model {
			continue
		}
		if effort == "" {
			return nil
		}
		if len(m.Efforts) == 0 {
			return fmt.Errorf("this model takes no effort setting")
		}
		if !contains(m.Efforts, effort) {
			return fmt.Errorf("effort %s is not offered by model %s", effort, model)
		}
		return nil
	}
	return fmt.Errorf("model %s is not offered", model)
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// argument is a model, effort or profile name that becomes a command
// argument or part of an agent request. Models may contain "/", "." and ":"
// (OpenCode's provider/model), so this is looser than ValidID; nothing that
// starts with "-" is accepted, so it can never be read as an option.
var argument = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)

// ValidArgument reports whether v is safe to pass as a command argument.
func ValidArgument(v string) bool {
	return len(v) <= 200 && argument.MatchString(v)
}
