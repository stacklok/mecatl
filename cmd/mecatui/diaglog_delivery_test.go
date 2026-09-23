package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/gofrs/flock"
)

func TestResolveTransportDeliversContendedDiagnostics(t *testing.T) {
	for _, scenario := range []string{"success", "startup-failure", "fallback-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "mecatui.log")
			owner := openDiagLogWriter(stateEnv(root), false, shared)
			t.Cleanup(func() { _ = owner.Closer.Close() })
			if owner.Path != shared || owner.Contended {
				t.Fatalf("shared log setup: path=%q contended=%v", owner.Path, owner.Contended)
			}
			if _, err := io.WriteString(owner.Writer, "owner\n"); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(shared)
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestResolveTransportDiagnosticsHelper$", "--", "diag-transport", root, scenario) //nolint:gosec // current test binary, fixed helper, test-owned paths.
			cmd.Dir = root
			cmd.Env = []string{
				"HOME=" + filepath.Join(root, "home"),
				"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
				"XDG_CONFIG_DIRS=" + filepath.Join(root, "config-dirs"),
				"XDG_DATA_HOME=" + filepath.Join(root, "data"),
				"XDG_DATA_DIRS=" + filepath.Join(root, "data-dirs"),
				"XDG_STATE_HOME=" + filepath.Join(root, "state"),
				"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
				"XDG_RUNTIME_DIR=" + root,
				"TMPDIR=" + root,
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("transport helper: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
			}
			fallback := fallbackDiagLogPath(shared, cmd.Process.Pid)
			wantNotice := "mecatui: another mecatui holds the shared diagnostics log; this instance logs to " + fallback + "\n"
			if scenario == "fallback-failure" {
				wantNotice = "mecatui: another mecatui holds the shared diagnostics log and the per-process fallback could not be opened; diagnostics are disabled for this instance\n"
			}
			if !strings.HasPrefix(stderr.String(), wantNotice) || strings.Count(stderr.String(), wantNotice) != 1 {
				t.Errorf("contention notice missing, late, or duplicated: %q", &stderr)
			}
			if got := strings.Contains(stderr.String(), "hosting an embedded mecated"); got != (scenario == "success") {
				t.Errorf("startup outcome on stderr: %q", &stderr)
			}
			if scenario != "fallback-failure" {
				contents, err := os.ReadFile(fallback)
				if err != nil {
					t.Fatal(err)
				}
				const message = "mecatui: embedded server diagnostics log opened"
				var records []string
				for line := range strings.SplitSeq(string(contents), "\n") {
					if strings.Contains(line, message) {
						records = append(records, line)
					}
				}
				if scenario == "success" {
					if len(records) != 1 || (!strings.Contains(records[0], "path="+fallback) && !strings.Contains(records[0], "path="+strconv.Quote(fallback))) {
						t.Errorf("actual fallback lacks log-open record naming itself: %q", records)
					}
				} else if len(records) != 0 {
					t.Errorf("failed startup reported a successful log-open: %q", records)
				}
			}
			if _, err := io.WriteString(owner.Writer, "still-active\n"); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(shared); err != nil || string(got) != "owner\nstill-active\n" {
				t.Errorf("shared stream changed: %q, %v", got, err)
			}
			if after, err := os.Stat(shared); err != nil || !os.SameFile(before, after) {
				t.Errorf("shared inode replaced: %v", err)
			}
		})
	}
}

// A subprocess keeps resolveTransport's real stderr and global slog wiring intact
// without redirecting process globals beneath other tests.
func TestResolveTransportDiagnosticsHelper(t *testing.T) {
	args := flag.Args()
	if len(args) != 3 || args[0] != "diag-transport" {
		return
	}
	root, scenario := args[1], args[2]
	if runtime.GOOS == "linux" {
		// The child cwd is the test-owned root. This absolute alias avoids Linux's
		// UNIX socket path limit without allocating outside the fixture.
		t.Setenv("XDG_RUNTIME_DIR", "/proc/self/cwd")
	}
	// TestMain installs its own home; restore the explicit child fixture before
	// exercising conventional discovery and refresh xdg's package-init snapshot.
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	xdg.Reload()
	configDir := filepath.Join(root, "config", "mecatl")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// An empty broker inventory disables ambient container/MCP discovery without
	// constructing a broker or contacting an external service.
	if err := os.WriteFile(filepath.Join(configDir, "settings.yaml"), []byte("mcp:\n  mode: broker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "mecatui.log")
	cfg, err := parseFlags([]string{"--mock", "--toolhive-llm=false", "--product-metrics=false", "--no-store", "--no-memory", "--workspace", root, "--user-model-dir", filepath.Join(root, "usermodel"), "--diagnostics-log", shared})
	if err != nil {
		t.Fatal(err)
	}
	if scenario != "success" {
		// This fails inside embed.Start, AFTER the production caller opens/reports
		// its sink. No clock, sleep, source inspection, or replacement caller.
		blocker := filepath.Join(root, "runtime-blocker")
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", blocker)
	}
	if scenario == "fallback-failure" {
		if err := os.Mkdir(fallbackDiagLogPath(shared, os.Getpid()), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, _, cleanup, err := resolveTransport(t.Context(), cfg)
	defer cleanup()
	if scenario == "success" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || !strings.Contains(err.Error(), "start embedded server: create runtime dir:") {
		t.Fatalf("expected deterministic embedded startup failure, got %v", err)
	}
}

func TestDiagLogFallbackFailuresPreserveStreams(t *testing.T) {
	for _, scenario := range []string{"data-symlink", "lock-symlink", "nonregular", "retention-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			shared := filepath.Join(dir, "mecatui.log")
			owner := openDiagLogWriter(stateEnv(dir), false, shared)
			t.Cleanup(func() { _ = owner.Closer.Close() })
			if owner.Path != shared || owner.Contended {
				t.Fatal("shared log setup failed")
			}
			if _, err := io.WriteString(owner.Writer, "owner\n"); err != nil {
				t.Fatal(err)
			}
			sharedBefore, err := os.Stat(shared)
			if err != nil {
				t.Fatal(err)
			}
			fallback := fallbackDiagLogPath(shared, os.Getpid())
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("preserve target\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			guarded := fallback
			switch scenario {
			case "data-symlink":
				err = os.Symlink(target, fallback)
			case "lock-symlink":
				guarded = fallback + ".lock"
				err = os.Symlink(target, guarded)
			case "nonregular":
				err = os.Mkdir(fallback, 0o700)
			case "retention-failure":
				err = os.WriteFile(fallback, []byte("preserve fallback\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(guarded)
			if err != nil {
				t.Fatal(err)
			}
			var retained []string
			sink := openDiagLogWriterWithRetainer(stateEnv(dir), false, shared, func(path string) error {
				retained = append(retained, path)
				if scenario == "retention-failure" {
					return errors.New("injected retention failure")
				}
				return retainDiagLog(path)
			})
			t.Cleanup(func() { _ = sink.Closer.Close() })
			if !sink.Contended || sink.Writer != io.Discard || sink.Path != "" {
				t.Errorf("failed fallback: contended=%v writer=%T path=%q", sink.Contended, sink.Writer, sink.Path)
			}
			wantRetained := []string{fallback}
			if scenario == "lock-symlink" {
				wantRetained = nil
			}
			if !slices.Equal(retained, wantRetained) {
				t.Errorf("retention attempts = %q, want %q (no shared retention or retry)", retained, wantRetained)
			}
			if notice := diagLogContentionNotice(sink, false); !strings.Contains(notice, "diagnostics are disabled for this instance") {
				t.Errorf("failed fallback not discoverable: %q", notice)
			}
			if _, err := io.WriteString(sink.Writer, "must be discarded\n"); err != nil {
				t.Fatal(err)
			}
			if after, err := os.Lstat(guarded); err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Errorf("unsafe/failed destination replaced: %v", err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "preserve target\n" {
				t.Errorf("symlink target changed: %q, %v", got, err)
			}
			if scenario == "retention-failure" {
				if got, err := os.ReadFile(fallback); err != nil || string(got) != "preserve fallback\n" {
					t.Errorf("failed retention changed fallback: %q, %v", got, err)
				}
			}
			if _, err := io.WriteString(owner.Writer, "still-active\n"); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(shared); err != nil || string(got) != "owner\nstill-active\n" {
				t.Errorf("shared stream changed: %q, %v", got, err)
			}
			if after, err := os.Stat(shared); err != nil || !os.SameFile(sharedBefore, after) {
				t.Errorf("shared inode replaced: %v", err)
			}
			if entries, err := os.ReadDir(dir); err != nil {
				t.Fatal(err)
			} else {
				for _, entry := range entries {
					if !slices.Contains([]string{"mecatui.log", "mecatui.log.lock", "target", filepath.Base(fallback), filepath.Base(fallback) + ".lock"}, entry.Name()) {
						t.Errorf("unexpected retry artifact: %s", entry.Name())
					}
				}
			}
			if scenario != "lock-symlink" {
				// Probe the sentinel directly: the data path is deliberately still
				// unsafe, so reopening the writer would not prove lock release.
				lock := flock.New(fallback + ".lock")
				defer func() { _ = lock.Close() }()
				if locked, err := lock.TryLock(); err != nil || !locked {
					t.Errorf("failed fallback leaked its acquired lock: locked=%v err=%v", locked, err)
				}
			}
		})
	}
}
