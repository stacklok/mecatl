package openaicompat

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the openaicompat lister.
// ListModels is a single synchronous GET over an INJECTED transport (mocked in
// tests, no live network), so it spawns no goroutine of its own and nothing
// should linger after a test. There is no ignore list — anything that lingers
// is a real finding.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
