package main

import (
	"flag"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// TestProductMetricsSnapshotPopulatesConfiguredFeatures pins that mecatequi's
// heartbeat surfaces Guardrails/MCP/Provider from its own flags rather than
// leaving them at their zero values — an empty Provider would heartbeat as
// the invalid provider_configured{family=""}, outside the documented
// anthropic|openai|openrouter|other enum.
func TestProductMetricsSnapshotPopulatesConfiguredFeatures(t *testing.T) {
	mcpServers := cliconfig.RegisterMCPServerFlag(flag.NewFlagSet("test", flag.ContinueOnError), "")
	if err := mcpServers.Set("example=https://mcp.example.com"); err != nil {
		t.Fatalf("mcpServers.Set: %v", err)
	}
	if err := mcpServers.Finalize(); err != nil {
		t.Fatalf("mcpServers.Finalize: %v", err)
	}

	snap := productMetricsSnapshot(flags{
		guardrailsModel: "claude-haiku",
		mcpServers:      mcpServers,
		defaultProvider: "openrouter/some-model",
	})
	if !snap.Guardrails {
		t.Error("Guardrails = false, want true (guardrailsModel configured)")
	}
	if !snap.MCP {
		t.Error("MCP = false, want true (an mcp server is configured)")
	}
	if snap.Provider != productmetrics.ProviderOpenRouter {
		t.Errorf("Provider = %q, want %q", snap.Provider, productmetrics.ProviderOpenRouter)
	}
	if snap.Mode != productmetrics.ModeHeadless {
		t.Errorf("Mode = %q, want %q", snap.Mode, productmetrics.ModeHeadless)
	}
}

// TestProductMetricsSnapshotProviderNeverEmptyByDefault pins that an
// unconfigured, zero-value flags still resolves Provider to a real enum
// member (the Anthropic default), never the invalid empty string.
func TestProductMetricsSnapshotProviderNeverEmptyByDefault(t *testing.T) {
	if got := productMetricsSnapshot(flags{}).Provider; got != productmetrics.ProviderAnthropic {
		t.Errorf("Provider = %q, want %q (the default when nothing is configured)", got, productmetrics.ProviderAnthropic)
	}
}
