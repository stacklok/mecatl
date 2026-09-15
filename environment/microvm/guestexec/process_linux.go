//go:build linux

package guestexec

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd, grace time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	cmd.WaitDelay = grace
}

func configureWorkloadIdentity(cmd *exec.Cmd, identity WorkloadIdentity) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if identity.UID != uint32(os.Getuid()) || identity.GID != uint32(os.Getgid()) { //nolint:gosec // os.Getuid and os.Getgid return non-negative IDs.
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: identity.UID, Gid: identity.GID, Groups: []uint32{}}
	}
	cmd.SysProcAttr.AmbientCaps = nil
}
