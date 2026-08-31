//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
)

// rawPipe returns a pipe whose READ end has NO Go owner, plus an owned write end.
//
// os.Pipe would be the obvious choice and is the wrong one here. It hands back an
// *os.File for the read end, and every test that then passes r.Fd() to
// openLifetimePipe leaves TWO owners of one descriptor: the test's wrapper and
// the one openLifetimePipe adopts. Whichever is collected first closes the fd,
// and the other closes that NUMBER again later — by which time an unrelated
// socket may hold it. The damage never shows up in the test that caused it; it
// shows up as some other test's connection dying, which is precisely how the
// production bug in openLifetimePipe was found (a ten-minute hang in an
// unrelated http.Get).
//
// A raw pipe(2) descriptor has exactly one owner: whoever adopts it.
func rawPipe(t *testing.T) (readFD int, write *os.File) {
	t.Helper()
	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatalf("syscall.Pipe: %v", err)
	}
	return fds[0], os.NewFile(uintptr(fds[1]), "lifetime-pipe-write-end")
}
