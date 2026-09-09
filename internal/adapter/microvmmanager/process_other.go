//go:build !linux

package microvmmanager

import (
	"errors"
	"os"
	"syscall"
)

type managedProcessHandle struct {
	process *os.Process
}

func openManagedProcess(pid int) (managedProcessHandle, error) {
	process, err := os.FindProcess(pid)
	return managedProcessHandle{process: process}, err
}

func (h managedProcessHandle) signal() error {
	if err := h.process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (managedProcessHandle) close() error { return nil }
