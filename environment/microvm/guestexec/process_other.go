//go:build !unix

package guestexec

import (
	"os/exec"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd, grace time.Duration) {
	cmd.WaitDelay = grace
}

func configureWorkloadIdentity(_ *exec.Cmd, _ WorkloadIdentity) {}
