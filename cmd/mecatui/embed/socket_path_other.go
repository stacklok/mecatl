//go:build !darwin

package embed

// Non-Darwin platforms retain the existing behavior and let net.Listen apply
// their platform-specific UNIX socket constraints.
const unixSocketPathLimit = 0

const darwinShortSocketBase = ""

func unixSocketPathFits(string) bool { return true }
