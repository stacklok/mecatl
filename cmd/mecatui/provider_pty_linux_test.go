package main

import (
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func providerPTYName(t *testing.T, fd int) string {
	t.Helper()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("/dev/pts/%d", n)
}
