//go:build unix

package main

import (
	"net"
	"sync"
	"syscall"
)

// umaskMu serializes the process-global umask change around a socket bind.
//
// umask is per-PROCESS state, so temporarily narrowing it is only safe because
// of where this runs: once, during startup, before any listener serves and
// before any tool can create a file. The mutex closes the one remaining hazard —
// two concurrent binds interleaving their save/restore and leaving the umask
// permanently narrowed.
var umaskMu sync.Mutex

// listenUnixOwnerOnly binds a UNIX socket at path with an owner-only mode, set
// AT CREATION rather than by a chmod afterwards.
//
// This is the mechanism behind AC8.7. A bind followed by a chmod leaves a real
// window — short, but a window in which any process the directory admits can
// connect and, on a socket carrying an unauthenticated local harness API, that
// is command execution. Narrowing the umask across the bind removes the window
// instead of shrinking it: the kernel applies 0777 &^ umask when it creates the
// socket inode, so the file is never group- or world-reachable at any instant.
func listenUnixOwnerOnly(path string) (net.Listener, error) {
	umaskMu.Lock()
	prev := syscall.Umask(0o077)
	lis, err := net.Listen("unix", path)
	syscall.Umask(prev)
	umaskMu.Unlock()
	return lis, err
}
