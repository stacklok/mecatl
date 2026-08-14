package credentialstore_test

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore/conformance"
)

func TestMemoryStoreConformance(t *testing.T) {
	backend := credentialstore.NewMemoryBackend()
	conformance.Run(t, backend.Open, credentialstore.Capabilities{
		Persistent:      false,
		CrossProcessCAS: false,
	})
}
