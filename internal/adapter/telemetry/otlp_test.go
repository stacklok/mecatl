package telemetry

import (
	"context"
	"math"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestResourceInstallationID(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		{name: "omitted when unset"},
		{name: "present when configured", id: id, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := newResource(t.Context(), OTLPConfig{InstallationID: tc.id})
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			value, ok := res.Set().Value(attribute.Key("mecatl.installation.id"))
			if ok != tc.want {
				t.Fatalf("installation attribute present = %t, want %t", ok, tc.want)
			}
			if ok && value.AsString() != id {
				t.Fatalf("installation attribute = %q, want %q", value.AsString(), id)
			}
		})
	}
}

func TestSetupDisabledWhenEndpointEmpty(t *testing.T) {
	// A no-op provider is installed so we can assert Setup does not replace the
	// TRACER provider when tracing is disabled. Metrics are always on, so a real
	// Meter/Registry is still returned.
	otel.SetTracerProvider(noop.NewTracerProvider())
	before := otel.GetTracerProvider()

	providers, err := Setup(t.Context(), OTLPConfig{})
	if err != nil {
		t.Fatalf("Setup with empty endpoint: unexpected error: %v", err)
	}
	if providers.Shutdown == nil {
		t.Fatal("Setup returned a nil shutdown func")
	}
	if got := otel.GetTracerProvider(); got != before {
		t.Errorf("Setup installed a tracer provider when disabled: %T", got)
	}
	if providers.Meter == nil || providers.Registry == nil {
		t.Error("Setup returned nil Meter/Registry; metrics should always be on")
	}
	if err := providers.Shutdown(t.Context()); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}
}

func TestSetupBadProtocol(t *testing.T) {
	providers, err := Setup(t.Context(), OTLPConfig{
		Endpoint: "localhost:4317",
		Protocol: "carrier-pigeon",
	})
	if err == nil {
		t.Fatal("Setup with bad protocol: expected error, got nil")
	}
	if providers.Shutdown == nil {
		t.Fatal("Setup returned a nil shutdown func on error")
	}
	// The error-path shutdown must still be safe to call.
	if err := providers.Shutdown(t.Context()); err != nil {
		t.Errorf("error-path shutdown returned error: %v", err)
	}
}

func TestSetupGRPCDoesNotDial(t *testing.T) {
	// A bogus endpoint with Insecure must construct without blocking, because
	// the OTLP/gRPC exporter dials lazily on first export. Run under a deadline:
	// if Setup blocked on a dial, the context would expire and the test fail.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	type result struct {
		providers Providers
		err       error
	}
	done := make(chan result, 1)
	go func() {
		p, err := Setup(ctx, OTLPConfig{
			Endpoint: "127.0.0.1:1", // nothing listening here
			Protocol: ProtocolGRPC,
			Insecure: true,
			Timeout:  100 * time.Millisecond,
		})
		done <- result{p, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Setup against unreachable endpoint errored: %v", res.err)
		}
		if res.providers.Shutdown == nil {
			t.Fatal("Setup returned a nil shutdown func")
		}
		if _, ok := otel.GetTracerProvider().(*noop.TracerProvider); ok {
			t.Error("Setup did not install a real TracerProvider")
		}
		// Shutdown should return promptly and not panic. Any error (e.g. a
		// flush failing because nothing is listening) is acceptable here; we
		// only assert it does not hang or panic.
		sdCtx, sdCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer sdCancel()
		_ = res.providers.Shutdown(sdCtx)
	case <-time.After(2 * time.Second):
		t.Fatal("Setup blocked: gRPC exporter should construct without dialing")
	}
}

func TestSamplerNeverNil(t *testing.T) {
	for _, ratio := range []float64{-1, 0, 0.25, 1, 2} {
		if s := sampler(ratio); s == nil {
			t.Errorf("sampler(%v) returned nil", ratio)
		}
	}
}

// TestNewMeterProviderEmitsClassicLatencyBuckets wires the REAL metrics pipeline —
// newMeterProvider (the same prometheus-exporter reader + LatencyViews() Setup
// installs) — records a spread of turn durations through the real meter, then
// Gathers the prometheus registry directly. It asserts the gathered
// mecatl_turn_duration_seconds histogram family carries MULTIPLE explicit le=
// buckets with at least one FINITE (non-+Inf) bound — the issue #158 fix (ADR 0045):
// the classic text exposition now yields real le= ladders, so quantiles are
// obtainable with zero scrape config. The superseded base-2 exponential view
// (ADR 0018 §5) would have rendered a single le="+Inf" bucket here.
func TestNewMeterProviderEmitsClassicLatencyBuckets(t *testing.T) {
	res, err := newResource(t.Context(), OTLPConfig{})
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	mp, reg, err := newMeterProvider(context.Background(), res, OTLPConfig{})
	if err != nil {
		t.Fatalf("newMeterProvider: %v", err)
	}
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	turn, err := mp.Meter(meterName).Float64Histogram(
		turnDurationInstrument, otelmetric.WithUnit("s"))
	if err != nil {
		t.Fatalf("Float64Histogram: %v", err)
	}
	// A spread that lands in distinct buckets across the ladder: 5ms, 80ms, 1.5s,
	// 45s. The 45s value exercises the minute-scale high end (the slow-reasoning
	// case the boundaries were widened for).
	for _, v := range []float64{0.005, 0.08, 1.5, 45} {
		turn.Record(context.Background(), v)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var fam *dto.MetricFamily
	for _, f := range families {
		if f.GetName() == "mecatl_turn_duration_seconds" {
			fam = f
			break
		}
	}
	if fam == nil {
		t.Fatalf("gathered families missing mecatl_turn_duration_seconds (got %d families)", len(families))
	}
	if fam.GetType() != dto.MetricType_HISTOGRAM {
		t.Fatalf("turn_duration family type = %v, want HISTOGRAM", fam.GetType())
	}
	if len(fam.GetMetric()) != 1 {
		t.Fatalf("turn_duration series = %d, want 1", len(fam.GetMetric()))
	}
	h := fam.GetMetric()[0].GetHistogram()
	if h == nil {
		t.Fatal("turn_duration metric carries no histogram")
	}
	if h.GetSampleCount() != 4 {
		t.Errorf("sample count = %d, want 4", h.GetSampleCount())
	}
	// The explicit ladder must produce many le= buckets (one per boundary) — NOT
	// the single +Inf bucket the exponential view collapsed to. At least one must
	// carry a FINITE upper bound; that is what makes quantiles obtainable.
	buckets := h.GetBucket()
	if len(buckets) <= 1 {
		t.Fatalf("turn_duration le= buckets = %d, want >1 (real ladder; a single bucket is the +Inf-only regression)", len(buckets))
	}
	var sawFinite bool
	for _, b := range buckets {
		if !math.IsInf(b.GetUpperBound(), 1) {
			sawFinite = true
			break
		}
	}
	if !sawFinite {
		t.Errorf("turn_duration buckets carry no finite upper bound; only le=+Inf — quantiles unobtainable (issue #158 regressed)")
	}
}
