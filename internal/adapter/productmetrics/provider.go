package productmetrics

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive-core/telemetry/providers"
	"go.opentelemetry.io/otel/metric"
)

// endpoint and headerKeyName are the ONE destination this pipeline can ever
// send to (stacklok/infra#5604): a dedicated, internet-facing OTLP/HTTP
// ingest at metrics.stacklok.com, gated by a single shared key baked into
// the binary. Neither is operator-configurable — an operator's own
// --otlp-endpoint has zero effect on this path, and this path has zero
// effect on the operator's own OTLP/Prometheus pipeline (a completely
// separate MeterProvider, never installed as global). endpoint is a var
// (not a const) so tests can point it at an httptest server.
var (
	endpoint      = "https://metrics.stacklok.com/v1/metrics"
	headerKeyName = "x-mecatl-metrics-key"
)

// bakedKey is the shared ingest key baked into the binary at build time via
// `-X github.com/stacklok/mecatl/internal/adapter/productmetrics.bakedKey=…`
// (see Taskfile.yml's BUILD_LDFLAGS). An empty key — every local/dev/CI-test
// build that does not set the ldflag — disables the pipeline entirely:
// NewProvider refuses to construct, so a non-release build can never
// accidentally phone home with an invalid or absent key.
var bakedKey = ""

// Provider wraps the toolhive-core OTLP metrics provider. Its MeterProvider
// is NEVER installed as the process-global provider (mirrors
// internal/adapter/telemetry's own discipline in otlp.go), so it cannot
// collide with an operator's own OTel setup.
type Provider struct {
	composite *providers.CompositeProvider
}

// NewProvider builds the product-metrics MeterProvider for one process. A
// network-unreachable endpoint is NOT an error here — the OTLP/HTTP
// exporter dials lazily on first export, matching the existing exporters in
// internal/adapter/telemetry/otlp.go.
func NewProvider(ctx context.Context, cfg Config) (*Provider, error) {
	if bakedKey == "" {
		return nil, fmt.Errorf("productmetrics: no ingest key baked into this build (see BUILD_LDFLAGS in Taskfile.yml)")
	}
	// toolhive-core's OTLP metric exporter strips the scheme from the
	// endpoint and defaults to a secure (TLS) connection regardless — so a
	// plain http:// endpoint (only ever true in this package's own test,
	// pointed at an httptest.Server) must explicitly opt into WithInsecure,
	// or the exporter tries TLS against a plaintext listener and every
	// export fails. The real production endpoint is always https://.
	//
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
	composite, err := providers.NewCompositeProvider(ctx,
		providers.WithServiceName("mecatl"),
		providers.WithServiceVersion(cfg.Version),
		providers.WithOTLPEndpoint(endpoint),
		providers.WithMetricsEnabled(true),
		providers.WithInsecure(strings.HasPrefix(endpoint, "http://")),
		providers.WithHeaders(map[string]string{headerKeyName: bakedKey}),
		providers.WithCustomAttributes(map[string]string{
			"mecatl.install.id": cfg.InstallID,
			"mecatl.binary":     string(cfg.Binary),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("productmetrics: build provider: %w", err)
	}
	return &Provider{composite: composite}, nil
}

// Meter returns the underlying metric.MeterProvider for instrument construction.
func (p *Provider) Meter() metric.MeterProvider { return p.composite.MeterProvider() }

// Shutdown flushes and stops the provider, bounded by the caller's ctx.
func (p *Provider) Shutdown(ctx context.Context) error { return p.composite.Shutdown(ctx) }
