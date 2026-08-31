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
// The two conditions are the ones the flag's contract needs. An fd that is not
// open is a startup error rather than an immediate EOF, because watch() would
// read a bad descriptor as "the parent died" and the daemon would publish its
// ready file and vanish milliseconds later. An fd that IS open but is not a pipe
// is the same failure through a different door: this runs after bindListeners,
// so a stale or mistyped number can name a listener the daemon already owns,
// whose ENOTCONN also reads as parent exit.
func checkLifetimePipeFD(fd int) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("--lifetime-pipe-fd %d is not an open descriptor in this process (%w): the parent must pass the pipe's READ end as an inherited fd", fd, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return fmt.Errorf("--lifetime-pipe-fd %d is open but is a %s, not a pipe: pass the READ end of an inherited pipe — a socket, a regular file, or a descriptor this process already owns is a mistake, not a parent-liveness signal", fd, fdTypeName(st))
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
