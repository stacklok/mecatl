//go:build linux

package guestagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// DialHostVsock connects the guest agent to the host-bound go-microvm vsock port.
func DialHostVsock(ctx context.Context, port uint32) (io.ReadWriteCloser, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open guest vsock: %w", err)
	}
	closeFD := true
	defer func() {
		if closeFD {
			_ = unix.Close(fd)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
		return nil, fmt.Errorf("connect guest vsock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "mecatl-guest-vsock")
	if file == nil {
		return nil, errors.New("wrap guest vsock file descriptor")
	}
	closeFD = false
	return file, nil
}
