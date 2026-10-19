package mcpbroker

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialenvelope"
)

func testCredentialKeyRing(t *testing.T) *credentialenvelope.KeyRing {
	t.Helper()
	ring, err := credentialenvelope.NewKeyRing("key-a", map[string][]byte{"key-a": []byte(strings.Repeat("k", credentialenvelope.KeyBytes))})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
