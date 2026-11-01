package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/psicomante/agentflockr/internal/provider"
	"github.com/psicomante/agentflockr/internal/session"
)

// profileSuffix names Codex's own profile files: $CODEX_HOME/NAME.config.toml
// is the profile NAME for `codex --profile NAME`.
const profileSuffix = ".config.toml"

// Profiles are the default plus each profile file. Only file names are read;
// the contents may hold credentials and are never opened here.
func (a *Adapter) StartProfiles(context.Context) ([]provider.Profile, error) {
	home, err := a.startHome()
	if err != nil {
		return nil, err
	}
	var names []string
	entries, readErr := os.ReadDir(home)
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), profileSuffix)
		// Symlinks are skipped: a link could point at a file outside the home.
		// Names that fail the token check would be read as options.
		if !ok || !e.Type().IsRegular() || !provider.ValidArgument(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	problem := startProblem(home, readErr)
	profiles := []provider.Profile{{
		ID: "", Name: "Codex default", Detail: session.Text(home, 200), Problem: problem,
	}}
	for _, name := range names {
		profiles = append(profiles, provider.Profile{
			ID: name, Name: session.Text(name, 64), Detail: session.Text(home, 200), Problem: problem,
		})
	}
	return profiles, nil
}

// startProblem says why no Codex profile can start, or "" when Codex can run
// from this home.
func startProblem(home string, readErr error) string {
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		return "its home folder does not exist: " + session.Text(home, 200)
	}
	if readErr != nil {
		return "cannot read its home folder: " + session.Text(readErr.Error(), 160)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return "cannot find its command codex"
	}
	return ""
}

// StartCatalog lists the models and efforts `codex debug models` reports for
// the home. Codex does not list models per profile, so the catalogue is the
// home's; a profile that pins its own model is used by leaving the model on
// "Default".
func (a *Adapter) StartCatalog(ctx context.Context, profile string) (provider.Catalog, error) {
	if _, err := a.findStartProfile(ctx, profile); err != nil {
		return provider.Catalog{}, err
	}
	return a.catalog(ctx)
}

// Start returns the command for a new session. It sends no prompt. n has
// passed provider.CheckStart, so its profile is a listed profile file and its
// model and effort are offered; `codex debug models` is not run again.
func (a *Adapter) Start(_ context.Context, n provider.NewSession) (provider.Command, error) {
	home, err := a.startHome()
	if err != nil {
		return provider.Command{}, err
	}
	var args []string
	if n.Profile != "" {
		args = append(args, "--profile", n.Profile)
	}
	if n.Model != "" {
		args = append(args, "--model", n.Model)
	}
	if n.Effort != "" {
		// One argument, so the value is read as one TOML string.
		args = append(args, "-c", `model_reasoning_effort="`+n.Effort+`"`)
	}
	args = append(args, "--cd", n.Directory)
	return provider.Command{
		Name:      "codex",
		Args:      args,
		Directory: n.Directory,
		Env:       []string{"CODEX_HOME=" + home},
	}, nil
}

// findStartProfile checks that a profile ID is one the form lists and that it
// can run. The ID is compared, never used as a path.
func (a *Adapter) findStartProfile(ctx context.Context, id string) (provider.Profile, error) {
	profiles, err := a.StartProfiles(ctx)
	if err != nil {
		return provider.Profile{}, err
	}
	for _, p := range profiles {
		if p.ID != id {
			continue
		}
		if p.Problem != "" {
			return provider.Profile{}, errors.New(p.Problem)
		}
		return p, nil
	}
	return provider.Profile{}, fmt.Errorf("that profile is no longer offered")
}

func (a *Adapter) startHome() (string, error) {
	if a.Home == "" {
		return "", fmt.Errorf("the Codex home is not set")
	}
	return filepath.Abs(a.Home)
}

// modelsDocument decodes only what the form needs from `codex debug models`.
type modelsDocument struct {
	Models []struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Visibility  string `json:"visibility"`
		Levels      []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
		DefaultLevel string `json:"default_reasoning_level"`
	} `json:"models"`
}

// catalog reads the models Codex lists for the home. It runs the CLI with
// CODEX_HOME set, so the home's own catalogue is read, not the inherited one.
func (a *Adapter) catalog(ctx context.Context) (provider.Catalog, error) {
	home, err := a.startHome()
	if err != nil {
		return provider.Catalog{}, err
	}
	run := a.RunCommand
	if run == nil {
		run = provider.RunCommand
	}
	out, err := run(ctx, provider.Command{
		Name: "codex",
		Args: []string{"debug", "models"},
		Env:  []string{"CODEX_HOME=" + home},
	})
	if err != nil {
		return provider.Catalog{}, fmt.Errorf("could not read Codex's models: %w", err)
	}
	var doc modelsDocument
	if err := json.Unmarshal(out, &doc); err != nil {
		return provider.Catalog{}, fmt.Errorf("Codex's model list: %w", err)
	}
	var c provider.Catalog
	for _, m := range doc.Models {
		// Codex's own picker shows only the models marked "list"; the others
		// are internal, such as review models.
		if m.Visibility != "list" || !provider.ValidArgument(m.Slug) {
			continue
		}
		model := provider.Model{ID: m.Slug, Name: session.Text(m.DisplayName, 100)}
		if model.Name == "" {
			model.Name = m.Slug
		}
		for _, level := range m.Levels {
			if provider.ValidArgument(level.Effort) {
				model.Efforts = append(model.Efforts, level.Effort)
			}
		}
		if provider.ValidArgument(m.DefaultLevel) {
			model.DefaultEffort = m.DefaultLevel
		}
		c.Models = append(c.Models, model)
	}
	return c, nil
}
