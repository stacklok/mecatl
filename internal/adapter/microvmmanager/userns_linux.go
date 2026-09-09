//go:build linux

package microvmmanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func checkLinuxUserNamespaces(ctx context.Context) error {
	return checkLinuxUserNamespaceControls(os.ReadFile, func() error {
		cmd := exec.CommandContext(ctx, "true")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
		return cmd.Run()
	})
}

func checkLinuxUserNamespaceControls(readFile func(string) ([]byte, error), probe func() error) error {
	maximum, err := readPositiveControl(readFile, "/proc/sys/user/max_user_namespaces")
	if err != nil {
		return fmt.Errorf("unprivileged user namespaces are unavailable: %w; enable them and provide free per-user namespace quota, then retry", err)
	}
	if maximum == 0 {
		return errors.New("unprivileged user namespaces are disabled by user.max_user_namespaces=0; set a positive host limit and retry")
	}
	clone, err := readPositiveControl(readFile, "/proc/sys/kernel/unprivileged_userns_clone")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read kernel.unprivileged_userns_clone: %w", err)
	}
	if err == nil && clone == 0 {
		return errors.New("unprivileged user namespaces are disabled by kernel.unprivileged_userns_clone=0; enable the control and retry")
	}
	if err := probe(); err != nil {
		return fmt.Errorf("create an unprivileged user namespace: %w (the host policy may disable user namespaces or the current user may have exhausted its namespace quota; free quota or enable the capability, then retry)", err)
	}
	return nil
}

func readPositiveControl(readFile func(string) ([]byte, error), path string) (uint64, error) {
	data, err := readFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}
