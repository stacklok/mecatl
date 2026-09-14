package main

import (
	"bytes"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func providerPTYName(t *testing.T, fd int) string {
	t.Helper()
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(fd, request, 0); err != nil {
			t.Fatal(err)
		}
	}
	var name [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatal(errno)
	}
	return string(bytes.TrimRight(name[:], "\x00"))
}
