package memconformance

import "testing"

func TestConformanceSuiteIsRegistered(t *testing.T) {
	t.Helper()
	var _ = Run
}
