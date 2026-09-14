package main

import (
	"flag"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// TestProductMetricsSnapshotPopulatesConfiguredFeatures pins that mecak8s's
// heartbeat surfaces Guardrails/MCP/Scheduling/Provider from its own config
// rather than leaving them at their zero values — an empty Provider would
// heartbeat as the invalid provider_configured{family=""}, outside the
// documented anthropic|openai|openrouter|other enum.
func TestProductMetricsSnapshotPopulatesConfiguredFeatures(t *testing.T) {
	mcpServers := cliconfig.RegisterMCPServerFlag(flag.NewFlagSet("test", flag.ContinueOnError), "")
	if err := mcpServers.Set("example=https://mcp.example.com"); err != nil {
		t.Fatalf("mcpServers.Set: %v", err)
	}
	if err := mcpServers.Finalize(); err != nil {
		t.Fatalf("mcpServers.Finalize: %v", err)
	}

	snap := productMetricsSnapshot(config{
		guardrailsModel: "claude-haiku",
		mcpServers:      mcpServers,
		noScheduler:     false,
		defaultProvider: "openrouter/some-model",
	})
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
	if snap.Mode != productmetrics.ModeK8s {
		t.Errorf("Mode = %q, want %q", snap.Mode, productmetrics.ModeK8s)
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
