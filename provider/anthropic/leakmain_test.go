package anthropic

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the anthropic adapter — the
// SSE→Chunk stream consumer (stream.go) and the SDK streaming goroutine must
// unwind on cancel/EOF. A leaked stream consumer fails the suite.
//
// There is NO ignore list: the SSE→Chunk path is tested from fixtures with no
// live network, so no net/http idle-conn reaper or SDK transport goroutine is
// spawned. Anything that lingers is a real finding.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
