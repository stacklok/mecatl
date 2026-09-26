//go:build darwin

package microvm

import "golang.org/x/sys/unix"

func renameatNoReplace(oldDirFD int, oldPath string, newDirFD int, newPath string) error {
	return unix.RenameatxNp(oldDirFD, oldPath, newDirFD, newPath, unix.RENAME_EXCL)
}
