package permconfig

import "testing"

func TestParseYAMLTelemetryProductMetricsEnabled(t *testing.T) {
	data := []byte("telemetry:\n  productMetrics:\n    enabled: false\n")
	cfg, err := parseYAML(data)
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	if cfg.Telemetry == nil || cfg.Telemetry.ProductMetrics == nil {
		t.Fatal("Telemetry.ProductMetrics is nil")
	}
	if cfg.Telemetry.ProductMetrics.Enabled == nil || *cfg.Telemetry.ProductMetrics.Enabled != false {
		t.Errorf("Enabled = %v, want explicit false", cfg.Telemetry.ProductMetrics.Enabled)
	}
}

func TestParseYAMLTelemetryUnknownKeyErrors(t *testing.T) {
	data := []byte("telemetry:\n  productmetric:\n    enabled: false\n") // typo: productmetric
	if _, err := parseYAML(data); err == nil {
		t.Fatal("expected a strict-parse error for the unknown telemetry.productmetric key, got nil")
	}
}

func TestParseYAMLTelemetryProductMetricsUnknownKeyErrors(t *testing.T) {
	data := []byte("telemetry:\n  productMetrics:\n    enable: false\n") // typo: enable
	if _, err := parseYAML(data); err == nil {
		t.Fatal("expected a strict-parse error for the unknown enable key, got nil")
	}
}
