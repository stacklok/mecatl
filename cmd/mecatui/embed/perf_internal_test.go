package embed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
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

// TestSetupPerfDisabledPreservesPreexistingTap pins the perf-off half of the
// Task 12 product-metrics-tap preservation contract: with PerfConfig.Enabled
// false (mecatui's default), setupPerf's early return must leave a
// preexisting cfg.Sink/cfg.ToolCallRecorder — set by main.go BEFORE embed.Start
// when the product-metrics pipeline is enabled — completely untouched, so it
// survives byte-identically into the running engine.
func TestSetupPerfDisabledPreservesPreexistingTap(t *testing.T) {
	preexistingSink := fakeSink{emitted: new(bool)}
	preexistingRecorder := fakeRecorder{recorded: new(bool)}
	cfg := app.Config{
		Workspace:        t.TempDir(),
		Model:            "mock",
		Sink:             preexistingSink,
		ToolCallRecorder: preexistingRecorder,
	}

	if _, err := setupPerf(context.Background(), PerfConfig{}, &cfg, t.TempDir()); err != nil {
		t.Fatalf("setupPerf(disabled): %v", err)
	}
	if cfg.Sink != preexistingSink {
		t.Errorf("perf-off cfg.Sink = %#v, want the untouched preexisting sink", cfg.Sink)
	}
	if cfg.ToolCallRecorder != preexistingRecorder {
		t.Errorf("perf-off cfg.ToolCallRecorder = %#v, want the untouched preexisting recorder", cfg.ToolCallRecorder)
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

// fakeSink and fakeRecorder are minimal port.EventSink/port.ToolCallRecorder
// probes recording whether they were invoked.
type fakeSink struct{ emitted *bool }

func (f fakeSink) Emit(context.Context, session.Event) { *f.emitted = true }

type fakeRecorder struct{ recorded *bool }

func (f fakeRecorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
	*f.recorded = true
}

// TestWirePerfSinksFoldsInPreexistingTap pins the product-metrics-tap
// preservation contract (Task 12): whatever cfg.Sink/cfg.ToolCallRecorder ALREADY
// held before setupPerf/wirePerfSinks ran (main.go wires the product-metrics
// pipeline onto composition BEFORE embed.Start, i.e. before this ever runs) MUST
// still receive every event/tool-call fan-out AFTER perf wiring — not be
// silently overwritten. Exercises the real setupPerf → wirePerfSinks path with
// PerfConfig.Enabled true (the perf-on case; the perf-off case is covered by
// TestSetupPerfDisabledLeavesTelemetrySeamsNil next to it, which asserts a
// preexisting cfg.Sink is untouched because setupPerf never mutates cfg at all).
func TestWirePerfSinksFoldsInPreexistingTap(t *testing.T) {
	var preexistingEmitted, preexistingRecorded bool
	preexistingSink := fakeSink{emitted: &preexistingEmitted}
	preexistingRecorder := fakeRecorder{recorded: &preexistingRecorded}

	cfg := app.Config{
		Workspace:        t.TempDir(),
		Model:            "mock",
		Sink:             preexistingSink,
		ToolCallRecorder: preexistingRecorder,
	}

	// A dedicated short-path temp dir for the admin unix socket: t.TempDir()
	// embeds this test's (long) name in the path, which overflows the OS unix
	// socket path length limit.
	runtimeDir, err := os.MkdirTemp("", "embedperf")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	ps, err := setupPerf(context.Background(), PerfConfig{Enabled: true}, &cfg, runtimeDir)
	if err != nil {
		t.Fatalf("setupPerf(enabled): %v", err)
	}
	t.Cleanup(func() { ps.teardown(context.Background()) })

	if cfg.Sink == nil {
		t.Fatal("perf-on cfg.Sink is nil after wiring")
	}
	cfg.Sink.Emit(context.Background(), session.Event{})
	if !preexistingEmitted {
		t.Error("perf-on wiring dropped the preexisting EventSink instead of folding it in")
	}

	if cfg.ToolCallRecorder == nil {
		t.Fatal("perf-on cfg.ToolCallRecorder is nil after wiring")
	}
	cfg.ToolCallRecorder.ToolCall(session.SessionID(""), session.ToolCall{}, session.ToolResult{}, 0, 0)
	if !preexistingRecorded {
		t.Error("perf-on wiring dropped the preexisting ToolCallRecorder instead of teeing it in")
	}
}
