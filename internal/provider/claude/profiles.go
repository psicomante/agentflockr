package claude

import (
	"os"
	"path/filepath"
)

// Profiles returns the Claude Code homes kept beside the default one, such as
// ~/.claude-personal: every ~/.claude-* folder that has a projects folder.
// Other folders with that prefix are not homes and are left out.
func Profiles(home string) []string {
	// The pattern is constant, so Glob cannot fail on it.
	candidates, _ := filepath.Glob(filepath.Join(home, ".claude-*"))
	var homes []string
	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(candidate, "projects")); err == nil && info.IsDir() {
			homes = append(homes, candidate)
		}
	}
	return homes
}
