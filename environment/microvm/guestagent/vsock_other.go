//go:build !linux

package guestagent

import (
	"context"
	"errors"
	"io"
)

// DialHostVsock is available only inside the Linux execution image.
func DialHostVsock(context.Context, uint32) (io.ReadWriteCloser, error) {
	return nil, errors.New("microvm guest vsock requires linux")
}
