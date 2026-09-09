package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stacklok/mecatl/engine/session"
)

// fakeMetricsCollector is a minimal httptest server that accepts the OTLP/HTTP
// protobuf POST (/v1/metrics) and records the raw ExportMetricsServiceRequest
// bodies. It keeps the test OFFLINE (no real collector). Shared shape with
// internal/adapter/telemetry/otlp_metrics_test.go's collector.
//
// stall, when > 0, makes the handler sleep this long before responding so a
// timeout-bound flush can be exercised — it respects the request context so an
// exporter cancel (the caller's Shutdown ctx) returns promptly rather than
// blocking t.Cleanup's server Close.
type fakeMetricsCollector struct {
	mu       sync.Mutex
	requests []*otlpmetrics.ExportMetricsServiceRequest
	srv      *httptest.Server
	stall    time.Duration
}

func newFakeMetricsCollector(t *testing.T) *fakeMetricsCollector {
	return newFakeMetricsCollectorWithStall(t, 0)
}

func newFakeMetricsCollectorWithStall(t *testing.T, stall time.Duration) *fakeMetricsCollector {
	t.Helper()
	c := &fakeMetricsCollector{stall: stall}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.stall > 0 {
			timer := time.NewTimer(c.stall)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := new(otlpmetrics.ExportMetricsServiceRequest)
		if perr := proto.Unmarshal(body, req); perr != nil {
			http.Error(w, perr.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.requests = append(c.requests, req)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeMetricsCollector) addr() string { return c.srv.Listener.Addr().String() }

func (c *fakeMetricsCollector) snapshots() []*otlpmetrics.ExportMetricsServiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*otlpmetrics.ExportMetricsServiceRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

// sumMetricInt returns the cumulative int64 sum of the named Sum metric across
// every received request whose data points match ALL the given attribute pairs.
func sumMetricInt(t *testing.T, reqs []*otlpmetrics.ExportMetricsServiceRequest, name string, want map[string]string) int64 {
	t.Helper()
	var total int64
	for _, req := range reqs {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() != name {
						continue
					}
					sum := m.GetSum()
					if sum == nil {
						continue
					}
					for _, dp := range sum.GetDataPoints() {
						if protoAttrsMatch(dp.GetAttributes(), want) {
							total += dp.GetAsInt()
						}
					}
				}
			}
		}
	}
	return total
}

func protoAttrsMatch(attrs []*commonv1.KeyValue, want map[string]string) bool {
	for k, v := range want {
		found := false
		for _, a := range attrs {
			if a.GetKey() != k {
				continue
			}
			if sv := a.GetValue().GetStringValue(); sv == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestTelemetryDefaultIsNil pins the byte-identical no-telemetry posture: with
// no --otlp-* flags, buildObservability returns zero handles (Sink nil), so
// appConfig threads nil seams into app.Config. A regression that wired telemetry
// unconditionally would flip this.
func TestTelemetryDefaultIsNil(t *testing.T) {
	f, err := parseFlags([]string{"--prompt", "x", "--mock"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	obs, err := buildObservability(context.Background(), f, newDiagnostics())
	if err != nil {
		t.Fatalf("buildObservability: %v", err)
	}
	if obs.Sink != nil {
		t.Error("default (no --otlp-*) Sink must be nil")
	}
	if obs.ToolCallRecorder != nil {
		t.Error("default ToolCallRecorder must be nil")
	}
	if obs.MetricsRoleScoper != nil {
		t.Error("default MetricsRoleScoper must be nil")
	}
	if obs.Metrics != nil {
		t.Error("default Metrics must be nil")
	}
	if obs.Shutdown == nil {
		t.Error("default Shutdown must be non-nil (a no-op the caller defers)")
	}
	// The appConfig it produces must carry nil telemetry seams too.
	cfg := appConfig(f, newDiagnostics(), obs)
	if cfg.Sink != nil || cfg.ToolCallRecorder != nil || cfg.MetricsRoleScoper != nil {
		t.Error("appConfig threaded non-nil telemetry seams with no --otlp-* flags")
	}
}

// TestTelemetryPushesRunMetricsOnExit drives the WHOLE realMain path
// (--mock + --otlp-metrics-endpoint pointing at a fake OTLP/HTTP collector) over
// a hermetic git repo and asserts the collector received the run's metrics:
// mecatl.runs{stop="end_turn"} == 1 and a mecatl.events{type="session.init"}
// series. This proves the flush-before-exit seam pushes a single-shot run's
// metrics before os.Exit.
func TestTelemetryPushesRunMetricsOnExit(t *testing.T) {
	coll := newFakeMetricsCollector(t)
	repo := t.TempDir()
	initTestRepo(t, repo)

	var stdout, stderr bytes.Buffer
	code := realMain([]string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
		"--out-summary", filepath.Join(t.TempDir(), "summary.json"),
		"--otlp-metrics-endpoint", coll.addr(),
		"--otlp-metrics-protocol", "http",
		"--otlp-insecure",
		"--otlp-shutdown-timeout", "2s",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}

	reqs := coll.snapshots()
	if len(reqs) == 0 {
		t.Fatalf("fake OTLP collector received no metric exports; flush-before-exit did not fire\nstderr=%s", stderr.String())
	}
	if got := sumMetricInt(t, reqs, "mecatl.runs", map[string]string{"stop": string(session.StopEndTurn), "role": "main"}); got != 1 {
		t.Errorf("mecatl.runs{stop=end_turn,role=main} exported = %d, want 1", got)
	}
	// The mock run deterministically emits EXACTLY one EvSessionInit, so the
	// mecatl.events{type="session.init"} counter must be exactly 1 — a >= 1
	// assertion would let a double-count bug through.
	if got := sumMetricInt(t, reqs, "mecatl.events", map[string]string{"type": string(session.EvSessionInit), "role": "main"}); got != 1 {
		t.Errorf("mecatl.events{type=session.init,role=main} exported = %d, want 1", got)
	}
}

// TestTelemetryShutdownTimeoutBounded asserts a tight --otlp-shutdown-timeout
// bounds the flush against a STALLING collector (one that accepts the connection
// then sleeps). It drives the WHOLE realMain defer chain so the bound is
// exercised at the seam a real exit uses (flushTelemetry via the LIFO defer).
//
// The previous shape pointed at 127.0.0.1:1, which on Linux yields an INSTANT
// ECONNREFUSED — the test passed in ~5ms regardless of whether the bound worked.
// The stalling collector (mirroring internal/adapter/telemetry's
// TestSetupOTLPMetricsShutdownBoundedByContext) accepts the connection and sleeps
// PAST the guard bound, so the test FAILS if the bound is removed (realMain would
// block ~ the stall, tripping the guard) while staying comfortably under the guard
// when the bound holds (~ the configured timeout).
func TestTelemetryShutdownTimeoutBounded(t *testing.T) {
	const (
		shutdownTimeout = 150 * time.Millisecond
		// The stall MUST exceed the guard so an UNBOUNDED flush (bound removed) trips
		// the guard: unblocked, realMain blocks ~ stall.
		stall = 1200 * time.Millisecond
		// Guard bound: comfortably above the configured timeout (so scheduler/exporter
		// slack does not flake) but comfortably below the stall (so a missing bound is
		// caught). Bounded: ~shutdownTimeout (~170ms). Unbounded: ~stall (1.2s) → trips.
		guard = 600 * time.Millisecond
	)
	// A stalling collector: accepts the connection, then sleeps past the guard.
	coll := newFakeMetricsCollectorWithStall(t, stall)
	repo := t.TempDir()
	initTestRepo(t, repo)

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := realMain([]string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
		"--out-summary", filepath.Join(t.TempDir(), "summary.json"),
		"--otlp-metrics-endpoint", coll.addr(),
		"--otlp-metrics-protocol", "http",
		"--otlp-insecure",
		"--otlp-shutdown-timeout", shutdownTimeout.String(),
	}, &stdout, &stderr)
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}
	if elapsed > guard {
		t.Errorf("realMain against a stalling collector took %v; the %v --otlp-shutdown-timeout bound should keep it under %v (stall=%v)\nstderr=%s",
			elapsed, shutdownTimeout, guard, stall, stderr.String())
	}
}

// Ensure the os import is referenced (filepath used above; os via t.TempDir).
var _ = os.DevNull
