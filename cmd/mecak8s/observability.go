package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// observability carries the telemetry handles run() threads into appConfig and
// serve(), plus the flush-on-SIGTERM Shutdown. With no --otlp-* / --metrics-addr
// flags every field is zero-valued and Shutdown is a no-op (the byte-identical
// no-telemetry posture).
type observability struct {
	cliconfig.HeadlessTelemetryHandles
}

// buildObservability wires the OPT-IN OTLP telemetry pipeline for mecak8s via
// the shared cliconfig.HeadlessTelemetry helper (the SAME Setup→NewMetrics→
// WithRole→scoper path mecated wires inline). With no --otlp-* endpoints the
// helper returns zero handles (byte-identical default). The caller owns the
// Shutdown defer (flush on SIGTERM). The /metrics loopback listener is wired
// separately in serve() from the returned Registry.
func buildObservability(ctx context.Context, cfg config) (observability, error) {
	h, err := cliconfig.HeadlessTelemetry(ctx, cliconfig.HeadlessTelemetryConfig{
		ServiceName:         "mecak8s",
		InstallationID:      cfg.installationID,
		OTLPTraceEndpoint:   cfg.otlpEndpoint,
		OTLPTraceProtocol:   cfg.otlpProtocol,
		OTLPTraceInsecure:   cfg.otlpInsecure,
		OTLPMetricsEndpoint: cfg.otlpMetricsEndpoint,
		OTLPMetricsProtocol: cfg.otlpMetricsProtocol,
		OTLPMetricsInsecure: cfg.otlpInsecure,
		// Scrape-only (--metrics-addr with no --otlp-*): build the pipeline so the
		// Registry serves /metrics. The prometheus reader is always on.
		Scrape: cfg.metricsAddr != "",
	})
	if err != nil {
		return observability{}, err
	}
	return observability{HeadlessTelemetryHandles: h}, nil
}

// flushTelemetry runs the telemetry Shutdown (flush) with a bounded ctx so a
// dead collector cannot hang SIGTERM shutdown. Safe on a zero observability
// (Shutdown is a no-op when telemetry is disabled). A flush failure is logged
// and never aborts — telemetry is best-effort at shutdown.
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
		_, _ = fmt.Fprintf(stderr, "mecak8s: telemetry flush: %v\n", err)
	}
}
