package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/testutil/testhome"
)

// TestMain dispatches to run([]string{}) when the MECATUI_TEST_SIGNAL_HANDLER env
// var is set (child-process signal-test harness). Otherwise it runs the normal test
// suite.
func TestMain(m *testing.M) {
	os.Exit(testhome.Run("mecatui", func() int {
		if os.Getenv("MECATUI_TEST_SIGNAL_HANDLER") != "" {
			run([]string{})
			return 0
		}
		return m.Run()
	}))
}

func TestConventionalAuthFileIsIsolated(t *testing.T) {
	authPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl", "auth.yaml")
	if _, err := os.Stat(authPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated conventional auth path must not exist: %v", err)
	}
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if cfg.providerKeys.HasOpenAICodex() {
		t.Fatal("ordinary mecatui tests retained a conventional Codex credential")
	}
}

// syncBuffer is a bytes.Buffer guarded by a mutex, safe for concurrent writes
// from a drain goroutine and reads from the test goroutine (the plain
// bytes.Buffer shared across those two was the -race report).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startSignalChild launches the test binary as a child with the given
// MECATUI_TEST_SIGNAL_HANDLER mode and blocks until the child prints its
// "signal-handler ready" handshake (installed AFTER signal.Notify), so the
// parent's signals always land on an installed handler — never raced by -race
// startup latency. It returns the running cmd and a synchronized buffer that
// accumulates the child's merged output from the handshake onward.
func startSignalChild(t *testing.T, mode string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(exe, "-test.run", "TestSignalChildHarness$")
	cmd.Env = append(os.Environ(), "MECATUI_TEST_SIGNAL_HANDLER="+mode)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Parent reads the handshake from the pipe; the child holds the write end.
	_ = pw.Close()

	out := &syncBuffer{}
	// Read until the ready line appears (bounded so a wedged child fails fast).
	deadline := time.Now().Add(15 * time.Second)
	tmp := make([]byte, 4096)
	for !strings.Contains(out.String(), "signal-handler ready") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("child never signalled readiness; output so far: %q", out.String())
		}
		n, rerr := pr.Read(tmp)
		if n > 0 {
			_, _ = out.Write(tmp[:n])
		}
		if rerr != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("reading child handshake: %v (output: %q)", rerr, out.String())
		}
	}
	// Keep draining the pipe into out for the rest of the child's life.
	go func() {
		_, _ = io.Copy(out, pr)
	}()
	return cmd, out
}

// TestSignalChildHarness is a no-op in the parent; TestMain routes the child
// (MECATUI_TEST_SIGNAL_HANDLER set) into testSignalHandler before any test runs,
// so this body never executes in the child. It exists only to give the child's
// -test.run a valid target.
func TestSignalChildHarness(*testing.T) {}

// TestDoubleCtrlCForceExit spawns a child process with
// MECATUI_TEST_SIGNAL_HANDLER=second, sends two SIGINTs, and asserts exit code 130.
func TestDoubleCtrlCForceExit(t *testing.T) {
	cmd, out := startSignalChild(t, "second")

	// First SIGINT — the child goroutine prints the graceful-shutdown line.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("first signal: %v", err)
	}
	// Wait for the first signal to be handled before sending the second.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "shutting down gracefully") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("child never handled first signal; output: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Second SIGINT — the child goroutine fires os.Exit(130).
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("second signal: %v", err)
	}

	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("expected exit code 130, got %v (output: %q)", err, out.String())
	}
}

// TestSingleSignalGracefulExit spawns a child with
// MECATUI_TEST_SIGNAL_HANDLER=first, sends one SIGINT, and asserts exit 0 with the
// graceful-shutdown message on stderr.
func TestSingleSignalGracefulExit(t *testing.T) {
	cmd, out := startSignalChild(t, "first")

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("expected exit 0, got %v (output: %q)", err, out.String())
	}

	if !strings.Contains(out.String(), "shutting down gracefully") {
		t.Errorf("expected output to mention 'shutting down gracefully', got %q", out.String())
	}
}
