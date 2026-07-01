package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// TestFetchMcpResourcePresentInBothCatalogProfiles is the explicit
// catalog-presence assertion for the FetchMcpResource tool (issue #223 Phase
// 2): it is an outbound read (like WebSearch/WebFetch), so it must be present
// in BOTH the default (fs) catalog AND the no-fs catalog assembled by the
// REAL assembleCatalog over the REAL buildCatalog assets. A selector no-fs
// session or a default session must both offer it to the model.
func TestFetchMcpResourcePresentInBothCatalogProfiles(t *testing.T) {
	ctx := context.Background()
	url := newMCPTestServerWithResource(t)

	cfg := fullyLoadedCfg(t)
	cfg.MCPServers = []mcp.ServerConfig{{Name: "globe", URL: url}}

	oa := mockllm.New(mockllm.TextTurn("x"))
	reg := regForTest(oa, providerOpenAI, cfg.Model)
	hooks := hookexec.New(nil)

	sharedCat, assets, _, _, mcpClose, err := buildCatalog(ctx, cfg, reg, oa, hooks, agents.NewRegistry(nil), memstore.New())
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()

	if _, ok := toolNameSet(sharedCat.Tools())["FetchMcpResource"]; !ok {
		t.Fatal("FetchMcpResource is MISSING from the default (fs) shared catalog — it must be registered as a core outbound-read tool")
	}

	noFSCat, noFSClose := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, assets, catalogSession{
		provider: oa, providerID: providerOpenAI, model: cfg.Model, narrate: false, noFS: true,
	})
	defer func() { _ = noFSClose() }()
	if _, ok := toolNameSet(noFSCat.Tools())["FetchMcpResource"]; !ok {
		t.Fatal("FetchMcpResource is MISSING from the no-fs catalog — it is an outbound read with no filesystem need and must ride the no-fs profile alongside WebFetch")
	}
}
