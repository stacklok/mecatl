//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var daemonOwnershipWait = 30 * time.Second

type daemonOwnership struct {
	file *os.File
}

func acquireDaemonOwnership(stateDir string) (*daemonOwnership, error) {
	path := filepath.Join(stateDir, "microvmd.service.lock")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open microvmd service ownership: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	deadline := time.Now().Add(daemonOwnershipWait)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			info, statErr := file.Stat()
			var stat *syscall.Stat_t
			if info != nil {
				stat, _ = info.Sys().(*syscall.Stat_t)
			}
			uid := os.Getuid()
			if statErr != nil || info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat == nil || uid < 0 || uint64(uid) > uint64(^uint32(0)) || stat.Uid != uint32(uid) { // #nosec G115 -- range checked above.
				_ = file.Close()
				return nil, errors.New("microvmd service ownership is not a private owner-controlled regular file")
			}
			return &daemonOwnership{file: file}, nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock microvmd service ownership: %w", err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, errors.New("another microvmd launch still owns daemon startup after 30 seconds")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (o *daemonOwnership) close() {
	if o == nil || o.file == nil {
		return
	}
	_ = syscall.Flock(int(o.file.Fd()), syscall.LOCK_UN)
	_ = o.file.Close()
}
