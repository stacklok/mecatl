package productmetrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestNewProviderExportsToConfiguredEndpoint(t *testing.T) {
	origKey := bakedKey
	bakedKey = "test-key"
	defer func() { bakedKey = origKey }()

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(headerKeyName)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origEndpoint := endpoint
	endpoint = srv.URL
	defer func() { endpoint = origEndpoint }()

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
	if gotHeader != "test-key" {
		t.Errorf("collector received %s=%q, want %q", headerKeyName, gotHeader, "test-key")
	}
}
