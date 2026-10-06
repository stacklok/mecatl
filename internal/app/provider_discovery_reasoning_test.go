package app

import "testing"

// Separating observations from configured inventory floors must not change the
// existing reasoning-presence policy (tri-state redesign is outside ADR 0360).
func TestProviderDiscoveryPreservesConfiguredDefaultReasoningFloor(t *testing.T) {
	d := discoveryFixture(t, map[string]providerEntry{"custom": {defaultModel: "configured"}})
	if supported, known := modelReasoningSupport(d.reg, "custom", "configured"); supported || !known {
		t.Fatalf("configured inventory floor reasoning = %t/%t, want false/true", supported, known)
	}
	if _, known := modelReasoningSupport(d.reg, "custom", "passthrough"); known {
		t.Fatal("unlisted passthrough became known-incapable")
	}
}
