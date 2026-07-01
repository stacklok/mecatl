package app

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// sortedNames projects a catalog into its sorted tool-name list for diffing.
func sortedNames(cat *tool.Catalog) []string {
	names := make([]string, 0)
	for name := range toolNameSet(cat.Tools()) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// diffNameSets returns the names present in a but not b, and in b but not a.
func diffNameSets(a, b []string) (onlyA, onlyB []string) {
	as, bs := map[string]struct{}{}, map[string]struct{}{}
	for _, n := range a {
		as[n] = struct{}{}
	}
	for _, n := range b {
		bs[n] = struct{}{}
	}
	for _, n := range a {
		if _, ok := bs[n]; !ok {
			onlyA = append(onlyA, n)
		}
	}
	for _, n := range b {
		if _, ok := as[n]; !ok {
			onlyB = append(onlyB, n)
		}
	}
	return onlyA, onlyB
}

// fullyLoadedCfg turns ON every optional, conditionally-registered tool family so
// the drift test exercises the complete surface: memory + user-model stores,
// skills (one valid SKILL.md) + a draft quarantine dir OUTSIDE the workspace,
// Parallel, Teams, the MCP resource meta-tools, and a shell (Bash).
func fullyLoadedCfg(t *testing.T) Config {
	t.Helper()
	skillsDir := t.TempDir()
	writeSkill(t, skillsDir, "drift-skill", "a drift fixture", "DRIFT BODY")
	return Config{
		Workspace:        t.TempDir(),
		Model:            "gpt-5",
		Shell:            "/bin/sh",
		MemoryDir:        t.TempDir(),
		UserModelDir:     t.TempDir(),
		SkillsDirs:       []string{skillsDir},
		SkillsDraftDir:   t.TempDir(), // outside the workspace (a sibling temp dir)
		EnableParallel:   true,
		EnableTeams:      true,
		MCPResourceTools: true,
		Diagnostics:      port.NopDiagnostics{},
	}
}

// requiredFamilyTools is the explicit pin of every conditionally-registered tool
// family the fully-loaded shared catalog MUST contain. Equality between the two
// assembly paths alone is BLIND to a family dropped from BOTH (QA mutant M2:
// deleting Parallel/Team/Skill/SkillDraft/resource-tools/SubagentStatus from
// assembleCatalog kept the equality green); this list converts "paths agree" into
// "paths agree AND the agreed set contains every family". The six memory tool
// names are pinned separately by the factory tests in
// session_engine_memory_test.go (allMemoryToolNames).
var requiredFamilyTools = []string{
	"Subagent",
	"InspectSubagent",
	"SubagentStatus",
	"Parallel",
	"Team",
	"InspectMember",
	skills.ToolName,      // "Skill"
	skills.DraftToolName, // "SkillDraft"
	"ListMcpResources",
	"ReadMcpResource",
	"CallMcpWithQuery",
	"mcp__globe__echo", // the server-global MCP mount itself
}

// TestPerSessionCatalogMatchesSharedCatalog is the issue-#42 KILL-SWITCH: the
// build-time shared catalog and a per-session (selector) catalog must carry
// EXACTLY the same tool-name set under a fully-loaded config, AND that agreed set
// must contain every conditionally-registered family (requiredFamilyTools — the
// both-paths-drop blindspot). The shared baseline is the catalog buildCatalog
// ACTUALLY RETURNS — not a direct assembleCatalog call — so a post-assembly
// MustRegister snuck into buildCatalog/buildEngine (the exact historical bug
// shape, three prior firings) shifts the baseline and the equality catches it.
//
// The ONLY sanctioned per-session deltas are:
//  1. the client MCP tools (a session's own mcpServers) — covered by the
//     modulo-allowlist sub-test below;
//  2. the unwrapped hooks (maybeWrapUserModelReview decorates the MAIN engine
//     only) — a Deps difference, not a catalog one, so it is invisible here.
func TestPerSessionCatalogMatchesSharedCatalog(t *testing.T) {
	ctx := context.Background()
	// The global server exposes a RESOURCE too, so the ListMcpResources/
	// ReadMcpResource meta-tools actually register (their gate requires ≥1
	// resource) and the family pin below is exercised for real.
	url := newMCPTestServerWithResource(t)

	cfg := fullyLoadedCfg(t)
	cfg.MCPServers = []mcp.ServerConfig{{Name: "globe", URL: url}}

	oa := mockllm.New(mockllm.TextTurn("OPENAI"))
	or := mockllm.New(mockllm.TextTurn("OPENROUTER"))
	reg := twoProviderReg(oa, providerOpenAI, cfg.Model, or, providerOpenRouter)
	hooks := hookexec.New(nil)

	// The GENUINE shared catalog + the GENUINE Phase-A assets: buildCatalog's own
	// output (it connects the global manager from cfg.MCPServers, opens the
	// flocked stores, resolves the skills fixture, and runs the build-time
	// assembly). Using buildCatalog — not a direct assembleCatalog call — is what
	// keeps a future post-assembly registration in buildCatalog inside the guard.
	sharedCat, assets, _, _, mcpClose, err := buildCatalog(ctx, cfg, reg, oa, hooks, agents.NewRegistry(nil), memstore.New())
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()
	if assets.globalMgr == nil {
		t.Fatal("buildCatalog connected no global MCP manager — fixture broken (the MCP families would be silently skipped)")
	}
	sharedNames := sortedNames(sharedCat)

	// Family-presence pin (mutant M2): a family dropped from BOTH paths keeps the
	// equality below green — so first require each family in the shared baseline.
	sharedSet := toolNameSet(sharedCat.Tools())
	for _, name := range requiredFamilyTools {
		if _, ok := sharedSet[name]; !ok {
			t.Errorf("fully-loaded shared catalog is missing required family tool %q — a family was dropped from the assembly itself", name)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// The selector inputs (what sessionEngineFactory passes for a /models pick),
	// over the SAME Phase-A assets, no client manager.
	selCat, selClose := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, assets, catalogSession{
		provider: or, providerID: providerOpenRouter, model: "openrouter/other-model", narrate: false,
	})
	defer func() { _ = selClose() }()

	if onlyShared, onlySel := diffNameSets(sharedNames, sortedNames(selCat)); len(onlyShared) > 0 || len(onlySel) > 0 {
		t.Fatalf("per-session catalog drifted from the shared catalog (issue #42):\n  only in shared: %v\n  only in selector: %v",
			onlyShared, onlySel)
	}

	// Modulo-allowlist variant: with client MCP specs attached, the per-session
	// catalog may differ ONLY by the client's namespaced tools (sanctioned delta 1).
	t.Run("client specs differ only by the client namespace", func(t *testing.T) {
		cliURL := newMCPTestServer(t)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		clientMgr, err := mcp.NewManager(cctx, []mcp.ServerConfig{{Name: "cli", URL: cliURL}}, nil, nil)
		if err != nil {
			t.Fatalf("NewManager(client): %v", err)
		}
		cliCat, cliClose := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, assets, catalogSession{
			provider: or, providerID: providerOpenRouter, model: "openrouter/other-model", clientMgr: clientMgr, narrate: false,
		})
		defer func() { _ = cliClose() }()

		onlyShared, onlyCli := diffNameSets(sharedNames, sortedNames(cliCat))
		if len(onlyShared) > 0 {
			t.Fatalf("client-MCP session catalog is MISSING shared tools: %v", onlyShared)
		}
		for _, n := range onlyCli {
			if !strings.HasPrefix(n, "mcp__cli__") {
				t.Fatalf("client-MCP session catalog carries an unsanctioned extra tool %q (only mcp__cli__* may differ)", n)
			}
		}
		if len(onlyCli) == 0 {
			t.Fatal("client manager mounted no tool — the allowlist branch was not exercised")
		}
	})

	// Factory-level superset check: the REAL sessionEngineFactory must keep
	// routing through assembleCatalog — every shared-catalog tool name resolves on
	// a selector engine built by the factory.
	t.Run("factory engine carries every shared tool", func(t *testing.T) {
		factory := sessionEngineFactory(cfg, reg, oa, memstore.New(),
			permpolicy.NewPolicy(defaultRules(), nil), hooks, nil, prompt.RootAssembler{}, assets, nil)
		res, err := factory(ctx, server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()
		for _, name := range sharedNames {
			if !res.Engine.HasTool(name) {
				t.Errorf("factory-built selector engine is missing shared tool %q — sessionEngineFactory stopped using assembleCatalog?", name)
			}
		}
	})
}
