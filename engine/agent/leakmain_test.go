package agent_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate across the agent package — the
// concurrency-heaviest part of mecatl. The per-run drive goroutine
// (loop.go), the read-batch fan-out (dispatch.go), and the
// subagent/fork/judge/team observable goroutines all live here, so a
// cancellation path that fails to unwind a goroutine fails the suite.
//
// There is NO ignore list: the suite runs fully offline (mockllm + memfs,
// no telemetry/OTel BatchSpanProcessor, no real HTTP/gRPC dials), so there
// is no known-noise goroutine to pin. Every goroutine the tests spawn is
// expected to unwind on context cancel; anything that lingers is a real
// finding, not noise to suppress.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
