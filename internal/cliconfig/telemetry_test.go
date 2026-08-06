package cliconfig

import (
	"context"
	"testing"
)

// TestHeadlessTelemetryNoEndpointsIsNoop asserts the byte-identical no-telemetry
// posture: a zero HeadlessTelemetryConfig (both endpoints empty, Scrape false)
// returns zero handles and a no-op Shutdown, so a headless main with no --otlp-*
// flags is unchanged (Sink/ToolCallRecorder/MetricsRoleScoper nil, no pipeline
// built).
func TestHeadlessTelemetryNoEndpointsIsNoop(t *testing.T) {
	h, err := HeadlessTelemetry(context.Background(), HeadlessTelemetryConfig{})
	if err != nil {
		t.Fatalf("HeadlessTelemetry(zero): unexpected error: %v", err)
	}
	if h.Sink != nil {
		t.Error("zero-config Sink must be nil (no pipeline)")
	}
	if h.ToolCallRecorder != nil {
		t.Error("zero-config ToolCallRecorder must be nil")
	}
	if h.MetricsRoleScoper != nil {
		t.Error("zero-config MetricsRoleScoper must be nil (children unmetered)")
	}
	if h.Metrics != nil {
		t.Error("zero-config Metrics must be nil")
	}
	if h.Registry != nil {
		t.Error("zero-config Registry must be nil (no /metrics listener data)")
	}
	if h.Shutdown == nil {
		t.Fatal("zero-config Shutdown must be non-nil (a no-op the caller can defer)")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("zero-config Shutdown returned error: %v", err)
	}
}
