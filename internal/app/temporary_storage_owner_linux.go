//go:build linux

package app

import (
	"fmt"
	"os"
	"syscall"
)

func validateControlledRoot(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("temporary storage managed_root: inspect absolute root: %w", err)
	}
	currentUID := os.Getuid()
	if currentUID < 0 {
		return fmt.Errorf("temporary storage managed_root: current user identity unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || uint64(stat.Uid) != uint64(currentUID) {
		return fmt.Errorf("temporary storage managed_root: absolute root must be a current-user-owned private non-symlink directory")
	}
	return nil
}
