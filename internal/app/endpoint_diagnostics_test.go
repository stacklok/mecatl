package app

import (
	"context"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBuildProjectsOnlySelectedProviderEndpoint(t *testing.T) {
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:       t.TempDir(),
		NoSoul:          true,
		OpenRouterKey:   "test-key",
		OpenAIKey:       "test-key",
		DefaultProvider: providerOpenRouter,
		ProviderOverrides: permconfig.ProviderOverrides{
			providerOpenRouter: {BaseURL: "https://user:secret@provider.example:8443/api/../v1?token=secret#fragment"},
			providerOpenAI:     {BaseURL: "https://user:secret@selected.example:9443/api/../v1?token=secret#fragment"},
		},
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	info, err := server.NewHarnessServer(built.Service).GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{ProviderId: providerOpenAI})
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	if got := info.GetLlmProviderDisplayEndpoint(); got != "https://selected.example:9443/v1" {
		t.Fatalf("llm_provider_display_endpoint = %q, want sanitized selected provider", got)
	}
	for _, providerID := range []string{"", "unknown"} {
		info, err := server.NewHarnessServer(built.Service).GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{ProviderId: providerID})
		if err != nil {
			t.Fatalf("GetServerInfo(%q): %v", providerID, err)
		}
		if got := info.GetLlmProviderDisplayEndpoint(); got != "" {
			t.Fatalf("llm_provider_display_endpoint for %q = %q, want unavailable", providerID, got)
		}
	}
}
