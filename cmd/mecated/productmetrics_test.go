package main

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

// TestProductMetricsSnapshotModeReflectsHeadless pins that the deployment
// mode reported to the product-metrics heartbeat matches --headless — a
// headless mecated (autonomous/CI deployment) must report ModeHeadless, not
// the default ModeInteractive, or the adoption dashboard's headless/
// interactive split is corrupted for every headless mecated server.
func TestProductMetricsSnapshotModeReflectsHeadless(t *testing.T) {
	if got := productMetricsSnapshot(config{}).Mode; got != productmetrics.ModeInteractive {
		t.Errorf("Mode = %q, want %q (default interactive)", got, productmetrics.ModeInteractive)
	}
	if got := productMetricsSnapshot(config{headless: true}).Mode; got != productmetrics.ModeHeadless {
		t.Errorf("Mode = %q, want %q (--headless)", got, productmetrics.ModeHeadless)
	}
}
