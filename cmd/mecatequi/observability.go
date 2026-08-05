package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// observability carries the telemetry handles realMain threads into appConfig,
// plus the flush-on-exit Shutdown the main owns. With no --otlp-* flags every
// field is zero-valued and Shutdown is a no-op (the byte-identical no-telemetry
// posture).
type observability struct {
	cliconfig.HeadlessTelemetryHandles
}

// buildObservability wires the OPT-IN OTLP telemetry pipeline for mecatequi via
// the shared cliconfig.HeadlessTelemetry helper (the SAME Setup→NewMetrics→
// WithRole→scoper path mecated wires inline). It keeps appConfig a pure mapping
// over flags + these handles. With no --otlp-* endpoints the helper returns zero
// handles, so the no-telemetry default is byte-identical. The caller owns the
// Shutdown defer (flush-before-exit).
func buildObservability(ctx context.Context, f flags) (observability, error) {
	h, err := cliconfig.HeadlessTelemetry(ctx, cliconfig.HeadlessTelemetryConfig{
		ServiceName:         "mecatequi",
		OTLPTraceEndpoint:   f.otlpEndpoint,
		OTLPTraceProtocol:   f.otlpProtocol,
		OTLPTraceInsecure:   f.otlpInsecure,
		OTLPMetricsEndpoint: f.otlpMetricsEndpoint,
		OTLPMetricsProtocol: f.otlpMetricsProtocol,
		OTLPMetricsInsecure: f.otlpInsecure,
	})
	if err != nil {
		return observability{}, err
	}
	return observability{HeadlessTelemetryHandles: h}, nil
}

// flushTelemetry runs the telemetry Shutdown (flush) with a bounded ctx so a
// dead collector cannot hang the run. It is safe to call on a zero observability
// (Shutdown is a no-op when telemetry is disabled). A flush failure is logged
// to stderr and never aborts — telemetry is best-effort at exit.
func flushTelemetry(stderr io.Writer, obs observability, timeout time.Duration) {
	if obs.Shutdown == nil {
		return
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := obs.Shutdown(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: telemetry flush: %v\n", err)
	}
}
