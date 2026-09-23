package app

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
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
// Parallel, Teams, the MCP resource meta-tools, and a shell (Shell).
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
		// StoreDir (ADR 0073/0076): a jsonlstore backs a ScheduleStore, so the
		// fully-loaded catalog carries the Schedule + ScheduleQuery family and
		// the anti-drift pin exercises it for real (memstore keeps them
		// honestly absent — TestScheduleTool_RegisteredOnlyWhenStoreBacked).
		StoreDir: t.TempDir(),
	}
}

// requiredFamilyTools is the explicit pin of every conditionally-registered tool
// family the fully-loaded shared catalog MUST contain. Equality between the two
// assembly paths alone is BLIND to a family dropped from BOTH (QA mutant M2:
// deleting Parallel/Team/Skill/SkillDraft/resource-tools/SubagentStatus from
// assembleCatalog kept the equality green); this list converts "paths agree" into
// "paths agree AND the agreed set contains every family". The six memory tool
// names are pinned separately by the factory tests in
// session_engine_memory_test.go (allMemoryToolNames). PresentPlan (issue #206) is
// pinned here too — it is registered unconditionally by assembleCatalog, but
// without this entry a drop from BOTH paths would keep the equality green.
var requiredFamilyTools = []string{
	"Subagent",
	"InspectSubagent",
	"SubagentStatus",
	// ShellStatus is NOT in this list deliberately: it is registered iff Shell is
	// (registerCoreTools), so it belongs to the Shell-covered set the equality
	// already proves — the fully-loaded cfg has a shell, so the equality DOES
	// cover it; a dedicated row would only restate the registerCoreTools gate.
	"Parallel",
	"Team",
	"InspectMember",
	"PresentPlan",               // issue #206 Wave 3 — registered everywhere, advertised only in plan mode
	agentModelDiscoveryToolName, // composition-owned resolved model inventory
	skills.ToolName,             // "Skill"
	skills.DraftToolName,        // "SkillDraft"
	"ListMcpResources",
	"ReadMcpResource",
	"CallMcpWithQuery",
	"mcp__globe__echo", // the server-global MCP mount itself
	// Schedule + ScheduleQuery (ADR 0073/0076, AC2.3): the eager factory bind
	// registers them in BOTH catalogs over the SAME gate, so the name-set
	// equality covers them with NO schedule carve-out. fullyLoadedCfg backs a
	// ScheduleStore (StoreDir → jsonlstore) so the family pin exercises the
	// registration for real; a store without one keeps them honestly absent
	// from both (TestScheduleTool_RegisteredOnlyWhenStoreBacked).
	agent.ScheduleToolName,      // "Schedule"
	agent.ScheduleQueryToolName, // "ScheduleQuery"
}

// eagerScheduleFactoryForTest mirrors buildEngine's eager bind (ADR 0076): the
// manager is resolved from the store BEFORE any catalog assembly and the
// captured factory is what buildCatalog binds onto the assets. Tests that call
// buildCatalog directly (the drift/nofs guards) pass this so the shared
// assembly they exercise matches the production wiring; a store with no
// ScheduleStore (memstore) yields a nil-resolving factory — the honest
// absent-tool path.
func eagerScheduleFactoryForTest(t *testing.T, store port.SessionStore) func() port.ScheduleManager {
	t.Helper()
	// Concrete type + explicit nil check: the typed-nil discipline (a nil
	// *scheduleManager boxed in a non-nil interface would defeat the
	// honest-absence gate).
	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store:       store,
		Diagnostics: port.NopDiagnostics{},
	})
	return func() port.ScheduleManager {
		if mgr == nil {
			return nil
		}
		return mgr
	}
}

// fullyLoadedScheduleStore opens the jsonlstore under cfg.StoreDir — the
// ScheduleStore-backed store the fully-loaded drift/nofs guards pass to
// buildCatalog so the Schedule family registers (cfg.StoreDir is what
// buildSessionStore would use on the production path).
func fullyLoadedScheduleStore(t *testing.T, cfg Config) port.SessionStore {
	t.Helper()
	st, err := jsonlstore.New(cfg.StoreDir)
	if err != nil {
		t.Fatalf("jsonlstore.New(%q): %v", cfg.StoreDir, err)
	}
	return st
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
	store := fullyLoadedScheduleStore(t, cfg)
	sharedCat, assets, _, _, mcpClose, err := buildCatalog(ctx, isolateConfig(t, cfg), reg, oa, hooks, agents.NewRegistry(nil), store, eagerScheduleFactoryForTest(t, store))
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
	selCat, selClose, _ := assembleCatalog(ctx, cfg, reg, store, hooks, &assets, catalogSession{
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
		cliCat, cliClose, _ := assembleCatalog(ctx, cfg, reg, store, hooks, &assets, catalogSession{
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
		factory := sessionEngineFactory(cfg, reg, oa, store,
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

// TestPresentPlanInSharedAndPerSessionCatalogs pins issue #206 Wave 3's
// registration invariant: PresentPlan is registered in EVERY catalog (the shared
// build-time catalog AND a per-session selector catalog, over the SAME Phase-A
// assets) so the name-set equality guard above stays green. It is registered for
// name-set equality but ADVERTISED only in plan mode (the tool implements
// tool.PlanOnly, so the catalog's mode projection excludes it from non-plan modes).
// This test pins the registration half — the projection gate is pinned separately
// in engine/tool/catalog_test.go and engine/agent/presentplan_ask_test.go.
func TestPresentPlanInSharedAndPerSessionCatalogs(t *testing.T) {
	ctx := context.Background()
	cfg := fullyLoadedCfg(t)

	oa := mockllm.New(mockllm.TextTurn("OPENAI"))
	or := mockllm.New(mockllm.TextTurn("OPENROUTER"))
	reg := twoProviderReg(oa, providerOpenAI, cfg.Model, or, providerOpenRouter)
	hooks := hookexec.New(nil)

	sharedCat, assets, _, _, mcpClose, err := buildCatalog(ctx, isolateConfig(t, cfg), reg, oa, hooks, agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()

	if _, ok := sharedCat.Lookup("PresentPlan"); !ok {
		t.Fatal("the shared build-time catalog must register PresentPlan (issue #206 Wave 3)")
	}

	selCat, selClose, _ := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, &assets, catalogSession{
		provider: or, providerID: providerOpenRouter, model: "openrouter/other-model", narrate: false,
	})
	defer func() { _ = selClose() }()
	if _, ok := selCat.Lookup("PresentPlan"); !ok {
		t.Fatal("a per-session selector catalog must register PresentPlan (issue #206 Wave 3) — name-set equality with the shared catalog")
	}
}

// TestBackgroundShellCatalogWiring pins the background-Shell composition contract
// (the agent ShellTool + its ShellStatus companion) on the REAL assembly paths:
//
//   - the fully-loaded SHARED catalog holds the canonical Shell and ShellStatus pair;
//   - EVERY child surface that enables Shell registers the same pair, while a
//     shell-less surface registers neither. ShellStatus is a companion capability,
//     not an allowlist entry, so scoped agent definitions retain their requested
//     tool scope.
func TestCanonicalShellTool_Scenario1_CatalogNames(t *testing.T) {
	ctx := context.Background()
	url := newMCPTestServerWithResource(t)

	cfg := fullyLoadedCfg(t)
	cfg.MCPServers = []mcp.ServerConfig{{Name: "globe", URL: url}}
	// TRUSTED workspace: the child shell is gated on
	// cfg.TrustProject (buildSandboxedCommandRunner, issue #40)
	// — without this the child-side half of the test would exercise the shell-less
	// posture instead of the Shell-carrying one.
	cfg.TrustProject = true

	oa := mockllm.New(mockllm.TextTurn("OPENAI"))
	reg := regForTest(oa, providerOpenAI, cfg.Model)
	hooks := hookexec.New(nil)

	store := fullyLoadedScheduleStore(t, cfg)
	sharedCat, _, _, _, mcpClose, err := buildCatalog(ctx, isolateConfig(t, cfg), reg, oa, hooks, agents.NewRegistry(nil), store, eagerScheduleFactoryForTest(t, store))
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()

	bash, ok := sharedCat.Lookup(tool.ShellToolName)
	if !ok {
		t.Fatal("shared catalog lost Shell under a fully-loaded config")
	}
	if _, isAgent := bash.(agent.ShellTool); !isAgent {
		t.Fatalf("shared catalog Shell is %T, want agent.ShellTool (the background-capable construction)", bash)
	}
	bashStatus, ok := sharedCat.Lookup("ShellStatus")
	if !ok {
		t.Fatal("shared catalog lost ShellStatus (the agent ShellTool's companion)")
	}
	if _, isShellStatus := bashStatus.(*agent.ShellStatusTool); !isShellStatus {
		t.Fatalf("shared catalog ShellStatus is %T, want *agent.ShellStatusTool", bashStatus)
	}
	if _, ok := sharedCat.Lookup("Bash"); ok {
		t.Fatal("shared catalog must not register legacy Bash")
	}

	// The read-only explorer surface (the default Subagent child's catalog):
	// canonical Shell and ShellStatus are both present.
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: fully-loaded config yields a sandboxed runner")
	}
	explorer := readOnlyExplorerCatalog(runner)
	childShell, ok := explorer.Lookup(tool.ShellToolName)
	if !ok {
		t.Fatal("read-only explorer catalog lost Shell")
	}
	if _, isAgent := childShell.(agent.ShellTool); !isAgent {
		t.Fatalf("child Shell is %T, want agent.ShellTool (child background parity)", childShell)
	}
	childStatus, ok := explorer.Lookup("ShellStatus")
	if !ok {
		t.Fatal("read-only explorer catalog lost ShellStatus")
	}
	if _, isShellStatus := childStatus.(*agent.ShellStatusTool); !isShellStatus {
		t.Fatalf("child ShellStatus is %T, want *agent.ShellStatusTool", childStatus)
	}
	if _, ok := explorer.Lookup("Bash"); ok {
		t.Fatal("read-only explorer catalog must not register legacy Bash")
	}

	// The agent-def scoped path (the REAL buildAgentDefEngine): a def allow-listing
	// Shell keeps the canonical Shell/ShellStatus pair.
	def := agents.AgentDef{Name: "scoped-explorer", Tools: []string{"Read", "Shell"}}
	base := baseSubagentTools(cfg)
	defEng, defClose, defNames, _, _ := buildAgentDefEngine(ctx, cfg, def, "task:"+def.Name, "test",
		oa, cfg.Model, nil, base, false /*allowMutating*/, true /*allowShell*/, nil, hooks, runner, nil)
	if defClose != nil {
		defer func() { _ = defClose() }()
	}
	foundShell := false
	for _, n := range defNames {
		if n == tool.ShellToolName {
			foundShell = true
		}
	}
	if !foundShell {
		t.Fatalf("def-scoped names %v lost Shell (allowShell keeps it)", defNames)
	}
	// The built engine holds the canonical Shell/ShellStatus pair; the requested
	// scoped names retain only the explicit Shell allowlist entry.
	if !defEng.HasTool(tool.ShellToolName) {
		t.Fatal("def-scoped engine lost Shell")
	}
	if !defEng.HasTool("ShellStatus") {
		t.Fatal("def-scoped engine lost ShellStatus")
	}
	if defEng.HasTool("Bash") {
		t.Fatal("def-scoped engine must not contain legacy Bash")
	}
	if _, isAgent := base[tool.ShellToolName].(agent.ShellTool); !isAgent {
		t.Fatalf("base Shell is %T, want agent.ShellTool (child background parity)", base[tool.ShellToolName])
	}
}
