// Package buildinfo exposes the safe, build-time identity shared by shipped binaries.
package buildinfo

import (
	"fmt"
	"io"
	"runtime/debug"
)

// BuildID is an optional linker stamp. Taskfile builds stamp it from rooted git
// describe output. Direct unstamped Go/ko builds derive a fallback from embedded VCS
// metadata during initialization.
var BuildID string

func init() {
	var settings []debug.BuildSetting
	if info, ok := debug.ReadBuildInfo(); ok {
		settings = info.Settings
	}
	BuildID = resolveBuildID(BuildID, settings)
}

// resolveBuildID preserves an explicit nonempty linker stamp, including "dev".
// For an unstamped source build, it derives a reproducible identifier from Go
// build metadata.
func resolveBuildID(buildID string, settings []debug.BuildSetting) string {
	if buildID != "" {
		return buildID
	}

	var revision, modified string
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if !validRevision(revision) {
		return "dev"
	}

	buildID = "dev+" + revision[:12]
	if modified == "true" {
		buildID += ".dirty"
	}
	return buildID
}

func validRevision(revision string) bool {
	if len(revision) < 12 {
		return false
	}
	for _, r := range revision {
		if ('0' > r || r > '9') && ('a' > r || r > 'f') && ('A' > r || r > 'F') {
			return false
		}
	}
	return true
}

// IsVersion reports whether args request the side-effect-free version action.
func IsVersion(args []string) bool {
	return len(args) == 2 && args[1] == "--version"
}

// PrintVersion writes the consistent version form shared by shipped binaries.
func PrintVersion(w io.Writer, name string) {
	_, _ = fmt.Fprintf(w, "%s %s\n", name, BuildID)
}
