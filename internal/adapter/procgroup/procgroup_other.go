//go:build !unix

package procgroup

import "os/exec"

// Configure is a no-op on platforms without POSIX process groups. There the
// timeout is enforced by exec's default process kill plus a Cmd.WaitDelay
// backstop set by the caller.
func Configure(_ *exec.Cmd) {}
