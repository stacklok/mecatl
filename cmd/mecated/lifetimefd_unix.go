//go:build unix

package main

import (
	"fmt"
	"syscall"
)

// checkLifetimePipeFD validates --lifetime-pipe-fd WITHOUT taking ownership of
// the descriptor.
//
// Not taking ownership is the whole point, and it is subtler than it looks.
// os.NewFile attaches a cleanup that CLOSES the descriptor when the wrapper is
// garbage collected, so validating through an os.File and then dropping it on
// the error path hands the runtime a licence to close the caller's fd at an
// arbitrary later moment — by which time that number may have been reused by an
// unrelated socket. The symptom is not a failure here at all; it is some other
// component's connection dying much later, for no visible reason. fstat asks the
// kernel the same question and owns nothing.
//
// The conditions are the ones the flag's contract needs. An fd that is not open
// is a startup error rather than an immediate EOF, because watch() would read a
// bad descriptor as "the parent died" and the daemon would publish its ready
// file and vanish milliseconds later. The descriptor must also be either a FIFO
// read end or a connected UNIX-domain stream socketpair endpoint: both report
// EOF when the parent-held peer closes. This runs after bindListeners, so a stale
// or mistyped number can name a listener the daemon already owns; accepting that
// listener would turn ENOTCONN into a false parent-exit signal.
func checkLifetimePipeFD(fd int) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("--lifetime-pipe-fd %d is not an open descriptor in this process (%w): the parent must pass a pipe's READ end or a connected UNIX-domain stream socketpair endpoint as an inherited fd", fd, err)
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFIFO:
		return nil
	case syscall.S_IFSOCK:
		return checkLifetimeSocketpairFD(fd)
	default:
		return fmt.Errorf("--lifetime-pipe-fd %d is open but is a %s, not a pipe or connected UNIX-domain stream socketpair endpoint: a regular file, terminal, or descriptor this process already owns is a mistake, not a parent-liveness signal", fd, fdTypeName(st))
	}
}

// checkLifetimeSocketpairFD distinguishes the connected AF_UNIX stream endpoint
// Node and Bun create for child_process stdio:"pipe" from listening, network,
// and datagram sockets, none of which implement the EOF-on-parent-death contract.
func checkLifetimeSocketpairFD(fd int) error {
	socketType, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil {
		return fmt.Errorf("--lifetime-pipe-fd %d is a socket whose type cannot be inspected (%w)", fd, err)
	}
	if socketType != syscall.SOCK_STREAM {
		return fmt.Errorf("--lifetime-pipe-fd %d is a socket of type %d, not a connected UNIX-domain stream socketpair endpoint", fd, socketType)
	}
	peer, err := syscall.Getpeername(fd)
	if err != nil {
		return fmt.Errorf("--lifetime-pipe-fd %d is a socket without a connected peer (%w), not a connected UNIX-domain stream socketpair endpoint", fd, err)
	}
	if _, ok := peer.(*syscall.SockaddrUnix); !ok {
		return fmt.Errorf("--lifetime-pipe-fd %d is a connected non-UNIX socket, not a connected UNIX-domain stream socketpair endpoint", fd)
	}
	return nil
}

// fdTypeName names what the descriptor actually is, which is more useful to
// whoever mistyped the number than a raw mode word.
//
// It takes the whole Stat_t rather than the mode so that no conversion is
// needed at either end: Stat_t.Mode is uint32 on Linux and uint16 on Darwin, so
// any fixed parameter type is a redundant conversion on one of them and the
// unconvert linter is right to reject it. The untyped syscall constants adapt to
// whichever width the platform uses.
func fdTypeName(st syscall.Stat_t) string {
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFSOCK:
		return "socket"
	case syscall.S_IFREG:
		return "regular file"
	case syscall.S_IFDIR:
		return "directory"
	case syscall.S_IFCHR:
		return "character device"
	case syscall.S_IFBLK:
		return "block device"
	case syscall.S_IFLNK:
		return "symlink"
	default:
		return fmt.Sprintf("descriptor of type %#o", st.Mode&syscall.S_IFMT)
	}
}
