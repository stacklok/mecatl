package main

import (
	"flag"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/cliconfig"
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

// TestProductMetricsSnapshotPopulatesConfiguredFeatures pins the full
// heartbeat contract: every configured feature must surface on the
// FeatureSnapshot, and Provider must resolve to a real member of the
// documented anthropic|openai|openrouter|other enum — never the zero value
// (empty string), which would heartbeat as the invalid
// provider_configured{family=""}.
func TestProductMetricsSnapshotPopulatesConfiguredFeatures(t *testing.T) {
	mcpServers := cliconfig.RegisterMCPServerFlag(flag.NewFlagSet("test", flag.ContinueOnError), "")
	if err := mcpServers.Set("example=https://mcp.example.com"); err != nil {
		t.Fatalf("mcpServers.Set: %v", err)
	}
	if err := mcpServers.Finalize(); err != nil {
		t.Fatalf("mcpServers.Finalize: %v", err)
	}

	cfg := config{
		memoryDir:       "/tmp/memory",
		guardrailsModel: "claude-haiku",
		mcpServers:      mcpServers,
		noScheduler:     false,
		defaultProvider: "openrouter/some-model",
	}
	snap := productMetricsSnapshot(cfg)

	if !snap.Memory {
		t.Error("Memory = false, want true (memoryDir configured)")
	}
	if !snap.Guardrails {
		t.Error("Guardrails = false, want true (guardrailsModel configured)")
	}
	if !snap.MCP {
		t.Error("MCP = false, want true (an mcp server is configured)")
	}
	if !snap.Scheduling {
		t.Error("Scheduling = false, want true (noScheduler is false)")
	}
	if snap.Provider != productmetrics.ProviderOpenRouter {
		t.Errorf("Provider = %q, want %q", snap.Provider, productmetrics.ProviderOpenRouter)
	}
}

// TestProductMetricsSnapshotProviderNeverEmptyByDefault pins that an
// unconfigured, zero-value config still resolves Provider to a real enum
// member (the Anthropic default), never the invalid empty string.
func TestProductMetricsSnapshotProviderNeverEmptyByDefault(t *testing.T) {
	if got := productMetricsSnapshot(config{}).Provider; got != productmetrics.ProviderAnthropic {
		t.Errorf("Provider = %q, want %q (the default when nothing is configured)", got, productmetrics.ProviderAnthropic)
	}
}
