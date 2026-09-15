package productmetrics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// endpoint and headerKeyName are the ONE destination this pipeline can ever
// send to (stacklok/infra#5604): a dedicated, internet-facing OTLP/HTTP
// ingest at mecatl.metrics.stacklok.com, gated by a single shared key baked into
// the binary. Neither is operator-configurable — an operator's own
// --otlp-endpoint has zero effect on this path, and this path has zero
// effect on the operator's own OTLP/Prometheus pipeline (a completely
// separate MeterProvider, never installed as global). endpoint is a var
// (not a const) so tests can point it at an httptest server.
//
// endpoint is passed VERBATIM to otlpmetrichttp.WithEndpointURL, which sets
// the exporter's URL path to exactly endpoint's own path — an empty path is
// treated as the literal root "/", NOT as "use the exporter's documented
// /v1/metrics default" (see WithEndpointURL's own doc comment in
// go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp's
// internal/oconf package). endpoint therefore MUST spell out "/v1/metrics"
// itself. This is deliberately NOT toolhive-core's otlp.NewMetricReader,
// whose createMetricExporter instead SPLITS a supplied endpoint into
// host+basePath and, when a basePath is present, APPENDS its own
// "/v1/metrics" suffix onto it — so a base URL that already ends in
// "/v1/metrics" would there produce "/v1/metrics/v1/metrics" against the
// real collector. Building the exporter directly over otlpmetrichttp (as
// here) sidesteps that concatenation entirely: the path below is sent
// exactly as written, once.
var (
	endpoint      = "https://mecatl.metrics.stacklok.com/v1/metrics"
	headerKeyName = "x-mecatl-metrics-key"
)

// bakedKey is the shared ingest key baked into the binary at build time via
// `-X github.com/stacklok/mecatl/internal/adapter/productmetrics.bakedKey=…`
// (see Taskfile.yml's BUILD_LDFLAGS). An empty key — every local/dev/CI-test
// build that does not set the ldflag — disables the pipeline entirely:
// NewProvider refuses to construct, so a non-release build can never
// accidentally phone home with an invalid or absent key.
var bakedKey = ""

// exportInterval is the cadence at which the PeriodicReader flushes collected
// metrics to the ingest endpoint. The OTel SDK's PeriodicReader defaults to
// 60s (sdkmetric's own defaultInterval); this product-metrics pipeline has no
// need for minute-granularity export, so it overrides that default to 30m
// (via sdkmetric.WithInterval below) to cut export traffic to the vendor
// destination by ~30x.
const exportInterval = 30 * time.Minute

// Available reports whether this build has an ingest key baked in — i.e.
// whether NewProvider can ever construct a real pipeline. Composition MUST
// check this BEFORE minting/persisting any local install-id state (issue:
// disclosure/first-run ordering): a keyless build (every local/dev/CI-test
// build) that instead created the install-id file first, then failed here,
// would leave that file behind — so a LATER release build's genuine first
// export would read it back and report firstRun=false, silently skipping
// the disclosure notice ADR 0338 requires before that first export.
func Available() bool { return bakedKey != "" }

// SetBakedKeyForTest overrides bakedKey for the duration of a test and
// returns a restore func the caller must defer. bakedKey is normally set
// exactly once, at build time, via the `-X …productmetrics.bakedKey=…`
// ldflag (see BUILD_LDFLAGS in Taskfile.yml); every other package's tests
// need a way to exercise the Available()==true path (e.g. cliconfig's
// disclosure-ordering tests) without actually shipping a real key, hence
// this seam — mirroring the ForTest helpers elsewhere in internal/adapter
// (e.g. scheduler.RunOnceForTest).
func SetBakedKeyForTest(key string) (restore func()) {
	orig := bakedKey
	bakedKey = key
	return func() { bakedKey = orig }
}

// SetEndpointForTest overrides endpoint for the duration of a test and
// returns a restore func the caller must defer — the cross-package sibling
// of SetBakedKeyForTest, so a caller in another package (e.g. cliconfig's
// disclosure-ordering tests) that also needs Available()==true can point
// the exporter at an httptest server instead of the real production
// ingest.
func SetEndpointForTest(url string) (restore func()) {
	orig := endpoint
	endpoint = url
	return func() { endpoint = orig }
}

// allowedResourceAttrs is the CLOSED set of resource attribute keys this
// pipeline may ever export, enforced by allowlistExporter below regardless
// of what the OTel SDK itself adds to a MeterProvider's resource.
var allowedResourceAttrs = map[string]bool{
	"service.name":      true,
	"service.version":   true,
	"mecatl.install.id": true,
	"mecatl.binary":     true,
}

// allowlistExporter wraps a sdkmetric.Exporter and rewrites every exported
// ResourceMetrics.Resource down to allowedResourceAttrs immediately before
// serialization/transmission. This is the LAST line of defense against
// undeclared resource attributes reaching this vendor pipeline: the OTel SDK
// itself is not a clean pass-through here — metric.WithResource
// unconditionally merges the resource it is given with
// resource.Environment() (i.e. the ambient OTEL_RESOURCE_ATTRIBUTES env var)
// inside go.opentelemetry.io/otel/sdk/metric's own WithResource option, with
// no application-facing way to opt out. An operator's own
// OTEL_RESOURCE_ATTRIBUTES (meant for their OTLP/Prometheus pipeline) must
// never reach the Stacklok product-metrics destination, so filtering at
// export time — after the SDK's merge has already happened — is the only
// point that can enforce the closed catalog.
type allowlistExporter struct {
	next sdkmetric.Exporter
}

func (e *allowlistExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return e.next.Temporality(k)
}

func (e *allowlistExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return e.next.Aggregation(k)
}

func (e *allowlistExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	rm.Resource = allowlistResource(rm.Resource)
	return e.next.Export(ctx, rm)
}

func (e *allowlistExporter) ForceFlush(ctx context.Context) error { return e.next.ForceFlush(ctx) }

func (e *allowlistExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

// allowlistResource returns a fresh Resource carrying ONLY the attributes in
// allowedResourceAttrs from res — dropping anything else the SDK, an
// ambient env var, or a future dependency change might have added.
func allowlistResource(res *resource.Resource) *resource.Resource {
	if res == nil {
		return res
	}
	var kept []attribute.KeyValue
	for _, kv := range res.Attributes() {
		if allowedResourceAttrs[string(kv.Key)] {
			kept = append(kept, kv)
		}
	}
	return resource.NewSchemaless(kept...)
}

// Provider wraps an OTLP metrics MeterProvider built directly over the OTel
// SDK's own otlpmetrichttp exporter (NOT toolhive-core's
// providers.NewCompositeProvider, which unconditionally adds
// resource.WithFromEnv() and resource.WithHost() to the exported resource on
// top of the SDK's own unconditional env merge — see allowlistExporter).
// Its MeterProvider is NEVER installed as the process-global provider
// (mirrors internal/adapter/telemetry's own discipline in otlp.go), so it
// cannot collide with an operator's own OTel setup.
type Provider struct {
	meterProvider *sdkmetric.MeterProvider
}

// NewProvider builds the product-metrics MeterProvider for one process. A
// network-unreachable endpoint is NOT an error here — the OTLP/HTTP
// exporter dials lazily on first export, matching the existing exporters in
// internal/adapter/telemetry/otlp.go.
func NewProvider(ctx context.Context, cfg Config) (*Provider, error) {
	if bakedKey == "" {
		return nil, fmt.Errorf("productmetrics: no ingest key baked into this build (see BUILD_LDFLAGS in Taskfile.yml)")
	}
	// mecatl.install.id is a per-install random UUID, deliberately attached
	// as a resource attribute (so it flattens onto every instrument this
	// provider exports). This was removed once (see git history) over
	// unbounded-cardinality concerns on the Prometheus-remote-write
	// destination (stacklok/infra#5604), then reinstated after the actual
	// cost was sized against real AMP pricing and accepted — see the ADR's
	// cost-analysis section for the numbers. mecak8s provisions this value
	// differently (a stable per-Helm-release ConfigMap, not this package's
	// local install-id file — see internal/cliconfig's mecak8s wiring and
	// deploy/helm/mecak8s/templates/install-id-configmap.yaml), since a
	// pod-local file would mint a new id on every pod restart.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("mecatl"),
			semconv.ServiceVersion(cfg.Version),
			attribute.String("mecatl.install.id", cfg.InstallID),
			attribute.String("mecatl.binary", string(cfg.Binary)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("productmetrics: build resource: %w", err)
	}

	// otlpmetrichttp.WithEndpointURL parses endpoint itself and derives the
	// host, the URL path (verbatim — see the endpoint var doc), and
	// TLS-vs-insecure transport (https:// scheme => secure, everything else
	// => insecure) — so the plain http:// endpoint this package's own tests
	// point at an httptest.Server against needs no separate WithInsecure()
	// call.
	exp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint),
		otlpmetrichttp.WithHeaders(map[string]string{headerKeyName: bakedKey}),
	)
	if err != nil {
		return nil, fmt.Errorf("productmetrics: build exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(&allowlistExporter{next: exp},
			sdkmetric.WithInterval(exportInterval))),
	)
	return &Provider{meterProvider: mp}, nil
}

// Meter returns the underlying metric.MeterProvider for instrument construction.
func (p *Provider) Meter() metric.MeterProvider { return p.meterProvider }

// Shutdown flushes and stops the provider, bounded by the caller's ctx.
func (p *Provider) Shutdown(ctx context.Context) error { return p.meterProvider.Shutdown(ctx) }
