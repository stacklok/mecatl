//go:build linux

package control

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

type platformPeerAuthenticator struct{}

func (platformPeerAuthenticator) PeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("control connection is %T, not *net.UnixConn", conn)
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access control socket: %w", err)
	}
	var (
		credential    *unix.Ucred
		credentialErr error
	)
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("inspect control peer: %w", err)
	}
	if credentialErr != nil {
		return 0, fmt.Errorf("inspect control peer credentials: %w", credentialErr)
	}
	return credential.Uid, nil
}
