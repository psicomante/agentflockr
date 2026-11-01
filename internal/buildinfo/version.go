// Package buildinfo identifies the running binary, independently of its location.
package buildinfo

import "runtime/debug"

// unstamped is what a build reports when nothing says which version it is.
const unstamped = "dev"

// Version is set at build time by the Makefile and GoReleaser through
// -ldflags. When they didn't set it, init falls back to the module version Go
// recorded, so `go install …@v0.2.0` reports v0.2.0.
var Version = unstamped

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolve(Version, info, ok)
}

// resolve keeps a version set by -ldflags, and otherwise uses the main
// module's recorded version. Go records "(devel)" when it knows no version,
// such as a build from a folder without version control.
func resolve(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != unstamped || !ok || info == nil {
		return stamped
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return stamped
}
