//go:build !unix

package main

import "net"

// listenUnixOwnerOnly binds a UNIX socket at path.
//
// Non-unix platforms have no umask, so the bind-time narrowing that closes the
// permission window on unix is unavailable here. enforceOwnerOnlySocket still
// runs afterwards and narrows the mode if the platform reports one, but the
// window it leaves is real and is why a spawned daemon's socket belongs in an
// owner-only directory on every platform (see ensureSocketDir). Local spawn on
// Windows is an explicit non-goal of issue #821.
func listenUnixOwnerOnly(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}
