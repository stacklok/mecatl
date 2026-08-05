package openaichat

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the openaichat adapter — the
// SSE→Chunk stream consumer and the SDK streaming goroutine must unwind on
// cancel/EOF. The translation is tested from fixtures with no live network, so
// nothing legitimate should linger; anything that does is a real finding.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
