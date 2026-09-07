// Package cliconfig: headless telemetry helpers.
//
// HeadlessTelemetry builds the SAME observability path cmd/mecated wires inline
// (telemetry.Setup → NewMetrics → metrics.WithRole(RoleMain) + telemetry.NewTracing
// → telemetry.NewSink → the role-scoper closure for children) but for the
// headless mains (mecatequi, mecak8s) — extracted here so the three mains cannot
// drift on the Setup→sink→scoper shape (Rule of Three). It is OPT-IN: a caller
// that passes no endpoints gets zero handles (the byte-identical no-telemetry
// posture), so a headless main with no --otlp-* flags stays exactly as it was.
//
// It lives in internal/cliconfig (NOT internal/app): composition stays
// telemetry-import-free, and the cmd mains already route flag wiring through
// this package. The handles are returned to the main, which owns the
// Shutdown defer (flush-before-exit) so it unwinds on the process's exit path,
// not here.

package cliconfig

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
)

// HeadlessTelemetryConfig carries the OTLP endpoint knobs the headless mains
// expose on their flags. A zero value (every Endpoint empty) produces zero
// handles — the byte-identical no-telemetry posture.
type HeadlessTelemetryConfig struct {
	// ServiceName sets the resource service.name attribute. Defaults to "mecatl"
	// when empty (matches telemetry.Setup).
	ServiceName string

	// OTLPTraceEndpoint enables OTLP TRACE push when non-empty. Mirrors mecated's
	// --otlp-endpoint. Empty disables tracing.
	OTLPTraceEndpoint string
	// OTLPTraceProtocol selects the trace transport: "grpc" (default) or "http".
	OTLPTraceProtocol string
	// OTLPTraceInsecure skips TLS when dialing the trace collector (dev only).
	OTLPTraceInsecure bool

	// OTLPMetricsEndpoint enables OTLP METRICS push when non-empty. The prometheus
	// reader stays always on regardless (see telemetry.Setup). Empty installs no
	// periodic reader.
	OTLPMetricsEndpoint string
	// OTLPMetricsProtocol selects the metrics transport: "grpc" (default) or "http".
	OTLPMetricsProtocol string
	// OTLPMetricsInsecure skips TLS when dialing the metrics collector (dev only).
	OTLPMetricsInsecure bool

	// Scrape, when true, builds the pipeline (Setup + Metrics + the role="main"
	// EventSink + the child scoper) even when no OTLP endpoint is set — the
	// scrape-only deployment (mecak8s --metrics-addr with no --otlp-*). The
	// prometheus reader is always on, so the returned Registry serves /metrics.
	// Tracing stays a no-op when no trace endpoint is set. False (the default)
	// keeps the byte-identical no-op posture when no endpoints are configured.
	Scrape bool
}

// HeadlessTelemetryHandles bundles the handles HeadlessTelemetry returns. A
// caller threads Sink/ToolCallRecorder/MetricsRoleScoper into app.Config and
// owns the Shutdown defer (a bounded ctx so a dead collector cannot hang exit).
// Every field is zero-valued when telemetry is disabled, so a caller can pass
// them straight into app.Config without nil-checking (nil Sink/ToolCallRecorder/
// MetricsRoleScoper is the byte-identical no-metrics path).
type HeadlessTelemetryHandles struct {
	// Shutdown flushes + stops the providers (metrics periodic reader + trace
	// batch processor). Always non-nil (a no-op when telemetry is disabled), so a
	// caller can defer it unconditionally.
	Shutdown func(context.Context) error
	// Registry is the prometheus registry the metrics exporter registers on;
	// serve it via telemetry.NewAdminMux at /metrics (mecak8s). Nil when telemetry
	// is disabled.
	Registry *prometheus.Registry
	// Metrics is the domain metrics adapter; nil when disabled.
	Metrics *telemetry.Metrics
	// Sink is the fanned-out EventSink (metrics + tracing) tagged role="main";
	// nil when disabled.
	Sink port.EventSink
	// ToolCallRecorder is the role="main" tool-call recorder; nil when disabled.
	ToolCallRecorder port.ToolCallRecorder
	// MetricsRoleScoper is the closure handing each CHILD engine a role-scoped
	// (EventSink, ToolCallRecorder) pair keyed on the BOUNDED family label
	// internal/app's roleFamily already resolved; nil when disabled (children
	// unmetered, byte-identical).
	MetricsRoleScoper func(familyRole string) (port.EventSink, port.ToolCallRecorder)
	// SessionLoadFailureMetricsEmitter records ownership-concealed load failures
	// with one closed class label. Nil when telemetry is disabled.
	SessionLoadFailureMetricsEmitter func(port.SessionLoadFailureClass)
}

// HeadlessTelemetry builds the OTel metrics + (optional) tracing pipeline for a
// headless main and returns the handles to thread into app.Config. When both the
// trace and metrics endpoints are empty it returns zero handles and a no-op
// Shutdown — the byte-identical no-telemetry posture, so a headless main with no
// --otlp-* flags is unchanged.
//
// The caller owns the Shutdown defer; mecatequi registers it BEFORE built.Close()
// so the flush runs first (defers are LIFO), and mecak8s registers it so the
// SIGTERM path flushes OTLP before the listener stops.
func HeadlessTelemetry(ctx context.Context, cfg HeadlessTelemetryConfig) (HeadlessTelemetryHandles, error) {
	// The no-op posture: no OTLP endpoints AND not scrape → no pipeline, no sink,
	// no scoper. Scrape-only (Scrape=true with no OTLP) still builds the pipeline.
	if cfg.OTLPTraceEndpoint == "" && cfg.OTLPMetricsEndpoint == "" && !cfg.Scrape {
		return HeadlessTelemetryHandles{
			Shutdown: func(context.Context) error { return nil },
		}, nil
	}

	providers, err := telemetry.Setup(ctx, telemetry.OTLPConfig{
		Endpoint:        cfg.OTLPTraceEndpoint,
		Protocol:        cfg.OTLPTraceProtocol,
		Insecure:        cfg.OTLPTraceInsecure,
		ServiceName:     cfg.ServiceName,
		MetricsEndpoint: cfg.OTLPMetricsEndpoint,
		MetricsProtocol: cfg.OTLPMetricsProtocol,
		MetricsInsecure: cfg.OTLPMetricsInsecure,
	})
	if err != nil {
		return HeadlessTelemetryHandles{
			Shutdown: func(context.Context) error { return nil },
		}, fmt.Errorf("headless telemetry: setup: %w", err)
	}

	metrics, err := telemetry.NewMetrics(providers.Meter)
	if err != nil {
		_ = providers.Shutdown(ctx)
		return HeadlessTelemetryHandles{
			Shutdown: func(context.Context) error { return nil },
		}, fmt.Errorf("headless telemetry: metrics: %w", err)
	}

	// Tracing rides the globally-installed TracerProvider (telemetry.Setup installs
	// it via otel.SetTracerProvider when OTLPTraceEndpoint is set; a no-op when
	// tracing is disabled). This mirrors cmd/mecated's wiring.
	tracing := telemetry.NewTracing(otel.GetTracerProvider())

	// Role-scoped main pair (issue #47): the MAIN engine records through the
	// role="main" view so EVERY series carries the role label uniformly; children
	// get their own bounded-family views via the scoper below.
	mainScoped := metrics.WithRole(telemetry.RoleMain)
	sink := telemetry.NewSink(mainScoped, tracing)

	// Child role scoper (issue #47): hands each CHILD engine a role-scoped
	// (EventSink, ToolCallRecorder) pair keyed on the BOUNDED family label
	// internal/app's roleFamily already resolved. Mirrors cmd/mecated's closure
	// minus the perf-MCP slow-turn buffer (a headless main does not mount the
	// perf MCP server).
	roleScoper := func(familyRole string) (port.EventSink, port.ToolCallRecorder) {
		scoped := metrics.WithRole(familyRole)
		return telemetry.NewSink(scoped), scoped
	}

	return HeadlessTelemetryHandles{
		Shutdown:                         providers.Shutdown,
		Registry:                         providers.Registry,
		Metrics:                          metrics,
		Sink:                             sink,
		ToolCallRecorder:                 mainScoped,
		MetricsRoleScoper:                roleScoper,
		SessionLoadFailureMetricsEmitter: metrics.EmitSessionLoadFailure,
	}, nil
}
