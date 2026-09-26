//go:build linux || darwin

package microvmmanager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func daemonOwnershipHeld(stateDir string) (bool, error) {
	path := filepath.Join(stateDir, "microvmd.service.lock")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, fmt.Errorf("open microvmd service ownership: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false, errors.New("microvmd service ownership is not a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	uid := os.Getuid()
	if !ok || uid < 0 || uint64(uid) > uint64(^uint32(0)) || stat.Uid != uint32(uid) { // #nosec G115 -- range checked above.
		return false, errors.New("microvmd service ownership is not owned by the current user")
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, fmt.Errorf("inspect microvmd service ownership: %w", err)
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return false, nil
}
