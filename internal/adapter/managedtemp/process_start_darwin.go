//go:build darwin

package managedtemp

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStartIdentity records Darwin's kernel-reported process start time,
// paired with its PID only after the kernel confirms the same process record.
// It deliberately fails rather than using a PID-only fallback.
func processStartIdentity(pid int) (string, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("managedtemp: read process start identity: %w", err)
	}
	if proc == nil || int(proc.Proc.P_pid) != pid || proc.Proc.P_starttime.Sec <= 0 {
		return "", fmt.Errorf("managedtemp: malformed process start identity")
	}
	return fmt.Sprintf("%d.%06d", proc.Proc.P_starttime.Sec, proc.Proc.P_starttime.Usec), nil
}
