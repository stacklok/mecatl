package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// observability carries the telemetry handles run() threads into appConfig and
// serve(), plus the flush-on-SIGTERM Shutdown. With no --otlp-* / --metrics-addr
// flags every HeadlessTelemetryHandles field is zero-valued and Shutdown is a
// no-op (the byte-identical no-telemetry posture). productMetrics carries the
// opt-out product-adoption metrics handles (issue #343 follow-up): Shutdown is
// always non-nil (a no-op when disabled) so the caller can defer it
// unconditionally.
type observability struct {
	cliconfig.HeadlessTelemetryHandles
	productMetrics cliconfig.ProductMetricsHandles
}

// buildObservability wires the OPT-IN OTLP telemetry pipeline for mecak8s via
// the shared cliconfig.HeadlessTelemetry helper (the SAME Setup→NewMetrics→
// WithRole→scoper path mecated wires inline), plus the OPT-OUT product-metrics
// pipeline. With no --otlp-* endpoints the OTLP helper returns zero handles
// (byte-identical default). The caller owns the Shutdown defer (flush on
// SIGTERM). The /metrics loopback listener is wired separately in serve() from
// the returned Registry.
//
// Product metrics: mecak8s is long-running, so heartbeatCtx is the SAME
// signal-driven ctx run() already has in scope (cancelled by its own
// signalCtx()/stop() chain on SIGTERM — no separate cancel function needed)
// and heartbeatInterval is productmetrics.DefaultHeartbeatInterval (the
// steady-state ticker). A build failure (e.g. --product-metrics=true forced on
// with no baked ingest key) is NEVER fatal — product metrics are best-effort —
// so it degrades to a no-op Shutdown rather than failing this function's error
// return (which stays meaningful for the OTLP half only).
//
// The first-run disclosure notice is printed HERE, at setup time — NOT in
// flushTelemetry — because mecak8s is a long-running daemon (unlike
// mecatequi's single-shot process, where setup and flush are seconds apart):
// printing only at shutdown would leave the notice invisible for as long as
// the process runs (potentially days/weeks) and never printed at all on a
// SIGKILL/OOM-kill with no graceful shutdown path. This mirrors
// cmd/mecated/main.go's setupProductMetrics, which prints at setup for the
// same reason.
func buildObservability(ctx context.Context, cfg config, diag port.Diagnostics) (observability, error) {
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

	// Product metrics (opt-out): resolve the effective enabled value via a
	// THROWAWAY resolver mirroring mecated's setupProductMetrics precedent —
	// app.Build's own resolver is internal and never exposed back here, so
	// this narrow read-only resolver re-parses the same operator
	// settings.yaml (an accepted, negligible boot-time cost, same as the
	// other two mains).
	permResolver := permconfig.NewWithEnv(permconfig.Options{
		Conventional:  cfg.permissionsConventional,
		ImportClaude:  cfg.importClaudePermissions,
		ExplicitFiles: []string(cfg.permissionConfigs),
		Diagnostics:   diag,
	}, xdgconfig.OSEnv)
	enabled := cliconfig.ResolveProductMetricsEnabled(cliconfig.ProductMetricsPrecedence{
		FlagSet:         cfg.productMetricsSet,
		FlagValue:       cfg.productMetrics,
		SettingsEnabled: permResolver.OperatorProductMetricsEnabled(),
	})
	// mecak8s cannot use the local-file install-id mechanism the other three
	// binaries share: it runs storage-free with no PVC (ADR 0048), so every
	// pod restart would mint a fresh, never-reused id — the worst-case
	// cardinality pattern for this pipeline. The Helm chart instead provisions
	// ONE stable id per release in a ConfigMap (see
	// deploy/helm/mecak8s/templates/install-id-configmap.yaml) and threads it
	// in through this env var. Empty (the binary run directly, outside the
	// chart) falls back to BuildProductMetrics's own local-file default —
	// still functional, just without the "one stable id per k8s deployment"
	// guarantee the chart provides.
	installIDOverride := os.Getenv("MECATL_PRODUCT_METRICS_INSTALL_ID")
	pm, pmErr := cliconfig.BuildProductMetrics(ctx, ctx, enabled, cfg.productMetricsDryRun,
		productmetrics.BinaryMecak8s, buildinfo.BuildID, productmetrics.DefaultHeartbeatInterval,
		productmetrics.FeatureSnapshot{Mode: productmetrics.ModeK8s}, installIDOverride, diag)
	if pmErr != nil {
		// Mirror the existing telemetry-setup-failure posture: a warning, never
		// a fatal error — product metrics are best-effort and must not block
		// the daemon from starting.
		diag.Log(ctx, port.LevelWarn, "product metrics disabled: setup failed", "err", pmErr)
		pm = cliconfig.ProductMetricsHandles{Shutdown: func(context.Context) error { return nil }}
	}
	if pm.FirstRun {
		// stderr, not diag: mecak8s already writes plain informational lines to
		// stderr elsewhere (e.g. boundedClose's timeout line in main.go), and the
		// disclosure banner is a one-time, human-facing notice rather than a
		// structured operational log line.
		_, _ = fmt.Fprint(os.Stderr, cliconfig.ProductMetricsDisclosureNotice)
	}

	return observability{HeadlessTelemetryHandles: h, productMetrics: pm}, nil
}

// flushTelemetry runs the OTLP + product-metrics Shutdown (flush) with a
// bounded ctx so a dead collector cannot hang SIGTERM shutdown. Safe on a zero
// observability (both Shutdowns are no-ops when telemetry is disabled). A
// flush failure is logged and never aborts — telemetry is best-effort at
// shutdown. (The first-run disclosure notice is printed at buildObservability
// setup time, not here — see that function's doc comment.)
func flushTelemetry(stderr io.Writer, obs observability, timeout time.Duration) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if obs.Shutdown != nil {
		if err := obs.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecak8s: telemetry flush: %v\n", err)
		}
	}
	if obs.productMetrics.Shutdown != nil {
		if err := obs.productMetrics.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecak8s: product metrics flush: %v\n", err)
		}
	}
}

// productMetricsSink fans obs.Sink (the OTLP sink, nil when telemetry is off)
// together with obs.productMetrics.Sink (nil when product metrics are off)
// into one EventSink. telemetry.NewSink's fanOut.Emit calls every wrapped
// sink unconditionally, so a nil element would panic — both are filtered into
// a non-nil-only slice first (mirroring mecated/mecatui's Task 11/12 pattern).
// With both nil this returns nil, reproducing the byte-identical
// no-telemetry Sink posture exactly.
func productMetricsSink(obs observability) port.EventSink {
	var sinks []port.EventSink
	if obs.Sink != nil {
		sinks = append(sinks, obs.Sink)
	}
	if obs.productMetrics.Sink != nil {
		sinks = append(sinks, obs.productMetrics.Sink)
	}
	if len(sinks) == 0 {
		return nil
	}
	return telemetry.NewSink(sinks...)
}

// productMetricsRecorder fans obs.ToolCallRecorder together with
// obs.productMetrics.ToolCallRecorder via cliconfig.TeeToolCallRecorder — but
// ONLY when at least one is non-nil. TeeToolCallRecorder always returns a
// non-nil multiToolCallRecorder interface value even over an all-nil input
// (a typed-nil-slice wrapper, not a nil interface), which would break the
// byte-identical no-telemetry ToolCallRecorder-is-nil posture when both
// sources are off. With both nil this returns nil.
func productMetricsRecorder(obs observability) port.ToolCallRecorder {
	if obs.ToolCallRecorder == nil && obs.productMetrics.ToolCallRecorder == nil {
		return nil
	}
	return cliconfig.TeeToolCallRecorder(obs.ToolCallRecorder, obs.productMetrics.ToolCallRecorder)
}
