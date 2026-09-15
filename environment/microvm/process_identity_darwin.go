//go:build darwin

package microvm

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

func platformProcessStartIdentity(ctx context.Context, pid int) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if info.Proc.P_pid != int32(pid) {
		return "", ErrEnvironmentUnavailable
	}
	started := info.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", started.Sec, started.Usec), nil
}
