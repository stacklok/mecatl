//go:build unix

package osfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s never populated", path)
	return 0
}

// TestCommandRunnerCancelKillsProcessGroup pins the cancel-soundness fix: a
// command that BACKGROUNDS a grandchild holding the stdout/stderr pipes
// (`sleep 30 & wait`) must see that grandchild SIGKILLed on ctx cancel — not
// orphaned. Before procgroup.Configure, exec killed only the direct child (the
// sh) and the grandchild lived on. Run must also RETURN promptly: with the
// group killed the inherited pipes close, so cmd.Wait never parks on them.
func TestCommandRunnerCancelKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	ctx, cancel := context.WithCancel(context.Background())
	r := newRunner(t, t.TempDir())

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := r.Run(ctx, "sleep 30 & echo $! > "+pidFile+"; wait", "")
		done <- err
	}()

	grandPID := waitForPIDFile(t, pidFile)
	if !pidAlive(grandPID) {
		t.Fatalf("grandchild %d should be alive before cancel", grandPID)
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel err = %v want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel — Wait parked on grandchild pipes")
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("Run took %v after cancel; group kill + WaitDelay should bound it", elapsed)
	}

	// The grandchild must be dead, not orphaned.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pidAlive(grandPID) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(grandPID) {
		t.Fatalf("grandchild %d survived ctx cancel — process-group kill broken", grandPID)
	}
}

// TestCommandRunnerTimeoutKillsProcessGroup is the deadline-path twin: with no
// explicit cancel, the runner's own ctx deadline (here an explicit short one)
// must group-kill the grandchild too.
func TestCommandRunnerTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r, err := osfs.NewCommandRunnerShell(t.TempDir(), "/bin/sh")
	if err != nil {
		t.Fatalf("NewCommandRunnerShell: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, "sleep 30 & echo $! > "+pidFile+"; wait", "")
		done <- err
	}()

	grandPID := waitForPIDFile(t, pidFile)

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run after timeout err = %v want context.DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after deadline — Wait parked on grandchild pipes")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pidAlive(grandPID) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(grandPID) {
		t.Fatalf("grandchild %d survived ctx deadline — process-group kill broken", grandPID)
	}
}
