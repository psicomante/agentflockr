package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestVersionFallsBackToTheModuleVersion(t *testing.T) {
	built := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/psicomante/agentflockr", Version: v}}
	}
	cases := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags win over the module version", "v0.2.0", built("v0.1.0"), true, "v0.2.0"},
		{"go install reports the module version", unstamped, built("v0.2.0"), true, "v0.2.0"},
		{"an unknown version stays dev", unstamped, built("(devel)"), true, unstamped},
		{"an empty version stays dev", unstamped, built(""), true, unstamped},
		{"no build info stays dev", unstamped, nil, false, unstamped},
	}
	for _, c := range cases {
		if got := resolve(c.stamped, c.info, c.ok); got != c.want {
			t.Errorf("%s: resolve(%q) = %q, want %q", c.name, c.stamped, got, c.want)
		}
	}
}
