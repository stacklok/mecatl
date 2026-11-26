package executionexecutor

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// touchWorkspace creates an unnamed file, which no workspace reader can see,
// where the file system supports one.
func touchWorkspace(root string) error {
	fd, err := unix.Open(root, unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EISDIR) {
		return touchNamed(root)
	}
	if err != nil {
		return &fs.PathError{Op: "open", Path: root, Err: err}
	}
	f := os.NewFile(uintptr(fd), root)
	return errors.Join(f.Sync(), f.Close())
}
