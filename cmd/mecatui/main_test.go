package main

import (
	"bytes"
	"context"
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

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/testutil/testhome"
)

// TestMain dispatches to the subprocess harnesses when their environment variables
// are set. Otherwise it runs the normal isolated test suite.
func TestMain(m *testing.M) {
	os.Exit(testhome.Run("mecatui", func() int {
		if id, ok := os.LookupEnv("MECATUI_TEST_EXIT_HANDOFF_ID"); ok {
			runExitHandoffProcessHarness(id)
			return 0
		}
		if os.Getenv("MECATUI_TEST_SIGNAL_HANDLER") != "" {
			run([]string{})
			return 0
		}
		return m.Run()
	}))
}

// TestResolveThemeAutoDetect pins the light/dark auto-detect gate (ADR 0280):
// armed only when no explicit theme was given AND stdout is a real terminal —
// every other combination (explicit theme, redirected stdout, or both) must
// leave it disarmed, since an explicit --theme/MECATUI_THEME always wins and a
// non-TTY stdout must never see the OSC background-colour query escape.
func TestResolveThemeAutoDetect(t *testing.T) {
	cases := []struct {
		name        string
		theme       string
		stdoutIsTTY bool
		want        bool
	}{
		{name: "no explicit theme, real TTY", theme: "", stdoutIsTTY: true, want: true},
		{name: "no explicit theme, redirected stdout", theme: "", stdoutIsTTY: false, want: false},
		{name: "explicit theme, real TTY", theme: "solar", stdoutIsTTY: true, want: false},
		{name: "explicit theme, redirected stdout", theme: "solar", stdoutIsTTY: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config{theme: tc.theme}
			if got := resolveThemeAutoDetect(cfg, tc.stdoutIsTTY); got != tc.want {
				t.Errorf("resolveThemeAutoDetect(theme=%q, stdoutIsTTY=%v) = %v, want %v",
					tc.theme, tc.stdoutIsTTY, got, tc.want)
			}
		})
	}
}

// TestResolveKeyboardProbe pins the keyboard-capability deadline's gate: armed
// only when stdout is a real terminal. The deadline exists to interpret silence
// as "a modified Enter is unconfirmed", and redirected output is silent for a
// reason that has nothing to do with the terminal — arming it there would
// downgrade the newline hint for every piped or captured run.
func TestResolveKeyboardProbe(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stdoutIsTTY bool
		want        bool
	}{
		{name: "real TTY arms the deadline", stdoutIsTTY: true, want: true},
		{name: "redirected stdout leaves it disarmed", stdoutIsTTY: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveKeyboardProbe(tc.stdoutIsTTY); got != tc.want {
				t.Errorf("resolveKeyboardProbe(stdoutIsTTY=%v) = %v, want %v", tc.stdoutIsTTY, got, tc.want)
			}
		})
	}
}

// TestEmbeddedConfigWiresMCPProfileLoader pins the fix for the silent-ignore of
// operator-tier mcp.servers by mecatui's embedded server: embeddedConfig MUST set
// a non-nil MCPProfileLoader so app.Build loads the operator profiles instead of
// dropping them. The test uses parseFlags(nil), the same cmd test seam the other
// bare-mode config tests in this package use.
func TestEmbeddedConfigWiresMCPProfileLoader(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	ac := embeddedConfig(cfg, nil)
	if ac.MCPProfileLoader == nil {
		t.Fatal("app.Config.MCPProfileLoader is nil; operator profiles would be ignored")
	}
	if _, ok := ac.MCPProfileLoader.(*cliconfig.MCPProfileResolver); !ok {
		t.Fatalf("MCPProfileLoader = %T, want *cliconfig.MCPProfileResolver", ac.MCPProfileLoader)
	}
	if ac.MCPAuthorityLoader == nil {
		t.Fatal("app.Config.MCPAuthorityLoader is nil; broker mode would not use canonical authority resolution")
	}
	if _, ok := ac.MCPAuthorityLoader.(*cliconfig.MCPProfileResolver); !ok {
		t.Fatalf("MCPAuthorityLoader = %T, want *cliconfig.MCPProfileResolver", ac.MCPAuthorityLoader)
	}
	if ac.MCPAuthorityDefault != mcpauthority.Global {
		t.Errorf("MCPAuthorityDefault = %q, want %q", ac.MCPAuthorityDefault, mcpauthority.Global)
	}
	if !ac.MCPBrokerSupported {
		t.Error("MCPBrokerSupported = false, want true")
	}
}

func TestEmbeddedConfigBuildAcceptsBrokerAuthority(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("mcp:\n  mode: broker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ac := embeddedConfig(config{workspace: t.TempDir(), mock: true}, nil)
	ac.MockProvider = mockllm.New()
	ac.PermissionConfigs = []string{settings}
	ac.StoreDir = ""
	ac.MemoryDir = t.TempDir()
	ac.UserModelDir = t.TempDir()
	ac.ToolHiveEnabled = false
	built, err := buildIsolated(t, context.Background(), ac)
	if err != nil {
		t.Fatalf("embedded broker authority Build: %v", err)
	}
	t.Cleanup(built.Close)
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
