package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

// Protocol selects the OTLP transport used by the exporter.
const (
	// ProtocolGRPC exports spans over OTLP/gRPC (the default).
	ProtocolGRPC = "grpc"
	// ProtocolHTTP exports spans over OTLP/HTTP (protobuf).
	ProtocolHTTP = "http"
)

// defaultServiceName is the resource service.name used when OTLPConfig leaves
// ServiceName empty.
const defaultServiceName = "mecatl"

// OTLPConfig configures the OTLP trace exporter and the SDK TracerProvider that
// Setup installs. A zero Endpoint disables tracing entirely.
type OTLPConfig struct {
	// Endpoint is the collector address, e.g. "localhost:4317" for gRPC or a
	// URL/host for HTTP. An empty Endpoint disables tracing: Setup installs
	// nothing and returns a no-op shutdown.
	Endpoint string
	// Protocol selects the transport: "grpc" (default) or "http". Any other
	// value is rejected by Setup.
	Protocol string
	// Insecure skips TLS when dialing the collector (development only).
	Insecure bool
	// ServiceName sets the resource service.name attribute. Defaults to
	// "mecatl" when empty.
	ServiceName string
	// Headers are sent with every export request (e.g. auth headers).
	Headers map[string]string
	// Timeout bounds a single export request. Zero uses the exporter default.
	Timeout time.Duration
	// SampleRatio is the parent-based head-sampling ratio in [0,1]. Values <= 0
	// select always-on sampling (the default); values >= 1 also sample every
	// trace. Non-root spans follow their parent's sampling decision.
	SampleRatio float64
	// Version sets the resource service.version attribute when non-empty.
	Version string
	// InstallationID sets the optional mecatl.installation.id resource attribute.
	InstallationID string

	// --- OTLP metrics push (optional; the prometheus reader stays always on) ---
	// MetricsEndpoint is the collector address for an OTLP METRICS push reader
	// (a PeriodicReader over an otlpmetricgrpc/otlpmetrichttp exporter). An empty
	// MetricsEndpoint installs NO periodic reader — the scrape-only path is
	// byte-identical. When set, the periodic reader joins the prometheus reader
	// on the SAME MeterProvider so /metrics AND the push both export the domain
	// instruments.
	MetricsEndpoint string
	// MetricsProtocol selects the metrics transport: "grpc" (default) or "http".
	// Any other value is rejected by Setup.
	MetricsProtocol string
	// MetricsInsecure skips TLS when dialing the metrics collector (dev only).
	MetricsInsecure bool
	// MetricsHeaders are sent with every metrics export request.
	MetricsHeaders map[string]string
	// MetricsTimeout bounds a single metrics export request. Zero uses the
	// exporter default.
	MetricsTimeout time.Duration
	// MetricsPushInterval is the PeriodicReader export cadence. Zero uses the
	// SDK default (60s). A short-lived caller (mecatequi) should set a small
	// interval AND call Shutdown to force a final flush before exit.
	MetricsPushInterval time.Duration
}

// Providers bundles the OTel providers Setup installs, so callers wire metrics
// and traces from one place instead of a positional return list.
type Providers struct {
	// Tracer is the TracerProvider (a no-op provider when tracing is disabled).
	// It is also installed globally via otel.SetTracerProvider, so
	// NewTracing(otel.GetTracerProvider()) picks it up.
	Tracer trace.TracerProvider
	// Meter is the MeterProvider feeding the domain instruments. Pass it to
	// NewMetrics. It is always a real SDK provider (metrics are always on, even
	// when OTLP tracing is disabled) so /metrics has data to serve.
	Meter metric.MeterProvider
	// Registry is the prometheus registry the metrics exporter registers on.
	// Serve it via MetricsHandler at /metrics.
	Registry *prometheus.Registry
	// Shutdown flushes and stops BOTH providers. Always non-nil.
	Shutdown func(context.Context) error
}

// Setup builds the OTel metrics and (optionally) tracing pipelines.
//
// Metrics are ALWAYS installed: a prometheus-exporter reader registers the
// domain instruments on a fresh prometheus.Registry (returned for /metrics), the
// latency histograms are configured as explicit-bucket histograms via metric.Views
// (ADR 0045 — so the classic text exposition carries real le= buckets / quantiles),
// and the runtime collector (go.goroutine.count, GC, heap, …) is started against
// the MeterProvider. The MeterProvider is NOT installed globally — it is returned
// in Providers.Meter for explicit injection into NewMetrics.
//
// Tracing is installed only when cfg.Endpoint is non-empty: Setup builds an OTLP
// span exporter and an SDK TracerProvider with a batch span processor, a
// parent-based sampler, and a resource carrying service.name and (optionally)
// service.version. It installs the provider via otel.SetTracerProvider and a W3C
// TraceContext propagator via otel.SetTextMapPropagator. When cfg.Endpoint is
// empty, tracing is disabled and Providers.Tracer is a no-op provider.
//
// The gRPC trace exporter is constructed lazily and does not dial the collector
// until the first export, so Setup returns promptly even against an unreachable
// endpoint.
//
// An OTLP metric exporter (push to a collector) is an optional seam: when
// cfg.MetricsEndpoint is set, Setup attaches a PeriodicReader over an
// otlpmetricgrpc/otlpmetrichttp exporter to the MeterProvider's reader slice
// alongside the always-on prometheus reader. The reader slice assembly (below in
// newMeterProvider) keeps both exporters exporting the SAME instruments.
func Setup(ctx context.Context, cfg OTLPConfig) (Providers, error) {
	noop := func(context.Context) error { return nil }
	providers := Providers{
		Tracer:   otel.GetTracerProvider(), // current global; a no-op until tracing is installed.
		Shutdown: noop,
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return providers, fmt.Errorf("telemetry: build resource: %w", err)
	}

	// --- Metrics (always on) ---
	mp, reg, err := newMeterProvider(ctx, res, cfg)
	if err != nil {
		return providers, fmt.Errorf("telemetry: build meter provider: %w", err)
	}
	providers.Meter = mp
	providers.Registry = reg

	// Runtime collector: goroutines, GC, heap, allocations against this provider.
	if rerr := otelruntime.Start(otelruntime.WithMeterProvider(mp)); rerr != nil {
		_ = mp.Shutdown(ctx)
		return providers, fmt.Errorf("telemetry: start runtime collector: %w", rerr)
	}

	shutdowns := []func(context.Context) error{mp.Shutdown}

	// --- Tracing (optional) ---
	if cfg.Endpoint != "" {
		exporter, eerr := newExporter(ctx, cfg)
		if eerr != nil {
			_ = mp.Shutdown(ctx)
			return providers, fmt.Errorf("telemetry: build OTLP exporter: %w", eerr)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler(cfg.SampleRatio)),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		providers.Tracer = tp
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	providers.Shutdown = func(sctx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			if serr := fn(sctx); serr != nil {
				errs = append(errs, serr)
			}
		}
		return errors.Join(errs...)
	}

	return providers, nil
}

// newMeterProvider builds the SDK MeterProvider with a prometheus-exporter reader
// (registered on a fresh registry) and the explicit-bucket-histogram views for
// every latency instrument (ADR 0045). It returns the provider and the registry
// to serve at /metrics.
//
// When cfg.MetricsEndpoint is set, an OTLP metrics push reader (a PeriodicReader
// over an otlpmetricgrpc/otlpmetrichttp exporter) joins the prometheus reader on
// the SAME MeterProvider, so /metrics AND the push both export the domain
// instruments. The prometheus reader is ALWAYS installed (the scrape path stays
// on even when a collector is configured). The returned shutdown (via the
// MeterProvider's Shutdown) flushes + stops the periodic reader too, so a
// short-lived caller can force a final export before exit.
func newMeterProvider(ctx context.Context, res *resource.Resource, cfg OTLPConfig) (*sdkmetric.MeterProvider, *prometheus.Registry, error) {
	reg := prometheus.NewRegistry()
	promExporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, nil, fmt.Errorf("build prometheus exporter: %w", err)
	}

	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExporter),
	}
	if cfg.MetricsEndpoint != "" {
		reader, rerr := newMetricPushReader(ctx, cfg)
		if rerr != nil {
			return nil, nil, fmt.Errorf("build OTLP metric reader: %w", rerr)
		}
		opts = append(opts, sdkmetric.WithReader(reader))
	}
	for _, v := range LatencyViews() {
		opts = append(opts, sdkmetric.WithView(v))
	}
	mp := sdkmetric.NewMeterProvider(opts...)
	return mp, reg, nil
}

// newMetricPushReader builds a PeriodicReader over an otlpmetricgrpc or
// otlpmetrichttp exporter for the OTLP metrics push path. The exporter is
// constructed lazily for gRPC (it dials on first export, like the trace
// exporter), so Setup returns promptly even against an unreachable endpoint.
func newMetricPushReader(ctx context.Context, cfg OTLPConfig) (*sdkmetric.PeriodicReader, error) {
	periodicOpts := make([]sdkmetric.PeriodicReaderOption, 0, 1)
	if cfg.MetricsPushInterval > 0 {
		periodicOpts = append(periodicOpts, sdkmetric.WithInterval(cfg.MetricsPushInterval))
	}
	switch cfg.MetricsProtocol {
	case "", ProtocolGRPC:
		gOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.MetricsEndpoint)}
		if cfg.MetricsInsecure {
			gOpts = append(gOpts, otlpmetricgrpc.WithInsecure())
		}
		if len(cfg.MetricsHeaders) > 0 {
			gOpts = append(gOpts, otlpmetricgrpc.WithHeaders(cfg.MetricsHeaders))
		}
		if cfg.MetricsTimeout > 0 {
			gOpts = append(gOpts, otlpmetricgrpc.WithTimeout(cfg.MetricsTimeout))
		}
		exp, eerr := otlpmetricgrpc.New(ctx, gOpts...)
		if eerr != nil {
			return nil, fmt.Errorf("build otlpmetricgrpc exporter: %w", eerr)
		}
		return sdkmetric.NewPeriodicReader(exp, periodicOpts...), nil
	case ProtocolHTTP:
		hOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.MetricsEndpoint)}
		if cfg.MetricsInsecure {
			hOpts = append(hOpts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.MetricsHeaders) > 0 {
			hOpts = append(hOpts, otlpmetrichttp.WithHeaders(cfg.MetricsHeaders))
		}
		if cfg.MetricsTimeout > 0 {
			hOpts = append(hOpts, otlpmetrichttp.WithTimeout(cfg.MetricsTimeout))
		}
		exp, eerr := otlpmetrichttp.New(ctx, hOpts...)
		if eerr != nil {
			return nil, fmt.Errorf("build otlpmetrichttp exporter: %w", eerr)
		}
		return sdkmetric.NewPeriodicReader(exp, periodicOpts...), nil
	default:
		return nil, fmt.Errorf("unknown OTLP metrics protocol %q (want %q or %q)",
			cfg.MetricsProtocol, ProtocolGRPC, ProtocolHTTP)
	}
}

// MetricsHandler returns an http.Handler that serves the given registry in the
// Prometheus text exposition format, suitable for mounting at /metrics. It is a
// thin wrapper over promhttp that keeps the handler construction (and the
// promhttp.HandlerOpts choice) in one place; the composition root still names
// *prometheus.Registry to wire the handler, which architecture.md §2 permits.
func MetricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// newExporter constructs the OTLP span exporter for the configured protocol.
func newExporter(ctx context.Context, cfg OTLPConfig) (*otlptrace.Exporter, error) {
	switch cfg.Protocol {
	case "", ProtocolGRPC:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
		}
		if cfg.Timeout > 0 {
			opts = append(opts, otlptracegrpc.WithTimeout(cfg.Timeout))
		}
		return otlptracegrpc.New(ctx, opts...)
	case ProtocolHTTP:
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		if cfg.Timeout > 0 {
			opts = append(opts, otlptracehttp.WithTimeout(cfg.Timeout))
		}
		return otlptracehttp.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("unknown OTLP protocol %q (want %q or %q)",
			cfg.Protocol, ProtocolGRPC, ProtocolHTTP)
	}
}

// newResource builds the OTel resource describing this service.
func newResource(ctx context.Context, cfg OTLPConfig) (*resource.Resource, error) {
	name := cfg.ServiceName
	if name == "" {
		name = defaultServiceName
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(name)}
	if cfg.Version != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.Version))
	}
	if cfg.InstallationID != "" {
		attrs = append(attrs, attribute.String("mecatl.installation.id", cfg.InstallationID))
	}
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
	)
}

// sampler returns a parent-based sampler. A ratio <= 0 selects always-on; a
// ratio >= 1 also samples every trace; values in between select a trace-ID-ratio
// root sampler.
func sampler(ratio float64) sdktrace.Sampler {
	switch {
	case ratio <= 0, ratio >= 1:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}
