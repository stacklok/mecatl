//go:build !unix

package procgroup

import (
	"os/exec"
	"time"
)

// Supported reports whether this platform can contain a whole command tree.
func Supported() bool { return false }

// Configure is a no-op on platforms without POSIX process groups. There the
// timeout is enforced by exec's default process kill plus a Cmd.WaitDelay
// backstop set by the caller.
func Configure(_ *exec.Cmd) {}

// Kill has no process-group implementation on this platform.
func Kill(_ int) error { return nil }

// GroupAlive is unavailable where process groups are unsupported.
func GroupAlive(_ int) bool { return false }

// WaitGone is immediate where process groups are unsupported.
func WaitGone(_ int, _ time.Duration) bool { return true }
