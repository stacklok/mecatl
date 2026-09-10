package cliconfig

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

func TestBuildProductMetricsDisabledReturnsZeroHandles(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), false, false,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, "", port.NopDiagnostics{})
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
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, "", port.NopDiagnostics{})
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
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, "", port.NopDiagnostics{})
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
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{}, "", port.NopDiagnostics{})
	if err != nil {
		t.Fatalf("BuildProductMetrics(enabled=false, dryRun=true): %v", err)
	}
	if h.Sink != nil || h.ToolCallRecorder != nil {
		t.Errorf("disabled handles carry a non-nil Sink/ToolCallRecorder even with dryRun=true: %+v", h)
	}
}

// TestBuildProductMetricsInstallIDOverrideSkipsTheLocalFile is the mecak8s
// contract (storage-free, no PVC, ADR 0048): a chart-provisioned install id
// must bypass LoadOrCreateInstallIDDefault ENTIRELY, not merely take
// precedence over whatever it returns.
//
// The oracle is which failure surfaces. With no resolvable state directory the
// local-file mechanism cannot even mint an id, so the no-override call fails at
// the install-id step; an override must get PAST that step and fail later, at
// provider construction (no baked ingest key in any non-release build). If the
// override were applied after the file read, both calls would report the same
// install-id error.
func TestBuildProductMetricsInstallIDOverrideSkipsTheLocalFile(t *testing.T) {
	// No XDG_STATE_HOME and no home dir => productmetrics.UserStateDir yields
	// "" and LoadOrCreateInstallIDDefault fails closed.
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	_, err := BuildProductMetrics(context.Background(), context.Background(), true, false,
		productmetrics.BinaryMecak8s, "test-version", 0, productmetrics.FeatureSnapshot{},
		"", port.NopDiagnostics{})
	if err == nil || !strings.Contains(err.Error(), "install id") {
		t.Fatalf("no override with an unresolvable state dir: err = %v, want an install-id failure", err)
	}

	_, err = BuildProductMetrics(context.Background(), context.Background(), true, false,
		productmetrics.BinaryMecak8s, "test-version", 0, productmetrics.FeatureSnapshot{},
		"11111111-2222-3333-4444-555555555555", port.NopDiagnostics{})
	if err == nil || strings.Contains(err.Error(), "install id") {
		t.Fatalf("override with an unresolvable state dir: err = %v, want the install-id step skipped", err)
	}
}

// TestBuildProductMetricsInstallIDOverrideNeverReportsFirstRun pins the
// disclosure-notice half: an override mints nothing locally, so this process
// has no first run to announce — the chart owns the id's lifecycle. Asserted
// on the disabled and dry-run paths, the only two that return handles without
// a baked ingest key.
func TestBuildProductMetricsInstallIDOverrideNeverReportsFirstRun(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled, dryRun bool
	}{
		{name: "disabled", enabled: false},
		{name: "dry run", enabled: true, dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := BuildProductMetrics(context.Background(), context.Background(), tc.enabled, tc.dryRun,
				productmetrics.BinaryMecak8s, "test-version", 0, productmetrics.FeatureSnapshot{},
				"11111111-2222-3333-4444-555555555555", port.NopDiagnostics{})
			if err != nil {
				t.Fatalf("BuildProductMetrics with an install-id override: %v", err)
			}
			if h.FirstRun {
				t.Error("an externally provisioned install id must never report FirstRun")
			}
		})
	}
}
