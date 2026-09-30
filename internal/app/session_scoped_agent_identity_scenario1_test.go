package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// writeScopedAgentDefFile writes a minimal agent-def markdown file with an
// explicit `tools:` allowlist (empty tools omits the field, exercising the
// "omitted tools:" resolution). It is this test file's own fixture writer
// (distinct from subagent_agent_router_test.go's writeAgentDefFile, which has
// no tools:/mcpServers: knobs) so Scenario 1's tests can pin an exact
// allowlist per def.
func writeScopedAgentDefFile(t *testing.T, dir, name string, tools []string, body string) {
	t.Helper()
	fm := "---\nname: " + name + "\ndescription: " + name + " specialist\n"
	if len(tools) > 0 {
		fm += "tools: [" + strings.Join(tools, ", ") + "]\n"
	}
	fm += "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(fm), 0o644); err != nil {
		t.Fatalf("write def %s: %v", name, err)
	}
}

// testAgentDefRootPolicy builds the ordinary main-session policy (AudienceMain,
// the shared mainRules/mainEvaluatorOptions this whole file's unit-level tests
// pass to buildAgentDefRootEngine) so those tests exercise the SAME governance
// construction the real factory (agentDefSessionEngineFactory) does.
func testAgentDefRootPolicy(cfg Config) port.PermissionPolicy {
	return permpolicy.NewPolicy(mainRules(cfg), nil, mainEvaluatorOptions(cfg)...)
}

// --- AC1.1 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_CatalogExactlyMatchesDef pins AC1.1:
// an agent-bound session's catalog is built EXCLUSIVELY from the resolved
// def's own tools (core) and mcpServers: (MCP) — never the deployment's
// default explorer catalog. baseSubagentTools(cfg) here carries far more than
// {Read, Grep} (Edit/Write/Copy/Move/Remove/Glob/WebFetch too), so an exact
// match proves the allowlist is a REPLACEMENT, not additive.
func TestSessionScopedAgentIdentity_Scenario1_CatalogExactlyMatchesDef(t *testing.T) {
	url := newMCPTestServer(t)
	cfg := Config{}
	def := agents.AgentDef{
		Name:       "reviewer",
		Tools:      []string{"Read", "Grep"},
		MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}
	base := baseSubagentTools(cfg)
	cat, _, _, mcpClose, _, _, _ := resolvedAgentDefCatalog(context.Background(), cfg, def, "test",
		"model-x", base, true /*allowMutating*/, false /*allowShell*/, nil, hookexec.New(nil), nil, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}

	want := []string{"Read", "Grep", "mcp__inline__echo"}
	sort.Strings(want)
	if onlyWant, onlyGot := diffNameSets(want, sortedNames(cat)); len(onlyWant) > 0 || len(onlyGot) > 0 {
		t.Fatalf("agent-bound root catalog is NOT exactly the def's own tools+mcpServers::\n  missing: %v\n  unexpected (leaked from the deployment default): %v",
			onlyWant, onlyGot)
	}
}

// --- AC1.3 / AC1.16 -----------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_OmittedToolsUsesChildBaseSet pins
// AC1.3: an omitted tools: resolves against the SAME bounded base set a
// Subagent child gets (baseSubagentTools(cfg)) — computed here directly from
// that shared function (data-driven, not a hard-coded literal), so a future
// change to the base set cannot silently desync this expectation from
// reality.
func TestSessionScopedAgentIdentity_Scenario1_OmittedToolsUsesChildBaseSet(t *testing.T) {
	cfg := Config{}
	def := agents.AgentDef{Name: "generalist"} // Tools omitted.
	base := baseSubagentTools(cfg)
	want := make([]string, 0, len(base))
	for name := range base {
		want = append(want, name)
	}
	sort.Strings(want)

	cat, _, _, mcpClose, _, _, _ := resolvedAgentDefCatalog(context.Background(), cfg, def, "test",
		"model-x", base, true /*allowMutating*/, false /*allowShell*/, nil, hookexec.New(nil), nil, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}
	if onlyWant, onlyGot := diffNameSets(want, sortedNames(cat)); len(onlyWant) > 0 || len(onlyGot) > 0 {
		t.Fatalf("omitted tools: is NOT exactly baseSubagentTools(cfg):\n  missing: %v\n  unexpected: %v", onlyWant, onlyGot)
	}
}

// TestSessionScopedAgentIdentity_Scenario1_ExplicitlyListedToolOutsideBaseSetStillDropped
// pins AC1.16: naming WebSearch/Schedule/Team (each present on the ordinary
// default catalog but absent from baseSubagentTools) does not grant them —
// scopedToolNamesMode drops any name outside the available base set
// unconditionally, whether tools: is omitted (previous test) or explicit
// (this one).
func TestSessionScopedAgentIdentity_Scenario1_ExplicitlyListedToolOutsideBaseSetStillDropped(t *testing.T) {
	cfg := Config{}
	def := agents.AgentDef{Name: "over-reacher", Tools: []string{"Read", "WebSearch", "Schedule", "Team", tool.ToolSearchName}}
	base := baseSubagentTools(cfg)

	cat, _, _, mcpClose, _, _, _ := resolvedAgentDefCatalog(context.Background(), cfg, def, "test",
		"model-x", base, true /*allowMutating*/, false /*allowShell*/, nil, hookexec.New(nil), nil, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}
	got := sortedNames(cat)
	if len(got) != 1 || got[0] != "Read" {
		t.Fatalf("tools: naming names outside baseSubagentTools(cfg) leaked into the catalog: got %v, want [Read]", got)
	}
}

// --- AC1.11 --------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_MutationFollowsDefTools pins
// AC1.11: mutation is available iff the def's tools: allows it — a session
// root is never forced read-only the way a Subagent delegate is. Read/Write
// (named) must be present; Edit (not named) must not.
func TestSessionScopedAgentIdentity_Scenario1_MutationFollowsDefTools(t *testing.T) {
	cfg := Config{}
	oa := mockllm.New(mockllm.TextTurn("x"))
	provReg := regForTest(oa, providerMock, "model-x")
	def := agents.AgentDef{Name: "writer", Tools: []string{"Read", "Write"}}
	base := baseSubagentTools(cfg)

	eng, mcpClose, _ := buildAgentDefRootEngine(context.Background(), cfg, def, "src", oa, "model-x",
		func() int { return 128000 }, base, false /*allowShell*/, nil, hookexec.New(nil), nil, nil,
		memstore.New(), testAgentDefRootPolicy(cfg), nil, nil, provReg, providerMock, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}
	if !eng.HasTool("Write") {
		t.Fatal("def's tools: lists Write — a session root must not force it read-only")
	}
	if !eng.HasTool("Read") {
		t.Fatal("Read expected")
	}
	if eng.HasTool("Edit") {
		t.Fatal("Edit is not named in tools: and must not appear")
	}
}

// --- AC1.15 ----------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_NoFSComposesWithDefCeiling pins
// AC1.15: under profile: "no-fs", filesystem tools and Shell stay absent even
// when the def's tools: lists them — the profile's AVAILABLE base
// (baseSubagentToolsNoFS) and the def's ceiling compose by intersection.
func TestSessionScopedAgentIdentity_Scenario1_NoFSComposesWithDefCeiling(t *testing.T) {
	cfg := Config{}
	def := agents.AgentDef{Name: "writer", Tools: []string{"Write", "Shell", "WebFetch"}}
	base := baseSubagentToolsNoFS()

	cat, _, _, mcpClose, _, _, _ := resolvedAgentDefCatalog(context.Background(), cfg, def, "test",
		"model-x", base, true /*allowMutating*/, false /*allowShell*/, nil, hookexec.New(nil), nil, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}
	got := sortedNames(cat)
	if len(got) != 1 || got[0] != "WebFetch" {
		t.Fatalf("no-fs base composed with the def's ceiling must keep ONLY WebFetch, got %v (Write/Shell must never be restored by the def's own allowlist)", got)
	}
}

// --- AC1.9 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_AuthorityMintedFromDefCatalog pins
// AC1.9: the minted session.Authority is derived from the def's own resolved
// catalog + resource-capability list — the def's inline-MCP tool is genuinely
// present in the Authority, never silently dropped the way a deployment
// build-time default catalog (which has never heard of this session-scoped
// server) would drop it.
func TestSessionScopedAgentIdentity_Scenario1_AuthorityMintedFromDefCatalog(t *testing.T) {
	url := newMCPTestServer(t)
	cfg := Config{}
	oa := mockllm.New(mockllm.TextTurn("x"))
	provReg := regForTest(oa, providerMock, "model-x")
	def := agents.AgentDef{
		Name:       "reviewer",
		Tools:      []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "inline", URL: url}},
	}
	base := baseSubagentTools(cfg)

	_, mcpClose, authority := buildAgentDefRootEngine(context.Background(), cfg, def, "src", oa, "model-x",
		func() int { return 128000 }, base, false /*allowShell*/, nil, hookexec.New(nil), nil, nil,
		memstore.New(), testAgentDefRootPolicy(cfg), nil, nil, provReg, providerMock, nil)
	if mcpClose != nil {
		defer func() { _ = mcpClose() }()
	}

	toolSet := map[string]bool{}
	for _, name := range authority.CapabilitySet.Tools {
		toolSet[name] = true
	}
	if !toolSet["Read"] {
		t.Fatalf("minted Authority missing Read: %v", authority.CapabilitySet.Tools)
	}
	if !toolSet["mcp__inline__echo"] {
		t.Fatalf("minted Authority does not include the def's own inline-MCP tool — it must be derived from the def's resolved catalog, not the deployment default: %v", authority.CapabilitySet.Tools)
	}

	// AC3.1: the minted Authority must carry a non-nil Ceiling matching this
	// SAME resolved catalog — GrantToolAuthority/CompleteWorkspaceEnrollment
	// (engine/session) enforce it only when set; a nil Ceiling here would make
	// their enforcement a structural no-op for every real agent-bound session,
	// silently defeating AC3.1/AC3.2 despite those tests passing in isolation
	// (they construct a Ceiling by hand rather than going through this mint path).
	if authority.Ceiling == nil {
		t.Fatalf("minted Authority has a nil Ceiling — GrantToolAuthority/CompleteWorkspaceEnrollment would treat this agent-bound session as unrestricted")
	}
	if !authority.Ceiling.Contains(authority.CapabilitySet) {
		t.Fatalf("minted Ceiling does not contain the minted CapabilitySet: ceiling=%v capability=%v", authority.Ceiling.Tools, authority.CapabilitySet.Tools)
	}
}

// --- AC1.4 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_UnknownDefRejected pins AC1.4: an
// unresolvable agent_definition_name is a loud error wrapping
// server.ErrInvalidArgument, mirroring how an unknown provider_id is handled
// today.
func TestSessionScopedAgentIdentity_Scenario1_UnknownDefRejected(t *testing.T) {
	cfg := Config{}
	oa := mockllm.New(mockllm.TextTurn("x"))
	provReg := regForTest(oa, providerMock, "model-x")
	assets := catalogAssets{agentReg: agents.NewRegistry(nil)} // no defs registered.

	factory := agentDefSessionEngineFactory(cfg, provReg, oa, memstore.New(), testAgentDefRootPolicy(cfg),
		hookexec.New(nil), nil, nil, assets, nil)
	_, err := factory(context.Background(), server.ProviderSelector{}, server.ProfileDefault, session.ModeDefault, session.Limits{}, "ghost-agent")
	if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("unknown agent_definition_name must be a loud InvalidArgument, got: %v", err)
	}
}

// --- AC1.5 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_DebugTargetMutualExclusionRejected
// pins AC1.5: agent_definition_name combined with debug_target_session_id is
// InvalidArgument — validated at the create boundary before any factory (here
// nofsRejectionService's minimal Service, mirroring
// TestCreateSessionUnknownProfileRejected) is even consulted.
func TestSessionScopedAgentIdentity_Scenario1_DebugTargetMutualExclusionRejected(t *testing.T) {
	svc := nofsRejectionService(t)
	_, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithAgentDefinitionName("release-reviewer"), server.WithDebugTarget("some-target"))
	if err == nil || !strings.Contains(err.Error(), "debug target") {
		t.Fatalf("agent_definition_name + debug_target_session_id must be rejected loudly, got: %v", err)
	}
}

// --- AC1.2 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_ClientMCPRejected pins AC1.2:
// CreateSessionRequest.mcp_servers is rejected as InvalidArgument whenever
// agent_definition_name is set — the def's own mcpServers: is the exclusive
// MCP scope. Validated at the create boundary before any connection attempt
// (ClientMCPFromWire's syntactic partition alone is enough to build the
// grant; the URL is never dialled).
func TestSessionScopedAgentIdentity_Scenario1_ClientMCPRejected(t *testing.T) {
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(defaultRules(), nil),
		Hooks:   hookexec.New(nil),
	})
	svc, err := newTestServerService(server.Config{
		Engine:            eng,
		Store:             memstore.New(),
		ClientMCPOnCreate: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	grant, err := svc.ClientMCPFromWire([]mcp.ClientServer{{Name: "srv", URL: "https://example.invalid/mcp"}})
	if err != nil {
		t.Fatalf("ClientMCPFromWire: %v", err)
	}
	_, err = svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithAgentDefinitionName("release-reviewer"), server.WithClientMCP(grant))
	if err == nil || !strings.Contains(err.Error(), "client-supplied MCP servers") {
		t.Fatalf("agent_definition_name + client MCP servers must be rejected loudly, got: %v", err)
	}
}

// --- AC1.6 / AC1.17 ------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_OrdinaryMainSessionBehavior pins
// AC1.6, through the FULL composition (app.Build → server.Service), offline:
// an agent-bound session's Write tool call — allowed by the def's own tools:
// but a default-mode Ask under the ordinary governance rules (defaultRules)
// — pauses to an ordinary permission ask (never child-shaped headless
// auto-deny), and resolves via the normal Service.ApproveRun path exactly
// like any other main session.
func TestSessionScopedAgentIdentity_Scenario1_OrdinaryMainSessionBehavior(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeScopedAgentDefFile(t, agentsDir, "writer", []string{"Read", "Write"}, "You are a writer.")

	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"hi"}`))),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir}, MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("writer"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := built.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askID string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			if ev.Ask.Tool != "Write" || string(ev.Ask.Call) != "w1" {
				t.Errorf("unexpected permission ask: %+v", ev.Ask)
			}
			if _, err := built.Service.ApproveRun(ctx, sess.ID, askID, session.VerdictAllowOnce, ""); err != nil {
				t.Fatalf("ApproveRun: %v", err)
			}
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if askID == "" {
		t.Fatal("an agent-bound session's Write call must raise an ordinary permission ask — never child-shaped headless auto-deny")
	}
	if _, statErr := os.Stat(filepath.Join(ws, "note.txt")); statErr != nil {
		t.Fatalf("Write must execute after the ordinary approve path resolved the ask: %v", statErr)
	}
}

// TestSessionScopedAgentIdentity_Scenario1_ResolvesViaExistingAgentDefSource
// pins AC1.17, through the FULL composition: agent_definition_name resolves
// through the SAME filesystem AgentDefSource (--agents-dir discovery) every
// other call site (startup Subagent construction, agent+model override,
// agent+read-write override) already uses — no separate/hard-coded lookup
// this call site could fall back to. A name absent from that SAME registry is
// rejected identically (AC1.4), proving there is no such fallback.
func TestSessionScopedAgentIdentity_Scenario1_ResolvesViaExistingAgentDefSource(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeScopedAgentDefFile(t, agentsDir, "fs-reviewer", []string{"Read"}, "Filesystem-discovered specialist.")

	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir}, MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("fs-reviewer"))
	if err != nil {
		t.Fatalf("agent_definition_name discovered via --agents-dir (the SAME AgentDefSource-backed registry every other call site uses) must resolve, got: %v", err)
	}
	if sess.AgentDefinitionName != "fs-reviewer" {
		t.Fatalf("AgentDefinitionName = %q, want %q", sess.AgentDefinitionName, "fs-reviewer")
	}

	_, err = built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("no-such-agent"))
	if err == nil {
		t.Fatal("an unresolvable name must be rejected even though a NAMED one just resolved via the same registry")
	}
}

// --- AC1.12 / AC1.13 / AC1.14 (Task C) ----------------------------------------

// writeAgentDefFrontmatterFile writes an agent-def markdown file from a raw
// frontmatter body, verbatim — this test file's fixture writer for
// frontmatter knobs writeScopedAgentDefFile doesn't expose (maxTurns/
// maxToolCalls/permissionMode/provider/model), needed by the Task C tests
// below.
func writeAgentDefFrontmatterFile(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + name + " specialist\n" + frontmatter + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write def %s: %v", name, err)
	}
}

// TestSessionScopedAgentIdentity_Scenario1_LimitsTightenOnly pins AC1.12,
// through the FULL composition: a request's Limits.max_turns/max_tool_calls
// may only LOWER the bound def's own configured caps (maxTurns: 10,
// maxToolCalls: 5) — never raise them — and an unset (zero) request field
// means "not supplied, inherit the def's cap," never "explicitly unlimited."
func TestSessionScopedAgentIdentity_Scenario1_LimitsTightenOnly(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeAgentDefFrontmatterFile(t, agentsDir, "capped", "tools: [Read]\nmaxTurns: 10\nmaxToolCalls: 5\n", "You are capped.")

	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir}, MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	cases := []struct {
		name          string
		request       session.Limits
		wantTurns     int
		wantToolCalls int
	}{
		{"unset request inherits the def's cap", session.Limits{}, 10, 5},
		{"looser request is tightened down to the def's cap", session.Limits{MaxTurns: 100, MaxToolCalls: 100}, 10, 5},
		{"tighter request narrows below the def's cap", session.Limits{MaxTurns: 3, MaxToolCalls: 2}, 3, 2},
		{"per-field: tighter turns, unset tool calls inherits the def's cap", session.Limits{MaxTurns: 3}, 3, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, tc.request,
				server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("capped"))
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if sess.Limits.MaxTurns != tc.wantTurns || sess.Limits.MaxToolCalls != tc.wantToolCalls {
				t.Fatalf("Limits = %+v, want MaxTurns=%d MaxToolCalls=%d (request was %+v)", sess.Limits, tc.wantTurns, tc.wantToolCalls, tc.request)
			}
		})
	}
}

// TestSessionScopedAgentIdentity_Scenario1_PermissionModeClampedNotRaised pins
// AC1.13, through the FULL composition: a request mode looser than the bound
// def's configured permissionMode is silently clamped to the def's value
// (plan < default < acceptEdits) — never rejected, never raised. A def
// configuring NO permissionMode imposes no ceiling at all (ordinary main
// session behavior — ADR 0353's ceiling is opt-in per def).
func TestSessionScopedAgentIdentity_Scenario1_PermissionModeClampedNotRaised(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeAgentDefFrontmatterFile(t, agentsDir, "planner", "tools: [Read]\npermissionMode: plan\n", "You plan only.")
	writeScopedAgentDefFile(t, agentsDir, "unrestricted", []string{"Read"}, "No mode ceiling.")

	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir}, MockProvider: llm,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	cases := []struct {
		name     string
		defName  string
		reqMode  session.PermissionMode
		wantMode session.PermissionMode
	}{
		{"looser default clamps down to the def's plan ceiling", "planner", session.ModeDefault, session.ModePlan},
		{"looser acceptEdits clamps down to the def's plan ceiling", "planner", session.ModeAccept, session.ModePlan},
		{"a request already at the def's ceiling is unchanged", "planner", session.ModePlan, session.ModePlan},
		{"a def with no permissionMode imposes no ceiling: acceptEdits is honoured", "unrestricted", session.ModeAccept, session.ModeAccept},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := built.Service.CreateSessionWithProfile(ctx, tc.reqMode, session.Limits{},
				server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName(tc.defName))
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if sess.Mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q (request was %q)", sess.Mode, tc.wantMode, tc.reqMode)
			}
		})
	}
}

// TestSessionScopedAgentIdentity_Scenario1_ProviderModelPairResolution pins
// AC1.14, directly against resolveAgentDefRootProviderModel (the unit-level
// seam agentDefSessionEngineFactory calls): all 5 cases of ADR 0353's
// provider/model pair resolution, plus the request-side "unknown provider_id"
// validation cases 3/4 already share with every other create path.
func TestSessionScopedAgentIdentity_Scenario1_ProviderModelPairResolution(t *testing.T) {
	oa := mockllm.New(mockllm.TextTurn("x"))
	or := mockllm.New(mockllm.TextTurn("x"))
	reg := twoProviderReg(oa, providerOpenAI, "parent-model", or, providerOpenRouter)
	cfg := Config{Model: "parent-model"}

	cases := []struct {
		name    string
		def     agents.AgentDef
		sel     server.ProviderSelector
		wantPID string
		wantMdl string
	}{
		{
			name:    "case 1: no override resolves the def's own base pair (def.Provider/def.Model)",
			def:     agents.AgentDef{Name: "a", Provider: providerOpenRouter, Model: "anthropic/claude-sonnet-4.5"},
			sel:     server.ProviderSelector{},
			wantPID: providerOpenRouter, wantMdl: "anthropic/claude-sonnet-4.5",
		},
		{
			name:    "case 1: no def pin, no override => deployment default",
			def:     agents.AgentDef{Name: "b"},
			sel:     server.ProviderSelector{},
			wantPID: providerOpenAI, wantMdl: "parent-model",
		},
		{
			name:    "case 2: model_id alone applies to the base pair's PROVIDER, never switches it",
			def:     agents.AgentDef{Name: "c"}, // base pair = openai/parent-model
			sel:     server.ProviderSelector{ModelID: "override-model"},
			wantPID: providerOpenAI, wantMdl: "override-model",
		},
		{
			name:    "case 3: provider_id alone switches provider and re-derives ITS OWN default model",
			def:     agents.AgentDef{Name: "d", Model: "pinned-for-a-different-provider"},
			sel:     server.ProviderSelector{ProviderID: providerOpenRouter},
			wantPID: providerOpenRouter, wantMdl: builtinDefaultModel[providerOpenRouter],
		},
		{
			name:    "case 4: both fields set uses the explicit pair verbatim",
			def:     agents.AgentDef{Name: "e", Provider: providerOpenAI, Model: "def-pinned-model"},
			sel:     server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: "explicit-model"},
			wantPID: providerOpenRouter, wantMdl: "explicit-model",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			childProvider, pid, mdl, windowFn, err := resolveAgentDefRootProviderModel(cfg, reg, tc.def, oa, providerOpenAI, tc.sel)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pid != tc.wantPID || mdl != tc.wantMdl {
				t.Fatalf("resolveAgentDefRootProviderModel = (%q,%q), want (%q,%q)", pid, mdl, tc.wantPID, tc.wantMdl)
			}
			if windowFn == nil {
				t.Fatal("windowFn must not be nil")
			}
			if childProvider == nil {
				t.Fatal("childProvider must not be nil")
			}
		})
	}

	t.Run("case 5: an unavailable definition-pinned provider fails session creation", func(t *testing.T) {
		def := agents.AgentDef{Name: "z", Provider: "vendor-unknown"}
		_, _, _, _, err := resolveAgentDefRootProviderModel(cfg, reg, def, oa, providerOpenAI, server.ProviderSelector{})
		if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("an unavailable definition-pinned provider must fail session creation (never silently instantiate on another provider), got: %v", err)
		}
	})

	t.Run("case 5 also covers case 2 (model_id alone still keys off the SAME unavailable base-pair provider)", func(t *testing.T) {
		def := agents.AgentDef{Name: "z2", Provider: "vendor-unknown"}
		_, _, _, _, err := resolveAgentDefRootProviderModel(cfg, reg, def, oa, providerOpenAI, server.ProviderSelector{ModelID: "some-model"})
		if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("case 2 shares case 1's base-pair provider, so it must fail identically when that provider is unavailable, got: %v", err)
		}
	})

	t.Run("an unknown REQUESTED provider_id (case 3) is InvalidArgument, mirroring every other create path", func(t *testing.T) {
		_, _, _, _, err := resolveAgentDefRootProviderModel(cfg, reg, agents.AgentDef{Name: "f"}, oa, providerOpenAI,
			server.ProviderSelector{ProviderID: "no-such-provider"})
		if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("unknown provider_id must be InvalidArgument, got: %v", err)
		}
	})

	t.Run("an unknown REQUESTED provider_id (case 4) is InvalidArgument, mirroring every other create path", func(t *testing.T) {
		_, _, _, _, err := resolveAgentDefRootProviderModel(cfg, reg, agents.AgentDef{Name: "g"}, oa, providerOpenAI,
			server.ProviderSelector{ProviderID: "no-such-provider", ModelID: "m"})
		if err == nil || !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("unknown provider_id must be InvalidArgument, got: %v", err)
		}
	})
}
