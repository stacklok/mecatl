package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stacklok/mecatl/engine/session"
)

// fakeOTLPMetricsCollector is an httptest server that accepts the OTLP/HTTP
// protobuf POST (/v1/metrics) and records the raw ExportMetricsServiceRequest
// bodies it receives. It is the metrics analogue of the trace fake collector
// pattern — the same shape otlp_test.go's Setup path uses for traces — and
// keeps the test OFFLINE (no real collector).
type fakeOTLPMetricsCollector struct {
	mu       sync.Mutex
	requests []*otlpmetrics.ExportMetricsServiceRequest
	srv      *httptest.Server
	// stall, when > 0, makes the handler sleep this long before responding so a
	// timeout-bound export can be exercised. It respects the request context so
	// an exporter cancel (its export timeout or the caller's Shutdown ctx) returns
	// promptly rather than blocking t.Cleanup's server Close.
	stall time.Duration
}

func newFakeOTLPMetricsCollector(t *testing.T, stall time.Duration) *fakeOTLPMetricsCollector {
	t.Helper()
	c := &fakeOTLPMetricsCollector{stall: stall}
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

func (c *fakeOTLPMetricsCollector) addr() string { return c.srv.Listener.Addr().String() }

func (c *fakeOTLPMetricsCollector) snapshots() []*otlpmetrics.ExportMetricsServiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*otlpmetrics.ExportMetricsServiceRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

// sumDataPoints returns the cumulative int64 sum of the named Sum metric across
// every received ExportMetricsServiceRequest whose data points match ALL the
// given attribute pairs. It is the cross-request equivalent of sumPointWith.
func sumDataPoints(t *testing.T, reqs []*otlpmetrics.ExportMetricsServiceRequest, name string, want map[string]string) int64 {
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

// protoAttrsMatch reports whether a slice of OTLP KeyValue attributes carries
// every given key=value pair (the proto analogue of attrsMatch).
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

// TestSetupNoMetricReaderWhenMetricsEndpointEmpty asserts the byte-identical
// scrape-only path: an empty MetricsEndpoint installs NO periodic reader, so a
// caller that does not opt into OTLP metrics push gets the SAME MeterProvider
// shape as before (only the prometheus reader). The prometheus registry still
// gathers the recorded series.
func TestSetupNoMetricReaderWhenMetricsEndpointEmpty(t *testing.T) {
	providers, err := Setup(t.Context(), OTLPConfig{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() {
		sdCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = providers.Shutdown(sdCtx)
	})

	m, err := NewMetrics(providers.Meter)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	// Record one run so the prometheus reader has data; the test asserts the
	// scrape path works (the scrape-only regression guard).
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop: session.StopEndTurn, Usage: session.Usage{InputTokens: 7, OutputTokens: 3},
	}})

	families, err := providers.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("scrape-only path gathered no families; prometheus reader missing")
	}
}

// TestSetupOTLPMetricsPushFlushOnShutdown asserts that Setup with a
// MetricsEndpoint installs a periodic reader, that recorded counters reach the
// fake OTLP/HTTP collector, and that Shutdown flushes the final export. It uses
// a SHORT MetricsPushInterval so a timely export is observable without waiting
// the SDK default 60s.
func TestSetupOTLPMetricsPushFlushOnShutdown(t *testing.T) {
	coll := newFakeOTLPMetricsCollector(t, 0)

	providers, err := Setup(t.Context(), OTLPConfig{
		ServiceName:         "test",
		MetricsEndpoint:     coll.addr(),
		MetricsProtocol:     ProtocolHTTP,
		MetricsInsecure:     true,
		MetricsPushInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	m, err := NewMetrics(providers.Meter)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	// One run: end_turn with output tokens. recordResult records
	// mecatl.tokens{kind="output"} and mecatl.runs{stop="end_turn"}.
	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 10, OutputTokens: 42},
	}})

	// Shutdown forces a final flush (the load-bearing seam for a short-lived
	// caller). Bound it so a dead collector can't hang the test.
	sdCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if serr := providers.Shutdown(sdCtx); serr != nil {
		t.Fatalf("Shutdown: %v", serr)
	}

	reqs := coll.snapshots()
	if len(reqs) == 0 {
		t.Fatal("fake OTLP collector received no metric exports; Shutdown flush did not fire")
	}

	if got := sumDataPoints(t, reqs, "mecatl.tokens", map[string]string{attrKind: "output", attrRole: RoleMain}); got != 42 {
		t.Errorf("mecatl.tokens{kind=output,role=main} exported = %d, want 42", got)
	}
	if got := sumDataPoints(t, reqs, "mecatl.runs", map[string]string{attrStop: string(session.StopEndTurn), attrRole: RoleMain}); got != 1 {
		t.Errorf("mecatl.runs{stop=end_turn,role=main} exported = %d, want 1", got)
	}
}

// TestSetupOTLPMetricsShutdownBoundedByContext asserts the flush-on-exit seam a
// short-lived caller (mecatequi) relies on: a stalling collector cannot make
// Shutdown exceed the caller's bounded context. The headless mains pass a
// bounded ctx to Shutdown (the --otlp-shutdown-timeout), so a dead collector
// never hangs exit. The per-export MetricsTimeout further bounds each HTTP call,
// but the load-bearing contract is the caller-supplied ctx bound.
func TestSetupOTLPMetricsShutdownBoundedByContext(t *testing.T) {
	coll := newFakeOTLPMetricsCollector(t, 1300*time.Millisecond) // stall past 3x the bound

	providers, err := Setup(t.Context(), OTLPConfig{
		ServiceName:         "test",
		MetricsEndpoint:     coll.addr(),
		MetricsProtocol:     ProtocolHTTP,
		MetricsInsecure:     true,
		MetricsTimeout:      100 * time.Millisecond,
		MetricsPushInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	m, err := NewMetrics(providers.Meter)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop: session.StopEndTurn, Usage: session.Usage{OutputTokens: 1},
	}})

	// A tight caller bound — the shape mecatequi's --otlp-shutdown-timeout gives.
	const bound = 400 * time.Millisecond
	start := time.Now()
	sdCtx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	_ = providers.Shutdown(sdCtx)
	elapsed := time.Since(start)
	// Shutdown must respect the caller's ctx bound (plus small scheduler slack),
	// NOT hang ~ the stall. The caller (mecatequi) then os.Exit's, killing any
	// lingering in-flight export goroutine. Allow a generous 3x slack but stay
	// well under the stall.
	if elapsed > 3*bound {
		t.Errorf("Shutdown against a stalling collector took %v; the %v caller ctx bound should bound it", elapsed, bound)
	}
}

// TestSetupOTLPMetricsBadProtocol asserts an unknown MetricsProtocol is rejected
// (fail-closed), mirroring the trace exporter's bad-protocol guard.
func TestSetupOTLPMetricsBadProtocol(t *testing.T) {
	providers, err := Setup(t.Context(), OTLPConfig{
		ServiceName:     "test",
		MetricsEndpoint: "localhost:4317",
		MetricsProtocol: "carrier-pigeon",
	})
	if err == nil {
		_ = providers.Shutdown(context.Background())
		t.Fatal("Setup with bad metrics protocol: expected error, got nil")
	}
	if providers.Shutdown == nil {
		t.Fatal("error-path shutdown must still be non-nil")
	}
	// The error-path shutdown must be safe to call.
	if serr := providers.Shutdown(t.Context()); serr != nil {
		t.Errorf("error-path shutdown returned error: %v", serr)
	}
}
