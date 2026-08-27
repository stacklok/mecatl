package port_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

type providerMetadataError struct{}

func (providerMetadataError) Error() string                        { return "provider failed" }
func (providerMetadataError) ProviderHTTPStatus() int              { return 503 }
func (providerMetadataError) ProviderInBandStatus() int            { return 429 }
func (providerMetadataError) ProviderErrorCode() string            { return "rate_limit_exceeded" }
func (providerMetadataError) ProviderErrorCorrelationKind() string { return "request" }
func (providerMetadataError) ProviderErrorCorrelationID() string   { return "req-123" }

func TestProviderErrorMetadataErrorIsStructural(t *testing.T) {
	var carrier port.ProviderErrorMetadataError
	if !errors.As(error(providerMetadataError{}), &carrier) {
		t.Fatal("primitive metadata carrier does not satisfy port contract")
	}
	if carrier.ProviderHTTPStatus() != 503 || carrier.ProviderInBandStatus() != 429 ||
		carrier.ProviderErrorCode() != "rate_limit_exceeded" ||
		carrier.ProviderErrorCorrelationKind() != "request" ||
		carrier.ProviderErrorCorrelationID() != "req-123" {
		t.Fatalf("unexpected metadata projection: %#v", carrier)
	}
}
