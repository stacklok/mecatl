package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// observability carries the telemetry handles realMain threads into appConfig,
// plus the flush-on-exit Shutdown the main owns. With no --otlp-* flags every
// HeadlessTelemetryHandles field is zero-valued and Shutdown is a no-op (the
// byte-identical no-telemetry posture). productMetrics carries the opt-out
// product-adoption metrics handles (issue #343 follow-up): Shutdown is always
// non-nil (a no-op when disabled) so the caller can defer it unconditionally.
type observability struct {
	cliconfig.HeadlessTelemetryHandles
	productMetrics cliconfig.ProductMetricsHandles
}

// buildObservability wires the OPT-IN OTLP telemetry pipeline for mecatequi via
// the shared cliconfig.HeadlessTelemetry helper (the SAME Setup→NewMetrics→
// WithRole→scoper path mecated wires inline), plus the OPT-OUT product-metrics
// pipeline. It keeps appConfig a pure mapping over flags + these handles. With
// no --otlp-* endpoints the OTLP helper returns zero handles, so the
// no-telemetry default is byte-identical. The caller owns the Shutdown defer
// (flush-before-exit).
//
// Product metrics: mecatequi is single-shot/short-lived, so heartbeatCtx is
// context.Background() (nothing to cancel — the process exits right after)
// and heartbeatInterval is 0 (a single immediate fire only, no ticker),
// mirroring mecatequi's own push-before-exit OTLP shape. A build failure (e.g.
// --product-metrics=true forced on with no baked ingest key) is NEVER fatal —
// product metrics are best-effort — so it degrades to a no-op Shutdown rather
// than failing this function's error return (which stays meaningful for the
// OTLP half only).
func buildObservability(ctx context.Context, f flags, diag port.Diagnostics) (observability, error) {
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

	// Product metrics (opt-out): resolve the effective enabled value via a
	// THROWAWAY resolver mirroring mecated's setupProductMetrics /
	// mecatui's setupProductMetrics precedent — app.Build's own resolver is
	// internal and never exposed back here, so this narrow read-only resolver
	// re-parses the same operator settings.yaml (an accepted, negligible
	// boot-time cost, same as the other two mains).
	permResolver := permconfig.NewWithEnv(permconfig.Options{
		Conventional:  true,
		ExplicitFiles: []string(f.permissionConfigs),
		Diagnostics:   diag,
	}, xdgconfig.OSEnv)
	enabled := cliconfig.ResolveProductMetricsEnabled(cliconfig.ProductMetricsPrecedence{
		FlagSet:         f.productMetricsSet,
		FlagValue:       f.productMetrics,
		SettingsEnabled: permResolver.OperatorProductMetricsEnabled(),
	})
	pm, pmErr := cliconfig.BuildProductMetrics(ctx, context.Background(), enabled, f.productMetricsDryRun,
		productmetrics.BinaryMecatequi, buildinfo.BuildID, 0, /* single fire, short-lived */
		productmetrics.FeatureSnapshot{Mode: productmetrics.ModeHeadless},
		"" /* no install-id override: local-file mechanism */, diag)
	if pmErr != nil {
		// Mirror the existing telemetry-setup-failure posture: a warning, never
		// a fatal error — product metrics are best-effort and must not block a
		// CI run.
		diag.Log(ctx, port.LevelWarn, "product metrics disabled: setup failed", "err", pmErr)
		pm = cliconfig.ProductMetricsHandles{Shutdown: func(context.Context) error { return nil }}
	}

	return observability{HeadlessTelemetryHandles: h, productMetrics: pm}, nil
}

// flushTelemetry runs the OTLP + product-metrics Shutdown (flush) with a
// bounded ctx so a dead collector cannot hang the run. It is safe to call on a
// zero observability (both Shutdowns are no-ops when telemetry is disabled). A
// flush failure is logged to stderr and never aborts — telemetry is
// best-effort at exit. The product-metrics first-run disclosure notice is
// printed to stderr here too (mecatequi already writes plain informational
// lines to stderr — see emitAuthFileWarning/verdictLine).
func flushTelemetry(stderr io.Writer, obs observability, timeout time.Duration) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if obs.Shutdown != nil {
		if err := obs.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecatequi: telemetry flush: %v\n", err)
		}
	}
	if obs.productMetrics.Shutdown != nil {
		if err := obs.productMetrics.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecatequi: product metrics flush: %v\n", err)
		}
	}
	if obs.productMetrics.FirstRun {
		_, _ = fmt.Fprint(stderr, cliconfig.ProductMetricsDisclosureNotice)
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
