package cliconfig

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

func TestBuildProductMetricsDisabledReturnsZeroHandles(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), false,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{})
	if err != nil {
		t.Fatalf("BuildProductMetrics(enabled=false): %v", err)
	}
	if h.Sink != nil || h.ToolCallRecorder != nil {
		t.Errorf("disabled handles carry a non-nil Sink/ToolCallRecorder: %+v", h)
	}
	if h.Shutdown == nil {
		t.Fatal("Shutdown must be non-nil even when disabled (a no-op)")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("no-op Shutdown returned an error: %v", err)
	}
}

func TestBuildProductMetricsEnabledFailsClosedWithNoBakedKey(t *testing.T) {
	// bakedKey is empty in every non-release build/test — enabling must
	// surface the error rather than silently disabling, so a caller notices
	// its release build is missing the ldflag.
	_, err := BuildProductMetrics(context.Background(), context.Background(), true,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{})
	if err == nil {
		t.Fatal("expected an error when enabled=true with no baked ingest key, got nil")
	}
}
