//go:build linux

package microvm

import "golang.org/x/sys/unix"

func renameatNoReplace(oldDirFD int, oldPath string, newDirFD int, newPath string) error {
	return unix.Renameat2(oldDirFD, oldPath, newDirFD, newPath, unix.RENAME_NOREPLACE)
}
