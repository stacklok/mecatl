//go:build linux

package executionexecutor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const descendantCleanupTimeout = 2 * time.Second

var subreaperEnabled atomic.Bool

type commandResult struct {
	stdout    []byte
	stderr    []byte
	exitCode  int
	truncated bool
}

// EnableCommandExecution makes this process a Linux child subreaper. It must be
// called by the dedicated executor helper before it starts any other process.
func EnableCommandExecution() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("enable executor child subreaper: %w", err)
	}
	subreaperEnabled.Store(true)
	return nil
}

func runIsolatedCommand(ctx context.Context, dir, command string, outputLimit int) (commandResult, bool, error) {
	if !subreaperEnabled.Load() {
		return commandResult{}, false, errors.New("command execution requires the dedicated subreaper helper")
	}
	var stdout, stderr boundedWriter
	stdout.limit = outputLimit
	stderr.limit = outputLimit
	cmd := exec.Command("/bin/sh", "-c", command) // #nosec G204 -- the command is the authenticated tool payload.
	cmd.Dir = dir
	cmd.Env = []string{"HOME=/workspace", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "TMPDIR=/tmp", "GOTMPDIR=/tmp"}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return commandResult{}, true, err
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		waitErr = <-waited
	}

	// The shell may have exited successfully while background descendants remain.
	// Kill its original process group, then repeatedly kill every process reparented
	// to this dedicated subreaper. ECHILD is the only clean-completion proof.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), descendantCleanupTimeout)
	defer cancel()
	proven := reapAllChildren(cleanupCtx)

	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode(waitErr), truncated: stdout.truncated || stderr.truncated}
	if ctx.Err() != nil {
		return result, proven, ctx.Err()
	}
	return result, proven, waitErr
}

func reapAllChildren(ctx context.Context) bool {
	for {
		for _, pid := range directChildren() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			switch {
			case errors.Is(err, syscall.ECHILD):
				return true
			case err != nil:
				return false
			case pid <= 0:
				goto wait
			}
		}
	wait:
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func directChildren() []int {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil
	}
	seen := make(map[int]struct{})
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err == nil && pid > 0 {
				seen[pid] = struct{}{}
			}
		}
	}
	out := make([]int, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type boundedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			w.truncated = true
		}
		_, _ = w.buf.Write(p)
	} else if original > 0 {
		w.truncated = true
	}
	return original, nil
}

func (w *boundedWriter) Bytes() []byte { return append([]byte(nil), w.buf.Bytes()...) }
