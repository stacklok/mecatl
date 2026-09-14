package productmetrics

import "testing"

func TestFeatureSnapshotEnabledIsClosedAndBounded(t *testing.T) {
	snap := FeatureSnapshot{Memory: true, MCP: true, Provider: ProviderAnthropic, Mode: ModeInteractive}
	got := snap.enabled()

	want := map[Feature]bool{
		FeatureMemory:     true,
		FeatureGuardrails: false,
		FeatureMCP:        true,
		FeatureScheduling: false,
	}
	if len(got) != len(want) {
		t.Fatalf("enabled() returned %d entries, want %d (%v)", len(got), len(want), got)
	}
	for f, v := range want {
		if got[f] != v {
			t.Errorf("enabled()[%q] = %v, want %v", f, got[f], v)
		}
	}
}
