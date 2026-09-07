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
	"time"
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
		return Kill(c.Process.Pid)
	}
}

// Kill terminates the managed process group. A vanished group is already done.
func Kill(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// WaitGone gives a cancellation kill a bounded chance to finish reaping the
// complete managed group before its lease is considered for immediate deletion.
func WaitGone(pid int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for GroupAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	return !GroupAlive(pid)
}
