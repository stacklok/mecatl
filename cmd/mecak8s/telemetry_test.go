package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

// fakeMetricsCollector is a minimal httptest server that accepts the OTLP/HTTP
// protobuf POST (/v1/metrics) and records the raw ExportMetricsServiceRequest
// bodies. It keeps the test OFFLINE (no real collector). Shared shape with
// internal/adapter/telemetry/otlp_metrics_test.go's collector and
// cmd/mecatequi's.
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

func requestsHaveResourceAttrs(reqs []*otlpmetrics.ExportMetricsServiceRequest, want map[string]string) bool {
	for _, req := range reqs {
		for _, rm := range req.GetResourceMetrics() {
			if protoAttrsMatch(rm.GetResource().GetAttributes(), want) {
				return true
			}
		}
	}
	return false
}

// freeLoopbackPort asks the OS for a free loopback TCP port and returns its
// "127.0.0.1:port" address, closing the probe listener so serve can rebind. A
// tiny TOCTOU window remains (the port could be reclaimed), but it is narrow
// enough for a test that binds within milliseconds.
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listener: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

func TestTelemetryInstallationIDFlagAndEnvironment(t *testing.T) {
	const envID = "123e4567-e89b-12d3-a456-426614174000"
	const flagID = "018f5e20-8c5a-7d89-b456-426614174001"

	t.Setenv("MECATL_INSTALLATION_ID", envID)
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags with environment: %v", err)
	}
	if cfg.installationID != envID {
		t.Fatalf("environment installation ID = %q, want %q", cfg.installationID, envID)
	}

	cfg, err = parseFlags([]string{"--telemetry-installation-id", flagID})
	if err != nil {
		t.Fatalf("parseFlags with flag: %v", err)
	}
	if cfg.installationID != flagID {
		t.Fatalf("flag installation ID = %q, want %q", cfg.installationID, flagID)
	}

	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{name: "environment", env: "not-a-uuid"},
		{name: "flag", args: []string{"--telemetry-installation-id", "123E4567-E89B-12D3-A456-426614174000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MECATL_INSTALLATION_ID", tc.env)
			if _, err := parseFlags(tc.args); err == nil || !strings.Contains(err.Error(), "canonical UUID") {
				t.Fatalf("parseFlags malformed installation ID error = %v", err)
			}
		})
	}
}

// TestTelemetryDefaultIsNil pins the byte-identical no-telemetry posture: with no
// --otlp-* / --metrics-addr flags, buildObservability returns zero handles (Sink
// nil), so appConfig threads nil seams into app.Config. A regression that wired
// telemetry unconditionally would flip this.
func TestTelemetryDefaultIsNil(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	obs, err := buildObservability(context.Background(), cfg, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("buildObservability: %v", err)
	}
	if obs.Sink != nil {
		t.Error("default Sink must be nil")
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
	if obs.Registry != nil {
		t.Error("default Registry must be nil (no --metrics-addr)")
	}
	if obs.Shutdown == nil {
		t.Error("default Shutdown must be non-nil (a no-op the caller defers)")
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, obs)
	if ac.Sink != nil || ac.ToolCallRecorder != nil || ac.MetricsRoleScoper != nil {
		t.Error("appConfig threaded non-nil telemetry seams with no telemetry flags")
	}
}

// TestTelemetryMetricsAddrServesPrometheus builds the full serve path
// (--metrics-addr 127.0.0.1:0 loopback) over a mock+miniredis Service, drives a
// run to populate the instruments, scrapes /metrics, and asserts the prometheus
// exposition carries a mecatl series. Then SIGTERM (ctx cancel) drives
// boundedShutdown cleanly.
func TestTelemetryMetricsAddrServesPrometheus(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	cfg, err := parseFlags([]string{
		"--mock",                                     // offline: no provider key in CI
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--redis-url", mr.Addr(),
		"--redis-allow-plaintext", // disposable miniredis fixture
		"--metrics-addr", "127.0.0.1:0",
		"--grpc-addr", "127.0.0.1:0",
		"--http-addr", "127.0.0.1:0",
		"--drain-addr", "127.0.0.1:0",
		"--session-lease-k8s-namespace", "", // no k8s apiserver in a test
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	obs, err := buildObservability(context.Background(), cfg, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("buildObservability: %v", err)
	}
	if obs.Registry == nil {
		t.Fatal("scrape-only (--metrics-addr) must build a Registry")
	}

	built, err := buildIsolated(t, context.Background(), appConfig(cfg, port.NopDiagnostics{}, obs))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	// Drive a run so the instruments record data before scraping. mecak8s is a
	// file-less deployment, so the session carries no workspace.
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, rerr := built.Service.StartRun(context.Background(), sess.ID, "hello")
	if rerr != nil {
		t.Fatalf("StartRun: %v", rerr)
	}
	for range run.Events() {
		// drain to completion
	}
	built.Service.FinishRun(sess.ID, run)

	ctx, cancel := context.WithCancel(t.Context())
	metricsListening := make(chan net.Addr, 1)
	serveDone := make(chan struct{})
	var serveErr error
	go func() {
		defer close(serveDone)
		defer cancel()
		serveErr = serveWithHooks(ctx, cfg, built.Service, obs, time.Sleep, func(addr net.Addr) {
			metricsListening <- addr
		}, builtServing{brokerHandlers: built.MCPBrokerHandlers, brokerCallbackPath: built.MCPBrokerCallbackPath})
	}()
	defer func() {
		cancel()
		select {
		case <-serveDone:
			if serveErr != nil {
				t.Errorf("serve: %v", serveErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not return within 10s of ctx cancel")
		}
	}()

	var addr net.Addr
	select {
	case addr = <-metricsListening:
	case <-serveDone:
		t.Fatalf("serve exited before metrics listener startup: %v", serveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("metrics listener did not start within 5s")
	}

	// The listener is already bound; the request can wait for Serve to accept it.
	scrapeCtx, scrapeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer scrapeCancel()
	req, err := http.NewRequestWithContext(scrapeCtx, http.MethodGet, "http://"+addr.String()+"/metrics", nil)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200; body: %s", resp.StatusCode, b)
	}
	// The prometheus exposition renders a mecatl_* series (the domain instruments
	// — e.g. mecatl_runs_total — recorded by the run above).
	if !strings.Contains(string(b), "mecatl_") {
		t.Errorf("/metrics body missing any mecatl_ series; got:\n%s", b)
	}
}

func TestTelemetryMetricsAddrBindFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	addr := occupied.Addr().String()
	cfg, err := parseFlags([]string{
		"--metrics-addr", addr,
		"--grpc-addr", "127.0.0.1:0",
		"--http-addr", "127.0.0.1:0",
		"--drain-addr", "127.0.0.1:0",
		"--product-metrics=false",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	obs, err := buildObservability(t.Context(), cfg, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("buildObservability: %v", err)
	}
	defer obs.Shutdown(context.Background())
	built, err := buildIsolated(t, t.Context(), app.Config{
		Workspace:   t.TempDir(),
		UseMock:     true,
		NoSoul:      true,
		NoUserModel: true,
		Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	listening := false
	err = serveWithHooks(ctx, cfg, built.Service, obs, time.Sleep, func(net.Addr) {
		listening = true
	}, builtServing{brokerHandlers: built.MCPBrokerHandlers, brokerCallbackPath: built.MCPBrokerCallbackPath})
	if err == nil || !strings.Contains(err.Error(), "listen metrics") || !strings.Contains(err.Error(), addr) {
		t.Fatalf("serve with occupied metrics address = %v, want listen metrics error naming %s", err, addr)
	}
	if listening {
		t.Fatal("metrics listener reported startup despite bind failure")
	}
}

// TestTelemetryMetricsAddrRejectsNonLoopback asserts a non-loopback
// --metrics-addr is REJECTED at parse time (fail-closed).
func TestTelemetryMetricsAddrRejectsNonLoopback(t *testing.T) {
	_, err := parseFlags([]string{"--metrics-addr", "0.0.0.0:9090"})
	if err == nil {
		t.Fatal("parseFlags accepted a non-loopback --metrics-addr; want a fail-closed rejection")
	}
	if !strings.Contains(err.Error(), "not loopback") {
		t.Errorf("error should name the loopback requirement; got %q", err.Error())
	}
}

// TestTelemetryPushesRunMetricsOnExit proves the OTLP-push ON path works through
// serve(): buildObservability→serve→flush-on-SIGTERM delivers a single run's
// metrics to a fake OTLP/HTTP collector. It mirrors cmd/mecatequi's
// TestTelemetryPushesRunMetricsOnExit but drives the daemon serve() loop and the
// SIGTERM (ctx cancel) path rather than a single-shot realMain. Without this
// test, only the default-off and scrape paths were covered — the push ON path
// was a gap.
func TestTelemetryPushesRunMetricsOnExit(t *testing.T) {
	coll := newFakeMetricsCollector(t)
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	cfg, err := parseFlags([]string{
		"--mock",
		"--permission-mode=yolo", "--guardrails=off", // unchecked allow-all is yolo
		"--redis-url", mr.Addr(),
		"--redis-allow-plaintext", // disposable miniredis fixture
		"--grpc-addr", "127.0.0.1:0",
		"--http-addr", "127.0.0.1:0",
		"--drain-addr", "127.0.0.1:0",
		"--session-lease-k8s-namespace", "", // no k8s apiserver in a test
		"--otlp-metrics-endpoint", coll.addr(),
		"--otlp-metrics-protocol", "http",
		"--otlp-insecure",
		"--otlp-shutdown-timeout", "2s",
		"--telemetry-installation-id", "123e4567-e89b-12d3-a456-426614174000",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	obs, err := buildObservability(context.Background(), cfg, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("buildObservability: %v", err)
	}
	if obs.Metrics == nil {
		t.Fatal("OTLP-push ON path must build a Metrics handle")
	}

	built, err := buildIsolated(t, context.Background(), appConfig(cfg, port.NopDiagnostics{}, obs))
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	// Drive a run so the instruments record data before the SIGTERM flush. mecak8s
	// is a file-less deployment, so the session carries no workspace.
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, rerr := built.Service.StartRun(context.Background(), sess.ID, "hello")
	if rerr != nil {
		t.Fatalf("StartRun: %v", rerr)
	}
	for range run.Events() {
		// drain to completion
	}
	built.Service.FinishRun(sess.ID, run)

	// SIGTERM: start serve() in a goroutine, cancel ctx (the SIGTERM path), and
	// wait for serve to return. Mirrors run()'s LIFO defer sequence: serve()
	// returns on ctx cancel, THEN run()'s defer flushTelemetry fires the OTLP
	// flush BEFORE built.Close(). The test reproduces that order so the flush
	// path under test is the one production uses.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serve(ctx, cfg, built.Service, obs, builtServing{brokerHandlers: built.MCPBrokerHandlers, brokerCallbackPath: built.MCPBrokerCallbackPath})
	}()
	cancel()
	select {
	case <-serveErr:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return within 10s of SIGTERM (ctx cancel)")
	}
	// The flush-on-SIGTERM seam run() defers (BEFORE built.Close(), LIFO): a
	// bounded ctx so a dead collector cannot hang shutdown.
	flushTelemetry(io.Discard, obs, cfg.otlpShutdownTimeout)

	reqs := coll.snapshots()
	if len(reqs) == 0 {
		t.Fatal("fake OTLP collector received no metric exports; flush-on-SIGTERM did not fire")
	}
	if !requestsHaveResourceAttrs(reqs, map[string]string{
		"service.name":           "mecak8s",
		"mecatl.installation.id": "123e4567-e89b-12d3-a456-426614174000",
	}) {
		t.Fatal("OTLP metrics resource missing mecak8s service name or installation ID")
	}
	if got := sumMetricInt(t, reqs, "mecatl.runs", map[string]string{"stop": string(session.StopEndTurn), "role": "main"}); got != 1 {
		t.Errorf("mecatl.runs{stop=end_turn,role=main} exported = %d, want 1", got)
	}
	// The mock run deterministically emits EXACTLY one EvSessionInit, so the
	// mecatl.events{type="session.init"} counter must be exactly 1 — a >= 1
	// assertion would let a double-count bug through (same discipline as
	// mecatequi's TestTelemetryPushesRunMetricsOnExit).
	if got := sumMetricInt(t, reqs, "mecatl.events", map[string]string{"type": string(session.EvSessionInit), "role": "main"}); got != 1 {
		t.Errorf("mecatl.events{type=session.init,role=main} exported = %d, want 1", got)
	}
}
