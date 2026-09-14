package productmetrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestNewProviderFailsClosedWithNoBakedKey(t *testing.T) {
	orig := bakedKey
	bakedKey = ""
	defer func() { bakedKey = orig }()

	_, err := NewProvider(context.Background(), Config{Binary: BinaryMecated, Version: "test"})
	if err == nil {
		t.Fatal("expected an error when no ingest key is baked into the build, got nil")
	}
}

// fakeMetricsCollector is a minimal httptest OTLP/HTTP metrics ingest that
// records the exact request path and the raw ExportMetricsServiceRequest
// body of every export, so tests can assert on both — not merely that a
// request without a path arrived (which cannot detect a doubled signal path
// or a leaked resource attribute).
type fakeMetricsCollector struct {
	srv     *httptest.Server
	gotPath string
	gotBody *otlpmetrics.ExportMetricsServiceRequest
	gotAuth string
}

func newFakeMetricsCollector(t *testing.T) *fakeMetricsCollector {
	t.Helper()
	c := &fakeMetricsCollector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.gotPath = r.URL.Path
		c.gotAuth = r.Header.Get(headerKeyName)
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
		c.gotBody = req
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// TestNewProviderExportsToConfiguredEndpoint pins the endpoint against a
// PRODUCTION-SHAPED base URL — the real `endpoint` var, not a bare
// pathless httptest.Server URL — so a regression that re-introduces the
// doubled "/v1/metrics/v1/metrics" signal path (toolhive-core's
// otlp.NewMetricReader appends its own "/v1/metrics" to any non-empty base
// path) fails this test instead of passing silently. It also seeds an
// ambient OTEL_RESOURCE_ATTRIBUTES value and asserts the exported
// ResourceMetrics.Resource carries ONLY the four declared attributes, so a
// regression back to providers.NewCompositeProvider's unconditional
// resource.WithFromEnv()/resource.WithHost() also fails here.
func TestNewProviderExportsToConfiguredEndpoint(t *testing.T) {
	origKey := bakedKey
	bakedKey = "test-key"
	defer func() { bakedKey = origKey }()

	collector := newFakeMetricsCollector(t)

	origEndpoint := endpoint
	// Mirror the real production endpoint shape exactly: a base URL with NO
	// signal path, matching `endpoint`'s documented contract.
	endpoint = collector.srv.URL + "/v1/metrics"
	defer func() { endpoint = origEndpoint }()

	origAttrs, hadAttrs := os.LookupEnv("OTEL_RESOURCE_ATTRIBUTES")
	if err := os.Setenv("OTEL_RESOURCE_ATTRIBUTES", "tenant.id=customer-a"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	defer func() {
		if hadAttrs {
			os.Setenv("OTEL_RESOURCE_ATTRIBUTES", origAttrs) //nolint:errcheck // test cleanup
		} else {
			os.Unsetenv("OTEL_RESOURCE_ATTRIBUTES") //nolint:errcheck // test cleanup
		}
	}()

	p, err := NewProvider(context.Background(), Config{
		Binary:    BinaryMecated,
		Version:   "test",
		InstallID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer p.Shutdown(context.Background())

	meter := p.Meter().Meter("test")
	counter, cerr := meter.Int64Counter("mecatl.product.test")
	if cerr != nil {
		t.Fatalf("Int64Counter: %v", cerr)
	}
	counter.Add(context.Background(), 1)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if collector.gotAuth != "test-key" {
		t.Errorf("collector received %s=%q, want %q", headerKeyName, collector.gotAuth, "test-key")
	}
	if collector.gotPath != "/v1/metrics" {
		t.Errorf("collector received request path %q, want %q (a doubled signal path regression)", collector.gotPath, "/v1/metrics")
	}
	if collector.gotBody == nil {
		t.Fatal("collector received no decodable ExportMetricsServiceRequest")
	}

	// Assert on the RESOURCE, not merely the instrument/data-point
	// attributes: NewCompositeProvider's resource.WithFromEnv() +
	// resource.WithHost() would surface both the seeded ambient
	// OTEL_RESOURCE_ATTRIBUTES ("tenant.id") and the local hostname here.
	allowed := map[string]bool{
		"service.name":      true,
		"service.version":   true,
		"mecatl.install.id": true,
		"mecatl.binary":     true,
	}
	for _, rm := range collector.gotBody.GetResourceMetrics() {
		for _, attr := range rm.GetResource().GetAttributes() {
			if !allowed[attr.GetKey()] {
				t.Errorf("resource carries undeclared attribute %q=%q (privacy contract violation)",
					attr.GetKey(), attr.GetValue().GetStringValue())
			}
		}
	}
}
