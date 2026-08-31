// Package buildinfo exposes the safe, build-time identity shared by shipped binaries.
package buildinfo

import (
	"fmt"
	"io"
)

// BuildID defaults for development builds and is overridden with -ldflags -X.
var BuildID = "dev"

// IsVersion reports whether args request the side-effect-free version action.
func IsVersion(args []string) bool {
	return len(args) == 2 && args[1] == "--version"
}

// PrintVersion writes the consistent version form shared by shipped binaries.
func PrintVersion(w io.Writer, name string) {
	_, _ = fmt.Fprintf(w, "%s %s\n", name, BuildID)
}
