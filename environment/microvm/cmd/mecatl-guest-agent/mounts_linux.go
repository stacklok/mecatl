//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/guest/netcfg"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func lockWorkloadPrivileges() error {
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

func prepareGuestNetwork() error {
	return netcfg.Configure(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

type guestMount struct {
	path, tag string
	readOnly  bool
}

func guestMounts() []guestMount {
	return []guestMount{
		{path: worktree.GuestWorkspace, tag: "mecatl-workspace"},
		{path: worktree.GuestMetadata, tag: "mecatl-git-metadata"},
		{path: worktree.GuestObjectStore, tag: "mecatl-git-objects", readOnly: true},
	}
}

func prepareGuestMounts() error {
	return mountGuestFilesystems(guestMounts())
}

func repositoryGuestMounts() []guestMount {
	return []guestMount{
		{path: "/run/mecatl/repositories", tag: "mecatl-repository-logical"},
		{path: worktree.GuestObjectStore, tag: "mecatl-git-objects", readOnly: true},
	}
}

func prepareRepositoryGuestMount() error {
	return mountGuestFilesystems(repositoryGuestMounts())
}

func mountGuestFilesystems(mounts []guestMount) error {
	for _, mount := range mounts {
		if err := os.MkdirAll(mount.path, 0o755); err != nil {
			return err
		}
		flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV)
		if mount.readOnly {
			flags |= syscall.MS_RDONLY
		}
		var err error
		for range 20 {
			err = syscall.Mount(mount.tag, mount.path, "virtiofs", flags, "")
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("mount virtiofs tag %q at %s: %w", mount.tag, mount.path, err)
		}
	}
	return nil
}
