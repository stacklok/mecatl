package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// fakeMetricsCollector is a minimal httptest server that accepts the OTLP/HTTP
// protobuf POST (/v1/metrics) and records the raw ExportMetricsServiceRequest
// bodies. It keeps the test OFFLINE (no real collector). Shared shape with
// internal/adapter/telemetry/otlp_metrics_test.go's collector.
type fakeMetricsCollector struct {
	mu       sync.Mutex
	requests []*otlpmetrics.ExportMetricsServiceRequest
	srv      *httptest.Server
}

func newFakeMetricsCollector(t *testing.T) *fakeMetricsCollector {
	t.Helper()
	c := &fakeMetricsCollector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	obs, err := buildObservability(context.Background(), f, newDiagnostics(), io.Discard)
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

// TestTelemetryShutdownTimeoutBounded observes cancellation of a real export
// through realMain's exit defer, without charging startup and Git work against
// the shutdown timeout. The collector never responds until cleanup releases it.
func TestTelemetryShutdownTimeoutBounded(t *testing.T) {
	// Keep exporter/reader fallback timeouts beyond the diagnostic watchdog, so
	// they cannot substitute for the command's shutdown bound.
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "60000")
	t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "60000")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "60000")
	const watchdog = 30 * time.Second
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Drain the body so net/http can observe the client's disconnect while
		// the handler is blocked waiting to respond.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read export: %v", err)
			return
		}
		started <- struct{}{}
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	repo := t.TempDir()
	initTestRepo(t, repo)

	var stdout, stderr bytes.Buffer
	args := []string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
		"--out-summary", filepath.Join(t.TempDir(), "summary.json"),
		"--otlp-metrics-endpoint", srv.Listener.Addr().String(),
		"--otlp-metrics-protocol", "http",
		"--otlp-insecure",
		"--otlp-shutdown-timeout", "1s",
	}
	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		code = realMain(args, &stdout, &stderr)
	}()
	t.Cleanup(func() {
		close(release)
		select {
		case <-done:
		case <-time.After(watchdog):
			t.Error("realMain did not stop after releasing the collector")
		}
	})

	select {
	case <-started:
	case <-time.After(watchdog):
		t.Fatal("exit flush never reached the collector")
	}
	select {
	case <-canceled:
	case <-time.After(watchdog):
		t.Fatal("exit flush did not cancel the blocked export")
	}
	select {
	case <-done:
	case <-time.After(watchdog):
		t.Fatal("realMain did not return after export cancellation")
	}
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}
	if _, flushErr, ok := strings.Cut(stderr.String(), "mecatequi: telemetry flush: "); !ok || !strings.Contains(flushErr, "context deadline exceeded") {
		t.Fatalf("missing shutdown deadline diagnostic\nstderr=%s", stderr.String())
	}
}

// The exact timeout belongs to flushTelemetry, not a stopwatch around realMain.
func TestFlushTelemetryShutdownDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 150 * time.Millisecond
		start := time.Now()
		obs := observability{HeadlessTelemetryHandles: cliconfig.HeadlessTelemetryHandles{
			Shutdown: func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(start.Add(timeout)) {
					t.Fatalf("shutdown deadline = (%v, %v), want %v", deadline, ok, start.Add(timeout))
				}
				<-ctx.Done()
				return ctx.Err()
			},
		}}
		var stderr bytes.Buffer
		flushTelemetry(&stderr, obs, timeout)
		if elapsed := time.Since(start); elapsed != timeout {
			t.Fatalf("flush took %v, want %v", elapsed, timeout)
		}
		if got, want := stderr.String(), "mecatequi: telemetry flush: context deadline exceeded\n"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	})
}

// Ensure the os import is referenced (filepath used above; os via t.TempDir).
var _ = os.DevNull
