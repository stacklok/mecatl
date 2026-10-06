//go:build linux || darwin

package microvm

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openatExistingFile(dirFD int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("private file is unsafe")
	}
	return file, nil
}
