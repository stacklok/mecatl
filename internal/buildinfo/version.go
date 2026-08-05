// Package buildinfo owns the one build identity shared by every command root.
package buildinfo

import "strings"

// Version is overridden by release/local build ldflags. Direct `go run` and
// ordinary development/test builds honestly identify themselves as "dev".
var Version = "dev"

// Current returns the normalized build identity used for outbound protocol
// metadata and local display. An accidentally empty ldflags value fails soft to
// the honest development identity.
func Current() string {
	if version := strings.TrimSpace(Version); version != "" {
		return version
	}
	return "dev"
}
