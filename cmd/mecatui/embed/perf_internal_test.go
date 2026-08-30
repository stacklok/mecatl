package embed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

// TestSetupPerfDisabledLeavesTelemetrySeamsNil pins the no-perf path (issue
// #47): with PerfConfig.Enabled false, setupPerf must return a zero perfState
// and mutate NOTHING on cfg — Sink, ToolCallRecorder, and MetricsRoleScoper all
// stay nil, so the main engine is unmetered and every child engine keeps the
// byte-identical pre-feature nil/nil telemetry shape.
func TestSetupPerfDisabledLeavesTelemetrySeamsNil(t *testing.T) {
	cfg := app.Config{Workspace: t.TempDir(), Model: "mock"}

	ps, err := setupPerf(context.Background(), PerfConfig{}, &cfg, t.TempDir())
	if err != nil {
		t.Fatalf("setupPerf(disabled): %v", err)
	}
	if ps.adminSrv != nil || ps.adminAddr != "" || ps.stop != nil || ps.shutdown != nil {
		t.Errorf("perf-off setupPerf returned a non-zero perfState: %+v", ps)
	}
	if cfg.Sink != nil {
		t.Errorf("perf-off cfg.Sink = %T, want nil", cfg.Sink)
	}
	if cfg.ToolCallRecorder != nil {
		t.Errorf("perf-off cfg.ToolCallRecorder = %T, want nil", cfg.ToolCallRecorder)
	}
	if cfg.MetricsRoleScoper != nil {
		t.Error("perf-off cfg.MetricsRoleScoper is non-nil; children must stay on the unmetered nil/nil path")
	}
}

func TestListenPrivateUnixRejectsUnsafeCollisions(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, adminSocketName)
			var err error
			if kind == "file" {
				err = os.WriteFile(path, []byte("keep"), 0o600)
			} else {
				err = os.Symlink("target", path)
			}
			if err != nil {
				t.Fatalf("prepare collision: %v", err)
			}
			if lis, listenErr := listenPrivateUnix(path); listenErr == nil {
				_ = lis.Close()
				t.Fatal("listenPrivateUnix accepted unsafe collision")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("collision was removed: %v", err)
			}
		})
	}
}

func TestSetupPerfRejectsNonLoopbackWithoutMCP(t *testing.T) {
	cfg := app.Config{Workspace: t.TempDir(), Model: "mock"}
	_, err := setupPerf(context.Background(), PerfConfig{Enabled: true, Addr: "0.0.0.0:0"}, &cfg, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("setupPerf error = %v, want non-loopback refusal", err)
	}
}
