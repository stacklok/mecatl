package cliconfig

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

func TestBuildProductMetricsDisabledReturnsZeroHandles(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), false, false,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, port.NopDiagnostics{})
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
	_, err := BuildProductMetrics(context.Background(), context.Background(), true, false,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, port.NopDiagnostics{})
	if err == nil {
		t.Fatal("expected an error when enabled=true with no baked ingest key, got nil")
	}
}

// TestBuildProductMetricsDryRunNeverTouchesInstallIDOrRealProvider proves the
// dry-run branch takes the DryRunRecorder short-circuit BEFORE the
// install-id/provider construction that requires a baked ingest key — so
// dryRun=true must succeed with no error even though the enabled-for-real
// case (above) fails closed with no baked key.
func TestBuildProductMetricsDryRunNeverTouchesInstallIDOrRealProvider(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), true, true,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("BuildProductMetrics(enabled=true, dryRun=true): %v", err)
	}
	if h.Sink == nil || h.ToolCallRecorder == nil {
		t.Errorf("dry-run handles must carry a non-nil Sink/ToolCallRecorder: %+v", h)
	}
	if h.Shutdown == nil {
		t.Fatal("Shutdown must be non-nil in dry-run mode (a no-op)")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("dry-run no-op Shutdown returned an error: %v", err)
	}
	if h.FirstRun {
		t.Error("dry-run must never mint/read an install id, so FirstRun must stay false")
	}
}

// TestBuildProductMetricsDisabledDryRunStillNoop proves dryRun is inert when
// enabled is false — the disabled posture must stay byte-identical
// regardless of the dry-run flag's value.
func TestBuildProductMetricsDisabledDryRunStillNoop(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), false, true,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("BuildProductMetrics(enabled=false, dryRun=true): %v", err)
	}
	if h.Sink != nil || h.ToolCallRecorder != nil {
		t.Errorf("disabled handles carry a non-nil Sink/ToolCallRecorder even with dryRun=true: %+v", h)
	}
}
