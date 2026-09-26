//go:build linux

package microvmmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type managedProcessHandle struct {
	fd int
}

func openManagedProcess(pid int) (managedProcessHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return managedProcessHandle{}, err
	}
	return managedProcessHandle{fd: fd}, nil
}

func (h managedProcessHandle) signal() error {
	if err := unix.PidfdSendSignal(h.fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func (h managedProcessHandle) close() error {
	if h.fd < 0 {
		return os.ErrInvalid
	}
	return unix.Close(h.fd)
}

func managedStopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

func requestManagedStop(_ context.Context, paths Paths) (bool, error) {
	data, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.pid"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return false, errors.New("invalid managed microvmd pid")
	}
	if err := validateOwnerSocket(paths.Socket); err != nil {
		return false, fmt.Errorf("refusing to signal recorded daemon without its owner-only socket: %w", err)
	}
	recordData, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.process.json"))
	if err != nil {
		return false, fmt.Errorf("managed microvmd process identity is unavailable; use the user service manager and do not signal the recorded PID: %w", err)
	}
	var record managedProcessRecord
	if err := json.Unmarshal(recordData, &record); err != nil || record.Schema != managedProcessSchema || record.PID != pid {
		return false, errors.New("managed microvmd process identity is invalid; use the user service manager and do not signal the recorded PID")
	}
	handle, err := openManagedProcess(pid)
	if err != nil {
		return false, fmt.Errorf("open exact managed microvmd process: %w", err)
	}
	defer func() { _ = handle.close() }()
	if err := validateManagedProcess(record, paths); err != nil {
		return false, err
	}
	if err := handle.signal(); err != nil {
		return false, err
	}
	return true, nil
}
