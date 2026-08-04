//go:build unix

package procgroup_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/procgroup"
)

// alive reports whether pid exists (signal 0 probes without delivering).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestConfigureCancelKillsGrandchild pins the cancel-soundness contract: a
// shell that backgrounds a grandchild (`sleep 30 &`) must see that grandchild
// SIGKILLed when the context is cancelled — not orphaned. Without the
// process-group Cancel, exec kills only the direct child (the sh) and the
// grandchild lives on.
func TestConfigureCancelKillsGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		"sleep 30 & echo $! > "+pidFile+"; wait")
	procgroup.Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the grandchild to exist and its pid to be recorded.
	var grandPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				if pid, err := strconv.Atoi(s); err == nil {
					grandPID = pid
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandPID == 0 {
		t.Fatal("grandchild pid never appeared in pid file")
	}
	if !alive(grandPID) {
		t.Fatalf("grandchild %d should be alive before cancel", grandPID)
	}

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Log("Wait returned nil after cancel (acceptable: exec reports ctx error via ctx.Err())")
	}

	// The grandchild must be dead — poll briefly: SIGKILL delivery is
	// synchronous, but reaping/state visibility can lag a tick.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && alive(grandPID) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(grandPID) {
		t.Fatalf("grandchild %d survived ctx cancel — process-group kill broken", grandPID)
	}
}

// TestConfigureCancelReapsWholeGroup is the multi-grandchild variant: two
// backgrounded children must BOTH die on cancel.
func TestConfigureCancelReapsWholeGroup(t *testing.T) {
	dir := t.TempDir()
	pid1 := filepath.Join(dir, "a.pid")
	pid2 := filepath.Join(dir, "b.pid")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		"sleep 30 & echo $! > "+pid1+"; sleep 30 & echo $! > "+pid2+"; wait")
	procgroup.Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	readPID := func(path string) int {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(path); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					return pid
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		return 0
	}
	p1, p2 := readPID(pid1), readPID(pid2)
	if p1 == 0 || p2 == 0 {
		t.Fatalf("grandchild pids not recorded: %d, %d", p1, p2)
	}

	cancel()
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (alive(p1) || alive(p2)) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(p1) || alive(p2) {
		t.Fatalf("grandchildren survived cancel: %d alive=%v, %d alive=%v",
			p1, alive(p1), p2, alive(p2))
	}
}
