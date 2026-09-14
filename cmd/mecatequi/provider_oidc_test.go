package main

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestAppConfigWiresNativeEndpointCredentialLoaderLifecycle(t *testing.T) {
	cfg := appConfig(flags{}, port.NopDiagnostics{}, observability{})
	loader, ok := cfg.NativeEndpointCredentialLoader.(*cliconfig.NativeEndpointLoader)
	if !ok || loader == nil {
		t.Fatalf("native endpoint credential loader = %T, want *cliconfig.NativeEndpointLoader", cfg.NativeEndpointCredentialLoader)
	}
	if cfg.NativeEndpointCredentialLifecycle != loader {
		t.Fatal("native endpoint credential lifecycle does not own the configured loader")
	}
}
