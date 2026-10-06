//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func checkLinuxUserNamespaceCapability(ctx context.Context) error {
	data, err := os.ReadFile("/proc/sys/user/max_user_namespaces")
	if err != nil {
		return fmt.Errorf("read user namespace quota: %w", err)
	}
	maximum, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || maximum == 0 {
		return errors.New("unprivileged user namespaces are disabled; set user.max_user_namespaces to a positive value")
	}
	if data, err = os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil && strings.TrimSpace(string(data)) == "0" {
		return errors.New("unprivileged user namespaces are disabled; enable kernel.unprivileged_userns_clone")
	}
	cmd := exec.CommandContext(ctx, "true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create unprivileged user namespace: %w (enable the capability or free per-user namespace quota)", err)
	}
	return nil
}
