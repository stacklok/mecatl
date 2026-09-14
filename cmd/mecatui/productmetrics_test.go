package main

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

// TestProductMetricsSnapshotPopulatesConfiguredFeatures pins that mecatui's
// embedded-server heartbeat surfaces the fields it CAN see on cfg (Memory,
// Provider) rather than leaving them at their zero values — an empty
// Provider would heartbeat as the invalid provider_configured{family=""},
// outside the documented anthropic|openai|openrouter|other enum.
func TestProductMetricsSnapshotPopulatesConfiguredFeatures(t *testing.T) {
	snap := productMetricsSnapshot(config{
		memoryDir:       "/tmp/memory",
		defaultProvider: "openrouter/some-model",
	})
	if !snap.Memory {
		t.Error("Memory = false, want true (memoryDir configured)")
	}
	if snap.Provider != productmetrics.ProviderOpenRouter {
		t.Errorf("Provider = %q, want %q", snap.Provider, productmetrics.ProviderOpenRouter)
	}
	if snap.Mode != productmetrics.ModeInteractive {
		t.Errorf("Mode = %q, want %q", snap.Mode, productmetrics.ModeInteractive)
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
