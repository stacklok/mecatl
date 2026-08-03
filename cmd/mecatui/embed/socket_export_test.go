package embed

import "net"

// NewUnixSocketListenerForTest exposes the production socket-path allocator to
// external-package end-to-end tests without widening the production API.
func NewUnixSocketListenerForTest() (net.Listener, string, string, error) {
	lis, dir, sock, err := newUnixSocketListener()
	return lis, "unix://" + sock, dir, err
}
