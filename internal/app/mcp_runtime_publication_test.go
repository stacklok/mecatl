package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func runtimeTestCandidate(revision uint64, names ...string) *mcpReconcileCandidate {
	tools := make([]mcpToolMeta, 0, len(names))
	for _, name := range names {
		tools = append(tools, mcpToolMeta{Name: name, Schema: `{}`})
	}
	return &mcpReconcileCandidate{generation: revision, tools: tools}
}

func TestMCPSourceReconciliation_Scenario2_AllOrNothingPublication(t *testing.T) {
	var retries atomic.Int32
	runtimes := newMCPRuntimeSet(func() { retries.Add(1) })
	first := runtimeTestCandidate(1, "mcp__one__read")
	if !runtimes.publish(nil, first) {
		t.Fatal("initial complete runtime was not published")
	}
	ctx, pin, err := runtimes.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := mcpRuntimeRevision(ctx); got != 1 {
		t.Fatalf("pinned revision = %d, want 1", got)
	}
	second := runtimeTestCandidate(2, "mcp__two__read")
	if !runtimes.publish(first, second) {
		t.Fatal("complete successor was not published")
	}
	if got := runtimes.currentRevision(); got != 2 {
		t.Fatalf("current revision = %d, want 2", got)
	}
	if first.isClosed() {
		t.Fatal("pinned retired runtime closed before drain")
	}
	pin()
	if !first.isClosed() {
		t.Fatal("retired runtime did not close after drain")
	}
	if retries.Load() != 0 {
		t.Fatalf("unexpected deferred retries = %d", retries.Load())
	}
	equalLeft := runtimeTestCandidate(10, "mcp__same__tool")
	equalLeft.resources = []mcp.Resource{{Server: "same", URI: "mcp://same"}}
	equalLeft.prompts = []mcp.Prompt{{Server: "same", Name: "prompt"}}
	equalRight := runtimeTestCandidate(11, "mcp__same__tool")
	equalRight.resources = append([]mcp.Resource(nil), equalLeft.resources...)
	equalRight.prompts = append([]mcp.Prompt(nil), equalLeft.prompts...)
	if !equalMCPCandidate(equalLeft, equalRight) {
		t.Fatal("equivalent complete runtime metadata was treated as changed")
	}
	equalRight.resources[0].URI = "mcp://changed"
	if equalMCPCandidate(equalLeft, equalRight) {
		t.Fatal("resource-only change did not require publication")
	}
	equalRight.resources[0] = equalLeft.resources[0]
	equalRight.prompts[0].Description = "changed"
	if equalMCPCandidate(equalLeft, equalRight) {
		t.Fatal("prompt-only change did not require publication")
	}

	shutdown := newMCPRuntimeSet(nil)
	shutdownCandidate := runtimeTestCandidate(1, "mcp__shutdown__tool")
	if !shutdown.publish(nil, shutdownCandidate) {
		t.Fatal("publish shutdown runtime")
	}
	_, releaseShutdownPin, err := shutdown.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan struct{})
	go func() {
		shutdown.close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("runtime shutdown completed before its operation pin drained")
	case <-time.After(20 * time.Millisecond):
	}
	if shutdownCandidate.isClosed() {
		t.Fatal("runtime shutdown closed a manager before operation drain")
	}
	releaseShutdownPin()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not complete after operation drain")
	}
	if !shutdownCandidate.isClosed() {
		t.Fatal("runtime shutdown did not close the drained manager")
	}
	runtimes.close()
}

func TestMCPSourceReconciliation_Scenario2_ProductionEngineRevisionMatrix(t *testing.T) {
	provider := mockllm.New()
	reg := regForTest(provider, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	runtimes := newMCPRuntimeSet(nil)
	first := runtimeTestCandidate(1)
	if !runtimes.publish(nil, first) {
		t.Fatal("publish first runtime")
	}
	assets := catalogAssets{mcpRuntimes: runtimes}
	factory := sessionEngineFactory(Config{Model: "test-model"}, reg, provider, store, policy, hookexec.New(nil), runtimes, prompt.RootAssembler{}, assets, nil)

	ctx, release, err := runtimes.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name    string
		profile server.SessionProfile
		mode    session.PermissionMode
		sel     server.ProviderSelector
		specs   []mcp.ServerConfig
	}{
		{name: "default", profile: server.ProfileDefault},
		{name: "selector", profile: server.ProfileDefault, sel: server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "test-model"}},
		{name: "no-fs", profile: server.ProfileNoFS},
		{name: "client-mcp", profile: server.ProfileDefault, specs: []mcp.ServerConfig{{Name: "client", URL: newMCPTestServer(t)}}},
		{name: "mode", profile: server.ProfileDefault, mode: session.ModePlan},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			result, buildErr := factory(ctx, row.sel, row.specs, row.profile, "", row.mode)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			defer result.Close()
			if result.RuntimeRevision != 1 {
				t.Fatalf("runtime revision = %d, want 1", result.RuntimeRevision)
			}
		})
	}
	target := session.New("debug-target", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "r1"}, session.Limits{}, time.Unix(0, 0))
	if err := store.Save(ctx, target); err != nil {
		t.Fatal(err)
	}
	debugFactory := debugSessionEngineFactory(Config{Model: "test-model"}, reg, provider, store, nil, policy, nil, runtimes)
	debugResult, err := debugFactory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if debugResult.RuntimeRevision != 1 {
		t.Fatalf("debug runtime revision = %d, want 1", debugResult.RuntimeRevision)
	}
	if _, err := debugFactory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, []string{"missing"}, []string{"mcp__missing__tool"}); err == nil {
		t.Fatal("debug factory accepted selected names absent from the pinned runtime")
	}
	release()
	runtimes.close()
}

func TestMCPSourceReconciliation_Scenario2_RuntimeConsistencyMatrix(t *testing.T) {
	var successorRetries atomic.Int32
	runtimes := newMCPRuntimeSet(func() { successorRetries.Add(1) })
	current := runtimeTestCandidate(1)
	if !runtimes.publish(nil, current) {
		t.Fatal("publish initial runtime")
	}
	pins := make([]func(), 0, maxMCPRetainedRuntimes)
	for i := 0; i < maxMCPRetainedRuntimes; i++ {
		_, release, err := runtimes.pin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, release)
		next := runtimeTestCandidate(uint64(i + 2))
		if !runtimes.publish(current, next) {
			t.Fatalf("publication %d unexpectedly deferred", i+2)
		}
		current = next
	}
	_, currentPin, err := runtimes.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pins = append(pins, currentPin)
	blocked := runtimeTestCandidate(uint64(maxMCPRetainedRuntimes + 2))
	if runtimes.publish(current, blocked) {
		t.Fatal("publication succeeded with full retirement set")
	}
	if !blocked.isClosed() {
		t.Fatal("unpublishable candidate was not closed")
	}
	pins[0]()
	if successorRetries.Load() != 1 {
		t.Fatalf("deferred successor retries after first drain = %d, want 1", successorRetries.Load())
	}
	for _, release := range pins[1:] {
		release()
	}
	if successorRetries.Load() != 1 {
		t.Fatalf("deferred successor was retried more than once: %d", successorRetries.Load())
	}
	runtimes.close()

	// Exercise the real Service run-entry and production session factory. The
	// cached shared engine is revision 1; a revision-2 root pin must rebuild it
	// before use, and that pin must outlive event drain until FinishRun.
	provider := mockllm.New(mockllm.TextTurn("done"))
	reg := regForTest(provider, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	runRuntimes := newMCPRuntimeSet(nil)
	rev1 := runtimeTestCandidate(1)
	if !runRuntimes.publish(nil, rev1) {
		t.Fatal("publish service revision 1")
	}
	assets := catalogAssets{mcpRuntimes: runRuntimes}
	productionFactory := sessionEngineFactory(Config{Model: "test-model"}, reg, provider, store, policy, hookexec.New(nil), runRuntimes, prompt.RootAssembler{}, assets, nil)
	initial := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test-model"})
	var builtRevision atomic.Uint64
	factory := func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		result, buildErr := productionFactory(ctx, sel, specs, profile, workspace, mode)
		if buildErr == nil {
			builtRevision.Store(result.RuntimeRevision)
		}
		return result, buildErr
	}
	svc, err := newTestServerService(server.Config{
		Engine: initial, Store: store, SessionEngine: factory,
		SharedEngineRevision: 1, OperationPin: runRuntimes.pin, OperationRevision: mcpRuntimeRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := runtimeTestCandidate(2)
	if !runRuntimes.publish(rev1, rev2) {
		t.Fatal("publish service revision 2")
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	if got := builtRevision.Load(); got != 2 {
		t.Fatalf("production run rebuilt revision = %d, want 2", got)
	}
	rev3 := runtimeTestCandidate(3)
	if !runRuntimes.publish(rev2, rev3) {
		t.Fatal("publish service revision 3")
	}
	if rev2.isClosed() {
		t.Fatal("root runtime pin drained before FinishRun")
	}
	svc.FinishRun(sess.ID, run)
	if !rev2.isClosed() {
		t.Fatal("root runtime pin did not drain at FinishRun")
	}
	runRuntimes.close()

	// Direct teams retain declarations only until RunTeam. Publication between
	// CreateTeam and RunTeam must retire the old runtime and build every member and
	// referenced specialist from the operation pin acquired by RunTeam.
	const teamTool = "mcp__svc__echo"
	teamRuntimes := newMCPRuntimeSet(nil)
	teamOldManager := connectMainManager(t, "svc", newMCPTestServerPrefixed(t, "team-old:"))
	teamOld := &mcpReconcileCandidate{manager: teamOldManager, generation: 1, tools: toolMetadata(teamOldManager.Tools())}
	if !teamRuntimes.publish(nil, teamOld) {
		t.Fatal("publish direct-team revision 1")
	}
	teamProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("team-call", teamTool, []byte(`{"text":"work"}`))),
		mockllm.TextTurn("member done"),
		mockllm.TextTurn("team report"),
		mockllm.ToolCallTurn(session.NewToolCall("team-call-new", teamTool, []byte(`{"text":"work"}`))),
		mockllm.TextTurn("new member done"),
		mockllm.TextTurn("new team report"),
	)
	teamReg := regForTest(teamProvider, providerOpenAI, "test-model")
	teamCfg := teamCfg(t)
	teamCfg.EnableTeams = true
	teamCfg.Model = "test-model"
	teamCfg.authorityEvaluator, _, err = selectAuthorityEvaluator("local", "")
	if err != nil {
		t.Fatal(err)
	}
	teamDefs := agents.NewRegistry([]agents.AgentDef{{
		Name: "team-ref", Description: "uses the pinned referenced service", Origin: tool.AgentOriginExplicit,
		MCPServers: []agents.AgentMCPServer{{Name: "svc"}},
	}})
	teamAssets := catalogAssets{mcpRuntimes: teamRuntimes, agentReg: teamDefs}
	var teamServerCfg server.Config
	applyTeamConfig(&teamServerCfg, teamCfg, teamReg, teamProvider, teamOldManager, teamDefs, nil, teamAssets)
	teamStore := memstore.New()
	rootNames := []string{teamTool}
	for name := range agent.MemberToolNames() {
		rootNames = append(rootNames, name)
	}
	teamServerCfg.Engine = initial
	teamServerCfg.Store = teamStore
	teamServerCfg.SharedEngineRoot = teamCfg.Workspace
	teamServerCfg.OperationPin = teamRuntimes.pin
	teamServerCfg.OperationRevision = mcpRuntimeRevision
	teamServerCfg.RootAuthority = func(session.SessionKind) session.Authority {
		return session.Authority{
			CapabilitySet: governance.CapabilitySet{Tools: rootNames, FileSystem: true},
			Provenance:    "test", DefinitionIdentity: "test",
		}
	}
	teamSvc, err := newTestServerService(teamServerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer teamSvc.Close()
	teamID, _, err := teamSvc.CreateTeamOnDefaultPlacement(context.Background(), "pinned", "use the service", 0, []agent.MemberSpec{{
		Name: "lead", Lead: true, AgentType: "team-ref", InitialPrompt: "call the service once",
	}})
	if err != nil {
		t.Fatal(err)
	}
	teamNewManager := connectMainManager(t, "svc", newMCPTestServerPrefixed(t, "team-new:"))
	teamNew := &mcpReconcileCandidate{manager: teamNewManager, generation: 2, tools: toolMetadata(teamNewManager.Tools())}
	if !teamRuntimes.publish(teamOld, teamNew) {
		t.Fatal("publish direct-team revision 2")
	}
	if !teamOld.isClosed() {
		t.Fatal("created-but-unrun direct team retained the retired runtime")
	}
	if _, err := teamSvc.RunTeam(context.Background(), teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatal(err)
	}
	member, err := teamStore.Load(context.Background(), agent.MemberSessionID(teamID, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if got := latestToolResult(member, "team-call"); !strings.Contains(got, "team-new:work") {
		t.Fatalf("direct RunTeam member did not use its run-time pin: %q", got)
	}
	newTeamID, _, err := teamSvc.CreateTeamOnDefaultPlacement(context.Background(), "current", "use the current service", 0, []agent.MemberSpec{{
		Name: "lead", Lead: true, AgentType: "team-ref", InitialPrompt: "call the service once",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teamSvc.RunTeam(context.Background(), newTeamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatal(err)
	}
	newMember, err := teamStore.Load(context.Background(), agent.MemberSessionID(newTeamID, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if got := latestToolResult(newMember, "team-call-new"); !strings.Contains(got, "team-new:work") {
		t.Fatalf("post-publication direct RunTeam member did not use the active runtime: %q", got)
	}
	teamRuntimes.close()
}

func TestMCPSourceReconciliation_Scenario3_NameAuthorityAvailabilityMatrix(t *testing.T) {
	const (
		name        = "mcp__svc__echo"
		unavailable = "tool is currently unavailable; do not retry unless the catalog changes"
	)
	ctx := context.Background()
	firstManager := connectMainManager(t, "svc", newMCPTestServerPrefixed(t, "first"))
	runtimes := newMCPRuntimeSet(nil)
	first := &mcpReconcileCandidate{manager: firstManager, generation: 1, tools: toolMetadata(firstManager.Tools())}
	if !runtimes.publish(nil, first) {
		t.Fatal("publish initial runtime")
	}
	emptyManager, err := mcp.NewCompleteManager(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := &mcpReconcileCandidate{manager: emptyManager, generation: 2}
	if !runtimes.publish(first, empty) {
		t.Fatal("publish successful empty removal")
	}

	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		requests = append(requests, req)
	})},
		mockllm.ToolCallTurn(session.NewToolCall("removed", name, []byte(`{"text":"removed"}`))),
		mockllm.TextTurn("removed observed"),
		mockllm.ToolCallTurn(session.NewToolCall("unknown", "mcp__svc__never-granted", []byte(`{}`))),
		mockllm.TextTurn("unknown observed"),
		mockllm.ToolCallTurn(session.NewToolCall("reappeared", name, []byte(`{"text":"again"}`))),
		mockllm.TextTurn("reappeared observed"),
	)
	reg := regForTest(provider, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeCLI, Tool: "*", Effect: governance.Allow}}, nil)
	authorityEvaluator, _, err := selectAuthorityEvaluator("local", "")
	if err != nil {
		t.Fatal(err)
	}
	assets := catalogAssets{mcpRuntimes: runtimes}
	factory := sessionEngineFactory(Config{Model: "test-model", authorityEvaluator: authorityEvaluator}, reg, provider, store, policy, hookexec.New(nil), runtimes, prompt.RootAssembler{}, assets, nil)
	sess := session.New("availability", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := sess.BindAuthority(session.Authority{
		CapabilitySet: governanceCapability(name),
		Provenance:    "test", DefinitionIdentity: "test",
	}); err != nil {
		t.Fatal(err)
	}
	env := testEnvironment(nofs.New(), nil)
	runOnce := func(wantRevision uint64) string {
		t.Helper()
		pinned, release, pinErr := runtimes.pin(ctx)
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		defer release()
		result, buildErr := factory(pinned, server.ProviderSelector{}, nil, server.ProfileNoFS, "", session.ModeDefault)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		defer result.Close()
		if result.RuntimeRevision != wantRevision {
			t.Fatalf("engine revision = %d, want %d", result.RuntimeRevision, wantRevision)
		}
		var content string
		var observed []session.EventType
		run := result.Engine.Run(pinned, sess, env, agent.RunRequest{Text: "exercise current MCP availability"})
		for ev := range run.Events() {
			observed = append(observed, ev.Type)
			if ev.Type == session.EvPermissionAsk {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
			if ev.Type == session.EvToolResult && ev.ToolResult != nil {
				content = ev.ToolResult.Content
			}
		}
		if content == "" {
			t.Logf("events=%v state=%s history=%+v", observed, sess.State, sess.Conversation.Messages)
		}
		return content
	}

	if got := runOnce(2); got != unavailable {
		t.Fatalf("removed granted call result = %q, want %q", got, unavailable)
	}
	if len(requests) == 0 || requestHasTool(requests[0], name) {
		t.Fatal("removed granted name remained in model tool specs")
	}
	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	if got := runOnce(2); got == unavailable || !strings.Contains(got, "unknown tool") {
		t.Fatalf("ungranted absent call result = %q, want ordinary unknown-tool error", got)
	}

	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	secondManager := connectMainManager(t, "svc", newMCPTestServerPrefixed(t, "second"))
	second := &mcpReconcileCandidate{manager: secondManager, generation: 3, tools: toolMetadata(secondManager.Tools())}
	if !runtimes.publish(empty, second) {
		t.Fatal("publish exact-name reappearance")
	}
	beforeReappearance := len(requests)
	if got := runOnce(3); !strings.Contains(got, "second") {
		t.Fatalf("reappeared same-name call result = %q, want new endpoint result", got)
	}
	reappearedAdvertised := false
	for _, req := range requests[beforeReappearance:] {
		reappearedAdvertised = reappearedAdvertised || requestHasTool(req, name)
	}
	if !reappearedAdvertised {
		t.Fatal("reappeared exact name was not restored to model specs")
	}
	if got := sess.Authority.CapabilitySet.Tools; len(got) != 1 || got[0] != name {
		t.Fatalf("same-name endpoint drift changed durable grant: %v", got)
	}
	runtimes.close()
}

func requestHasTool(req port.LLMRequest, name string) bool {
	for _, spec := range req.Tools {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func TestMCPSourceReconciliation_Scenario3_DelegationAndResumeNameSemantics(t *testing.T) {
	const (
		name        = "mcp__svc__echo"
		childID     = "subagent-parent-p1"
		unavailable = "tool is currently unavailable; do not retry unless the catalog changes"
	)
	ctx := context.Background()
	manager := connectMainManager(t, "svc", newMCPTestServerPrefixed(t, "pinned-old"))
	runtimes := newMCPRuntimeSet(nil)
	old := &mcpReconcileCandidate{manager: manager, generation: 1, tools: toolMetadata(manager.Tools())}
	if !runtimes.publish(nil, old) {
		t.Fatal("publish old runtime")
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"use the referenced service","agent":"ref-task"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("child-old", name, []byte(`{"text":"old"}`))),
		mockllm.TextTurn("old child done"),
		mockllm.TextTurn("parent old done"),
		mockllm.ToolCallTurn(session.NewToolCall("p2", "Subagent", []byte(`{"resume":"`+childID+`","prompt":"retry the same service"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("child-resume", name, []byte(`{"text":"resume"}`))),
		mockllm.TextTurn("resumed child done"),
		mockllm.TextTurn("parent resume done"),
	)
	reg := regForTest(provider, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	authorityEvaluator, _, err := selectAuthorityEvaluator("local", "")
	if err != nil {
		t.Fatal(err)
	}
	defs := agents.NewRegistry([]agents.AgentDef{{
		Name: "ref-task", Description: "reference test", Tools: []string{"Read"},
		MCPServers: []agents.AgentMCPServer{{Name: "svc"}},
	}})
	cfg := Config{Model: "test-model", authorityEvaluator: authorityEvaluator}
	assets := catalogAssets{mcpRuntimes: runtimes, agentReg: defs}
	factory := sessionEngineFactory(cfg, reg, provider, store, policy, hookexec.New(nil), runtimes, prompt.RootAssembler{}, assets, nil)
	parent := session.New("parent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 8}, time.Unix(0, 0))
	if err := parent.BindAuthority(session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Subagent", name}, RemainingDelegationDepth: 1, FileSystem: true},
		Provenance:    "test", DefinitionIdentity: "test",
	}); err != nil {
		t.Fatal(err)
	}
	drive := func(pinned context.Context, result server.SessionEngineResult) {
		t.Helper()
		run := result.Engine.Run(pinned, parent, memEnvironment("/ws"), agent.RunRequest{Text: "delegate"})
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk {
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
		}
	}

	oldCtx, oldRelease, err := runtimes.pin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldEngine, err := factory(oldCtx, server.ProviderSelector{}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	emptyManager, err := mcp.NewCompleteManager(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed := &mcpReconcileCandidate{manager: emptyManager, generation: 2}
	if !runtimes.publish(old, removed) {
		t.Fatal("publish removal while parent is pinned")
	}
	drive(oldCtx, oldEngine)
	child, err := store.Load(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if got := latestToolResult(child, "child-old"); !strings.Contains(got, "pinned-old") {
		t.Fatalf("pre-publication child did not inherit old runtime: %q", got)
	}
	if old.isClosed() {
		t.Fatal("parent-pinned runtime closed before delegated child completed")
	}
	oldRelease()
	if !old.isClosed() {
		t.Fatal("old runtime did not retire after parent and child drained")
	}
	_ = oldEngine.Close()

	if err := parent.Reopen(); err != nil {
		t.Fatal(err)
	}
	newCtx, newRelease, err := runtimes.pin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newEngine, err := factory(newCtx, server.ProviderSelector{}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer newEngine.Close()
	drive(newCtx, newEngine)
	resumed, err := store.Load(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if got := latestToolResult(resumed, "child-resume"); got != unavailable {
		t.Fatalf("resumed granted child call = %q, want %q", got, unavailable)
	}
	if got := mcpRuntimeRevision(newCtx); got != 2 {
		t.Fatalf("post-publication resume revision = %d, want 2", got)
	}
	newRelease()
	runtimes.close()
}

func latestToolResult(sess *session.Session, callID string) string {
	for i := len(sess.Conversation.Messages) - 1; i >= 0; i-- {
		result := sess.Conversation.Messages[i].ToolResult
		if result != nil && result.CallID == session.ToolCallID(callID) {
			return result.Content
		}
	}
	return ""
}

func governanceCapability(names ...string) governance.CapabilitySet {
	return governance.CapabilitySet{Tools: append([]string(nil), names...)}
}
