//go:build linux

package microvmmanager

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type managedProcessHandle struct {
	fd int
}

func openManagedProcess(pid int) (managedProcessHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return managedProcessHandle{}, err
	}
	return managedProcessHandle{fd: fd}, nil
}

func (h managedProcessHandle) signal() error {
	if err := unix.PidfdSendSignal(h.fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func (h managedProcessHandle) close() error {
	if h.fd < 0 {
		return os.ErrInvalid
	}
	return unix.Close(h.fd)
}
