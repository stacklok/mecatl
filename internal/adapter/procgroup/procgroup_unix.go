//go:build unix

// Package procgroup puts a child process in its own process group so a context
// cancellation kills the WHOLE group — not just the direct child. A shell
// command can spawn grandchildren (e.g. `sh -c "sleep 5"` or a backgrounded
// `make` with compiler children); killing only the shell orphans them, and a
// grandchild holding the stdout/stderr pipes makes (*exec.Cmd).Wait block until
// it exits — defeating the caller's timeout/cancel. Configure(cmd) installs
// Setpgid plus a Cancel that signals the negative PID, delivering SIGKILL to
// every process in the group so the pipes close promptly and Run honours its
// deadline. Shared by the hook runner (hookexec) and the Bash command runner
// (osfs).
package procgroup

import (
	"os/exec"
	"syscall"
)

// Supported reports whether this platform can contain a whole command tree.
func Supported() bool { return true }

// Configure puts cmd in its own process group and, on context cancellation,
// kills the entire group rather than just the leader. Signalling the negative
// PID delivers SIGKILL to every process in the group, so orphaned grandchildren
// die with their parent and any pipes they inherited close promptly.
func Configure(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		// Negative PID → the whole process group created via Setpgid. ESRCH
		// (group already gone) is benign; exec only consults this on the
		// cancellation path.
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
