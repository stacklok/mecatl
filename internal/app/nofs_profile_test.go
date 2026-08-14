package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// noFSExcludedTools is the EXACT set of tool names the no-FS catalog profile
// removes relative to the default profile under a fully-loaded config. Precision
// is the anti-drift property: the no-FS set must equal the default set MINUS
// exactly these — nothing more (a family silently dropped from no-FS) and
// nothing less (a file tool leaking back in).
var noFSExcludedTools = []string{
	"Read", "Edit", "Write", "Grep", "Glob", "Bash",
	"Parallel",
	"BashStatus",         // the background-Bash companion: no Bash ⇒ no jobs to status/collect
	skills.DraftToolName, // "SkillDraft"
}

// TestNoFSCatalogProfile is the no-FS drift kill-switch (issue #55, the
// TestPerSessionCatalogMatchesSharedCatalog idiom): under a fully-loaded config
// — every optional family ON, a connected global MCP server with a resource, a
// shell — the no-FS catalog assembled by the REAL assembleCatalog over the REAL
// buildCatalog assets must be EXACTLY the shared (default-profile) set minus
// noFSExcludedTools. Both sides are computed from the production assembly, so a
// gate dropped from registerCoreTools, a family skipped from no-FS by accident,
// or a file tool re-registered under no-FS all fail this equality.
func TestNoFSCatalogProfile(t *testing.T) {
	ctx := context.Background()
	url := newMCPTestServerWithResource(t)

	cfg := fullyLoadedCfg(t)
	cfg.MCPServers = []mcp.ServerConfig{{Name: "globe", URL: url}}

	oa := mockllm.New(mockllm.TextTurn("OPENAI"))
	reg := regForTest(oa, providerOpenAI, cfg.Model)
	hooks := hookexec.New(nil)

	sharedCat, assets, _, _, mcpClose, err := buildCatalog(ctx, cfg, reg, oa, hooks, agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()
	if assets.globalMgr == nil {
		t.Fatal("buildCatalog connected no global MCP manager — fixture broken")
	}

	noFSCat, noFSClose := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, &assets, catalogSession{
		provider: oa, providerID: providerOpenAI, model: cfg.Model, narrate: false, noFS: true,
	})
	defer func() { _ = noFSClose() }()

	// want = shared − excluded; the excluded names must all EXIST in the shared
	// set (otherwise the exclusion list itself has drifted from reality).
	sharedSet := toolNameSet(sharedCat.Tools())
	want := map[string]struct{}{}
	for name := range sharedSet {
		want[name] = struct{}{}
	}
	for _, name := range noFSExcludedTools {
		if _, ok := sharedSet[name]; !ok {
			t.Fatalf("excluded tool %q is not in the fully-loaded shared catalog — the exclusion list drifted", name)
		}
		delete(want, name)
	}

	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)

	if onlyWant, onlyGot := diffNameSets(wantNames, sortedNames(noFSCat)); len(onlyWant) > 0 || len(onlyGot) > 0 {
		t.Fatalf("no-FS catalog is NOT exactly (default profile − %v):\n  missing from no-FS: %v\n  unexpectedly in no-FS: %v",
			noFSExcludedTools, onlyWant, onlyGot)
	}
}

// TestNoFSParallelAbsent pins the Parallel family specifically: a no-FS catalog
// under an EnableParallel config must not carry the Parallel tool (every branch
// is a filesystem fork), while the default profile over the same assets does.
func TestNoFSParallelAbsent(t *testing.T) {
	ctx := context.Background()
	cfg := fullyLoadedCfg(t)
	oa := mockllm.New(mockllm.TextTurn("x"))
	reg := regForTest(oa, providerOpenAI, cfg.Model)
	hooks := hookexec.New(nil)

	_, assets, _, _, mcpClose, err := buildCatalog(ctx, cfg, reg, oa, hooks, agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	defer mcpClose()

	noFSCat, closeFn := assembleCatalog(ctx, cfg, reg, memstore.New(), hooks, &assets, catalogSession{
		provider: oa, providerID: providerOpenAI, model: cfg.Model, noFS: true,
	})
	defer func() { _ = closeFn() }()
	if _, ok := toolNameSet(noFSCat.Tools())["Parallel"]; ok {
		t.Fatal("no-FS catalog carries Parallel — branch forks are filesystem acts and must be absent")
	}
	// Delegation stays available: Subagent and Team survive the profile.
	for _, name := range []string{"Subagent", "Team", "Skill", "WebFetch", "FetchMcpResource"} {
		if _, ok := toolNameSet(noFSCat.Tools())[name]; !ok {
			t.Errorf("no-FS catalog is missing %q — only file tools/Bash/Parallel/SkillDraft may be excluded", name)
		}
	}
	// PresentPlan (issue #206 Wave 3) is registered in the no-FS catalog too — it
	// is a signalling affordance, NOT a filesystem act, so it is NOT in the
	// noFSExcludedTools set {Read,Edit,Write,Grep,Glob,Bash,Parallel,SkillDraft}.
	// (It implements tool.PlanOnly, so the mode projection hides it outside plan
	// mode — but it must be REGISTERED so the no-FS and shared name-sets agree.)
	if _, ok := toolNameSet(noFSCat.Tools())["PresentPlan"]; !ok {
		t.Error("no-FS catalog is missing PresentPlan — it is not a filesystem act and must be registered (advertised only in plan mode)")
	}
}

// TestApplyNoFSPosture pins the no-FS prompt posture helper: the FS env facts
// are zeroed (no cwd/shell/git for an <env> block to lie about) and the note is
// appended to the Role (with the DefaultRole fallback when Role was empty).
func TestApplyNoFSPosture(t *testing.T) {
	pc := prompt.Config{Env: prompt.Env{Cwd: "/srv/repo", Shell: "/bin/sh", GitStatus: "branch main", OS: "linux", Model: "m"}}
	got := applyNoFSPosture(pc, noFSPostureNote)
	if got.Env.Cwd != "" || got.Env.Shell != "" || got.Env.GitStatus != "" {
		t.Errorf("applyNoFSPosture left FS env facts: cwd=%q shell=%q git=%q", got.Env.Cwd, got.Env.Shell, got.Env.GitStatus)
	}
	if got.Env.OS != "linux" || got.Env.Model != "m" {
		t.Error("applyNoFSPosture must not touch non-FS env facts")
	}
	if !strings.HasPrefix(got.Role, prompt.DefaultRole()) {
		t.Error("empty Role must fall back to DefaultRole before the note (the applyUntrustedMemberShellNote idiom)")
	}
	if !strings.Contains(got.Role, "NO filesystem") {
		t.Errorf("posture Role must carry the \"NO filesystem\" note, got:\n%s", got.Role)
	}

	// A pre-composed Role is preserved, note appended.
	pc.Role = "custom role"
	if got := applyNoFSPosture(pc, noFSMemberNote); !strings.HasPrefix(got.Role, "custom role") || !strings.Contains(got.Role, "NO filesystem") {
		t.Errorf("pre-composed Role must be preserved with the note appended, got:\n%s", got.Role)
	}
}

// TestNoFSSubagentChildInheritsNoFS proves the no-FS Subagent child genuinely
// runs the file-less surface: driven through the REAL buildSubagentTool
// (noFS=true) with a scripted child, the child's Read and Bash calls come back
// as unknown-tool errors (no FS tools, no shell — even though the config HAS a
// shell, proving the no-FS gate beats the shell wiring, and without a Bash
// child no forker is ever wired: the two move together) while its Remember call
// AND its global-MCP call succeed against the shared store/manager. The tool
// specs OFFERED to the child model carry WebFetch + FetchMcpResource + the
// global MCP tool and no file tool. The Spec is the honest no-FS one.
func TestNoFSSubagentChildInheritsNoFS(t *testing.T) {
	ctx := context.Background()
	memStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	url := newMCPTestServerWithResource(t)
	a := catalogAssets{memStore: memStore, globalMgr: connectMainManager(t, "globe", url)}

	var (
		toolsMu      sync.Mutex
		offeredTools []string
	)
	provider := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
			toolsMu.Lock()
			defer toolsMu.Unlock()
			if offeredTools != nil {
				return // capture once; every child turn offers the same catalog
			}
			for _, ts := range req.Tools {
				offeredTools = append(offeredTools, ts.Name)
			}
		})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "r1", Name: "Read", Args: json.RawMessage(`{"file_path":"/etc/passwd"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "b1", Name: "Bash", Args: json.RawMessage(`{"command":"echo hi"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "m1", Name: "Remember", Args: json.RawMessage(`{"key":"note/x","value":"hello"}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "e1", Name: "mcp__globe__echo", Args: json.RawMessage(`{"text":"hi"}`)}),
		mockllm.TextTurn("nofs child summary"),
	)
	cfg := Config{Model: "m", Shell: "/bin/sh", TrustProject: true, Diagnostics: port.NopDiagnostics{}}
	subTool, closeFn := buildSubagentTool(ctx, cfg, regForTest(provider, providerMock, cfg.Model), provider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, a, true)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	// The honest no-FS spec (mutation guard c rides the engine-level test; this
	// pins the composition actually sets the option).
	if desc := subTool.Spec().Description; strings.Contains(desc, "Read/Grep/Glob") || !strings.Contains(desc, "NO filesystem") {
		t.Fatalf("no-FS Subagent spec must describe the file-less surface, got:\n%s", desc)
	}

	var (
		mu     sync.Mutex
		toolEv []session.SubagentPayload
	)
	emit := func(ev session.Event) {
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil {
			mu.Lock()
			toolEv = append(toolEv, *ev.Subagent)
			mu.Unlock()
		}
	}
	ot, ok := subTool.(interface {
		ExecuteObserved(context.Context, session.ToolCall, tool.Environment, func(session.Event)) (session.ToolResult, error)
	})
	if !ok {
		t.Fatal("Subagent tool does not implement ExecuteObserved")
	}
	res, err := ot.ExecuteObserved(ctx,
		session.NewToolCall("t1", "Subagent", json.RawMessage(`{"prompt":"investigate without files","description":"nofs probe"}`)),
		testEnvironment(nofs.New(), nil), emit)
	if err != nil {
		t.Fatalf("ExecuteObserved: %v", err)
	}
	if !strings.Contains(res.Content, "nofs child summary") {
		t.Fatalf("Subagent result must carry the child summary, got:\n%s", res.Content)
	}

	saw := map[string]bool{}
	for _, p := range toolEv {
		// ADR 0079: the projection also carries message.delta / result text previews
		// with no ToolName — only tool.call / tool.result projections are
		// tool-attributed. The ok/error outcome is attributed on the tool.RESULT
		// projection (a tool.call preview always reads IsError=false).
		if p.InnerKind != session.EvToolCall && p.InnerKind != session.EvToolResult {
			continue
		}
		if p.InnerKind == session.EvToolResult {
			switch p.ToolName {
			case "Read", "Bash":
				if !p.IsError {
					t.Errorf("no-FS child's %s call SUCCEEDED — the file/shell tool leaked into the child catalog", p.ToolName)
				}
			case "Remember":
				if p.IsError {
					t.Error("no-FS child's Remember call failed — the memory six must be in the child catalog")
				}
			case "mcp__globe__echo":
				if p.IsError {
					t.Error("no-FS child's global-MCP call failed — the shared global MCP tools must be in the child catalog")
				}
			}
		}
		saw[p.ToolName] = true
	}
	for _, name := range []string{"Read", "Bash", "Remember", "mcp__globe__echo"} {
		if !saw[name] {
			t.Errorf("scripted child call %q never surfaced on subagent.tool — script not exercised", name)
		}
	}

	// The catalog the child MODEL is offered: WebFetch + FetchMcpResource + the
	// global MCP tool are present, no file tool / shell is.
	toolsMu.Lock()
	offered := map[string]bool{}
	for _, name := range offeredTools {
		offered[name] = true
	}
	toolsMu.Unlock()
	for _, want := range []string{"WebFetch", "FetchMcpResource", "mcp__globe__echo", "CallMcpWithQuery"} {
		if !offered[want] {
			t.Errorf("child request tool specs are missing %q (got %v)", want, offeredTools)
		}
	}
	for _, banned := range []string{"Read", "Edit", "Write", "Grep", "Glob", "Bash"} {
		if offered[banned] {
			t.Errorf("child request tool specs OFFER %q — a file/shell tool leaked into the no-FS child surface", banned)
		}
	}
}

// runEvents drains a run collecting ALL events (drainRun returns only the final
// text), auto-allowing any permission ask.
func runEvents(run *agent.Run) []session.Event {
	var events []session.Event
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		events = append(events, ev)
	}
	return events
}

// TestCreateSessionNoFSProfileNoWorkspace is the no-FS e2e through the FULL
// composition (app.Build → server.Service), offline: a "no-fs" profile session
// is created with an EMPTY workspace, a chat turn runs, a memory tool works, a
// model "Read" attempt yields the unknown-tool error WITH its EvToolCall card
// emitted first (card-before-the-gate holds on the unknown-tool path), and the
// system prompt carries the no-FS posture note (model-visible discoverability).
func TestCreateSessionNoFSProfileNoWorkspace(t *testing.T) {
	ctx := context.Background()

	var (
		reqMu   sync.Mutex
		systems []string
	)
	built, err := Build(ctx, Config{
		Workspace:           t.TempDir(), // the SERVER still has a default workspace for the shared engine
		NoSoul:              true,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith(
				[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
					reqMu.Lock()
					systems = append(systems, req.System.Render())
					reqMu.Unlock()
				})},
				mockllm.ToolCallTurn(session.ToolCall{ID: "r1", Name: "Read", Args: json.RawMessage(`{"file_path":"main.go"}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "m1", Name: "Remember", Args: json.RawMessage(`{"key":"note/nofs","value":"works"}`)}),
				mockllm.TextTurn("nofs-done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile(no-fs, empty workspace): %v", err)
	}
	if sess.Workspace != "" {
		t.Fatalf("no-fs session persisted Workspace = %q, want \"\"", sess.Workspace)
	}

	run, err := svc.StartRun(ctx, sess.ID, "do a file-free check")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	events := runEvents(run)

	// Card-before-the-gate on the unknown-tool path: the Read EvToolCall card
	// precedes its error result.
	cardIdx, resIdx := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvToolCall && ev.ToolCall != nil && ev.ToolCall.ID == "r1" {
			cardIdx = i
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "r1" {
			resIdx = i
		}
	}
	if cardIdx == -1 || resIdx == -1 || cardIdx >= resIdx {
		t.Fatalf("Read must get an EvToolCall card (idx %d) BEFORE its result (idx %d)", cardIdx, resIdx)
	}
	if !unknownToolResult(events, "r1") {
		t.Fatal("a no-FS session's Read call must come back as an unknown-tool error")
	}
	// The memory tool genuinely works.
	if !sawDispatchedTool(events, "m1") {
		t.Fatal("Remember did not dispatch — the memory six must be in the no-FS catalog")
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "m1" && ev.ToolResult.IsError {
			t.Fatalf("Remember failed in the no-FS session: %s", ev.ToolResult.Content)
		}
	}
	// Clean terminal with the scripted final text.
	final := ""
	for _, ev := range events {
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "nofs-done" {
		t.Fatalf("final result = %q, want nofs-done", final)
	}

	// Model-visible posture: the system prompt carries the no-FS note (this is
	// the observable for mutation guard b's sibling — drop the posture append
	// and this fails).
	reqMu.Lock()
	defer reqMu.Unlock()
	if len(systems) == 0 {
		t.Fatal("no LLM request captured")
	}
	for _, sys := range systems {
		// The FULL note, not the bare "NO filesystem" substring: the Subagent
		// tool's no-fs description (embedded in the system prompt) also says
		// "NO filesystem", which made the substring oracle VACUOUS — proven by
		// mutation (dropping applyNoFSPosture passed the weaker assert).
		if !strings.Contains(sys, noFSPostureNote) {
			t.Fatalf("system prompt missing the no-FS posture note, got:\n%s", sys)
		}
	}
}

// TestCreateSessionNoFSRejectsWorkspace: the contradictory no-fs + workspace
// combination is rejected loudly (never resolved by dropping either field).
func TestCreateSessionNoFSRejectsWorkspace(t *testing.T) {
	svc := nofsRejectionService(t)
	_, err := svc.CreateSessionWithProfile(context.Background(), "/some/where", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err == nil || !strings.Contains(err.Error(), "must not carry a workspace") {
		t.Fatalf("no-fs + workspace must be rejected loudly, got: %v", err)
	}
}

// TestCreateSessionDefaultProfileRequiresWorkspace: today's behaviour is
// preserved byte-for-byte for the default profile — an empty workspace is still
// rejected.
func TestCreateSessionDefaultProfileRequiresWorkspace(t *testing.T) {
	svc := nofsRejectionService(t)
	_, err := svc.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault)
	if err == nil || !strings.Contains(err.Error(), "workspace is required") {
		t.Fatalf("default profile + empty workspace must be rejected, got: %v", err)
	}
}

// TestCreateSessionUnknownProfileRejected: an unknown profile is a loud
// invalid-argument, never a silent fallback to the default profile.
func TestCreateSessionUnknownProfileRejected(t *testing.T) {
	svc := nofsRejectionService(t)
	_, err := svc.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.SessionProfile("ram-only"))
	if err == nil || !strings.Contains(err.Error(), "unknown session profile") {
		t.Fatalf("unknown profile must be rejected loudly, got: %v", err)
	}
}

// nofsRejectionService builds a minimal real Service for the create-validation
// tests (no provider work is ever reached — validation rejects first).
func nofsRejectionService(t *testing.T) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(defaultRules(), nil),
		Hooks:   hookexec.New(nil),
	})
	svc, err := server.NewService(server.Config{
		Engine:     eng,
		Store:      memstore.New(),
		Workspaces: func(string) tool.Workspace { return nofs.New() },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestNoFSSessionSurvivesRestartE2E is the restart-escalation guard through the
// FULL composition (issue #55 HIGH): a no-fs session persisted to a durable
// store, used, and then picked up by a SECOND app.Build over the SAME store
// (the process-restart simulation) must be REHYDRATED — the post-restart run
// rides a catalog WITHOUT FS tools (a model Read comes back unknown-tool), the
// no-FS posture note is in the system prompt, the memory tools still work, and
// the empty workspace root NEVER reaches the shared osfs factory (the
// composition chokepoint's ERROR line never fires). The rehydrated engine is
// the DEFAULT provider's no-fs engine: the snapshot persists no provider/model
// selector, so the zero-selector factory call is the sound floor.
// Mutation-verified: removing the rehydration branch in StartRunContent fails
// this test (the Read dispatches against the shared FS engine).
func TestNoFSSessionSurvivesRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir() // the SERVER's default workspace for the shared engine

	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// "Before the restart": create the no-fs session, run one clean turn.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("pre-restart-done"))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "first file-free turn")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	runEvents(run1)
	built1.Close() // the "process exit": every in-memory registration dies here

	// "After the restart": a brand-new Build over the SAME durable store.
	var (
		reqMu   sync.Mutex
		systems []string
	)
	diag := newCapturingDiagnostics()
	cfg2 := baseCfg()
	cfg2.Diagnostics = diag
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.NewWith(
			[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				reqMu.Lock()
				systems = append(systems, req.System.Render())
				reqMu.Unlock()
			})},
			mockllm.ToolCallTurn(session.ToolCall{ID: "r1", Name: "Read", Args: json.RawMessage(`{"file_path":"main.go"}`)}),
			mockllm.ToolCallTurn(session.ToolCall{ID: "m1", Name: "Remember", Args: json.RawMessage(`{"key":"note/restart","value":"survived"}`)}),
			mockllm.TextTurn("rehydrated-done"),
		)
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	run2, err := built2.Service.StartRunContent(ctx, sess.ID, "post-restart file-free turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	events := runEvents(run2)

	// The rehydrated catalog has NO FS tools: Read is an unknown-tool error.
	if !unknownToolResult(events, "r1") {
		t.Fatal("post-restart Read did NOT come back unknown-tool — the no-fs session escalated onto a filesystem catalog")
	}
	// The memory six still work over the same durable memory dir.
	if !sawDispatchedTool(events, "m1") {
		t.Fatal("post-restart Remember did not dispatch — the rehydrated catalog lost the memory six")
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "m1" && ev.ToolResult.IsError {
			t.Fatalf("post-restart Remember failed: %s", ev.ToolResult.Content)
		}
	}
	final := ""
	for _, ev := range events {
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "rehydrated-done" {
		t.Fatalf("post-restart final = %q, want rehydrated-done", final)
	}

	// The model-visible posture survived the restart.
	reqMu.Lock()
	if len(systems) == 0 {
		reqMu.Unlock()
		t.Fatal("no post-restart LLM request captured")
	}
	for _, sys := range systems {
		// The FULL note (the bare substring is satisfied by the Subagent tool
		// description too — see TestCreateSessionNoFSProfileNoWorkspace).
		if !strings.Contains(sys, noFSPostureNote) {
			reqMu.Unlock()
			t.Fatalf("post-restart system prompt missing the no-FS posture note:\n%s", sys)
		}
	}
	reqMu.Unlock()

	// The empty root never reached the shared osfs factory: the composition
	// chokepoint (osfsWorkspaceFactory's "" intercept) never had to fire.
	if got := diag.countContaining("EMPTY root reached the shared osfs factory"); got != 0 {
		t.Fatalf("the osfs factory chokepoint fired %d time(s) — the rehydration seam let the empty root through", got)
	}
}

// TestOsfsWorkspaceFactoryEmptyRootIntercepted pins the composition-level
// defensive chokepoint: an EMPTY root handed to the shared workspace factory —
// by construction a no-fs session that somehow bypassed its per-session
// override — must NEVER open osfs over the server process's cwd. It serves the
// honest no-filesystem workspace and logs an ERROR (reaching it means an
// upstream no-fs guard regressed). A non-empty root still opens osfs normally.
func TestOsfsWorkspaceFactoryEmptyRootIntercepted(t *testing.T) {
	diag := newCapturingDiagnostics()
	factory := osfsWorkspaceFactory(diag)

	ws := factory("")
	if _, ok := ws.(nofs.Workspace); !ok {
		t.Fatalf("factory(\"\") = %T, want the nofs.Workspace intercept (an osfs workspace here is the cwd escalation)", ws)
	}
	if got := diag.countContaining("EMPTY root reached the shared osfs factory"); got != 1 {
		t.Fatalf("the empty-root intercept logged %d time(s), want exactly 1 (it must be loud)", got)
	}

	dir := t.TempDir()
	realWS := factory(dir)
	if realWS == nil {
		t.Fatal("factory(real dir) returned nil — the intercept must not affect the normal path")
	}
	if _, ok := realWS.(nofs.Workspace); ok {
		t.Fatal("factory(real dir) returned the nofs workspace — the intercept over-matched")
	}
}

// TestNoFSChildCatalogExactDelta is the drift kill-switch for the no-fs CHILD
// surface (the TestNoFSCatalogProfile idiom one level down): noFSChildCatalog
// must be EXACTLY {the capability-aware memory families} ∪ {WebFetch, FetchMcpResource, WebSearch}
// ∪ {the global MCP tools} — nothing more (a file tool or shell leaking into
// delegation children) and nothing less (a family silently dropped from the
// children). Mutation-verified (removing a family from noFSChildCatalog fails
// the equality).
func TestNoFSChildCatalogExactDelta(t *testing.T) {
	url := newMCPTestServerWithResource(t)
	mgr := connectMainManager(t, "globe", url)
	memStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(memStore): %v", err)
	}
	userStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(userModelStore): %v", err)
	}
	a := catalogAssets{memStore: memStore, userModelStore: userStore, globalMgr: mgr}

	want := []string{
		// Base and lifecycle tools over the same flocked shared stores.
		memory.RememberToolName, memory.RecallToolName, memory.SearchMemoryToolName,
		memory.InspectMemoryToolName, memory.ForgetMemoryToolName, memory.UndoMemoryToolName,
		memory.RememberUserToolName, memory.RecallUserToolName, memory.SearchUserModelToolName,
		memory.InspectUserMemoryToolName, memory.ForgetUserMemoryToolName, memory.UndoUserMemoryToolName,
		// The no-fs core tier: WebFetch + FetchMcpResource (outbound reads) + WebSearch
		// (search-then-fetch discovery). FetchMcpResource is an outbound read with no
		// filesystem need (issue #223 Phase 2).
		"WebFetch", "FetchMcpResource", "WebSearch",
		// CallMcpWithQuery (issue #223): the fail-closed escape hatch for an over-cap
		// structured MCP result. Cloud-native portable (no disk), so a no-FS child that
		// hits the fail-closed error has the tool to recover — no dead end.
		"CallMcpWithQuery",
	}
	for _, mt := range mgr.Tools() {
		want = append(want, mt.Spec().Name)
	}
	sort.Strings(want)

	cat := noFSChildCatalog(context.Background(), Config{Diagnostics: port.NopDiagnostics{}}, a)
	if onlyWant, onlyGot := diffNameSets(want, sortedNames(cat)); len(onlyWant) > 0 || len(onlyGot) > 0 {
		t.Fatalf("no-fs CHILD catalog is NOT exactly {capability-aware memory families} ∪ {WebFetch, FetchMcpResource, WebSearch} ∪ {global MCP}:\n  missing: %v\n  unexpected: %v",
			onlyWant, onlyGot)
	}
}

// TestSelectorSessionSurvivesRestartE2E is the cloud-native Phase 1 falsifiable
// gate through the FULL composition (app.Build → server.Service), offline: a
// session bound to a NON-default provider/model selector with a PARTIALLY-consumed
// token budget is created and run over a durable store, the process "exits"
// (built1.Close), a SECOND app.Build resumes over the SAME store, and the
// post-restart run must demonstrate three legs:
//
//	(a) same catalog        — a no-fs selector session: a model Read is unknown-tool,
//	                          the memory six still dispatch.
//	(b) SAME model          — the post-restart request resolves to the PERSISTED
//	                          selector's provider+model, NOT the default provider.
//	(c) budget continues    — the prior pre-restart spend (persisted in the snapshot
//	                          and accumulated by RecordUsage) survives the restart, so
//	                          the post-restart run trips StopBudget at the boundary.
//
// Mutation-verified per leg:
//   - (a)/(b): widening the rehydration trigger to pass ProviderSelector{} (instead
//     of the persisted labels) routes the run onto the DEFAULT provider; the model
//     assertion fails.
//   - (c): changing the budget check from `budgetExhausted(r, sess.Usage)` to
//     `budgetExhausted(r, total)` (the per-run delta) re-grants a fresh budget; the
//     run does NOT trip StopBudget and the model is called.
func TestSelectorSessionSurvivesRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir() // the SERVER's default workspace for the shared engine

	const selectorModel = "anthropic/claude-3.5-sonnet"
	// budget is sized so the SEEDED pre-restart spend (450) leaves room for the
	// post-restart run to execute its catalog-proving turns (Read unknown-tool +
	// Remember) and only THEN cross the ceiling — proving legs (a)/(b) on the
	// post-restart turns AND leg (c) at the later boundary, all in one run.
	const budget = 900

	// modelByProvider lets the per-provider constructor capture which provider+model
	// each request landed on, keyed by the provider id the registry constructed.
	type capture struct {
		mu     sync.Mutex
		models []string
	}
	var openaiCap, openrouterCap capture
	record := func(c *capture) mockllm.Option {
		return mockllm.WithRequestObserver(func(req port.LLMRequest) {
			c.mu.Lock()
			c.models = append(c.models, req.Model)
			c.mu.Unlock()
		})
	}

	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			MaxRunTokens:        budget,
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai", "OPENROUTER_API_KEY": "sk-openrouter"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// "Before the restart": create the selector (openrouter) session, run one turn
	// that consumes most of the budget.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, id, _, _ string) port.LLMProvider {
		if id == providerOpenRouter {
			return mockllm.NewWith(
				[]mockllm.Option{record(&openrouterCap)},
				mockllm.ChunksTurn(
					mockllm.TextChunk("pre-restart-done"),
					mockllm.UsageChunk(session.Usage{InputTokens: 300, OutputTokens: 150}), // 450 < 500
					mockllm.DoneChunk(session.StopEndTurn),
				),
			)
		}
		return mockllm.NewWith([]mockllm.Option{record(&openaiCap)}, mockllm.TextTurn("openai-default"))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: selectorModel}, server.ProfileNoFS)
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSessionWithProfile(selector+no-fs): %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "first selector turn")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	runEvents(run1)
	built1.Close() // the "process exit": every in-memory registration dies here

	// Sanity: the pre-restart turn ran on openrouter+selectorModel, never on openai.
	openrouterCap.mu.Lock()
	preModels := append([]string(nil), openrouterCap.models...)
	openrouterCap.mu.Unlock()
	if len(preModels) == 0 || preModels[0] != selectorModel {
		t.Fatalf("pre-restart request models = %v, want first == %q", preModels, selectorModel)
	}

	// "After the restart": a brand-new Build over the SAME durable store.
	diag := newCapturingDiagnostics()
	cfg2 := baseCfg()
	cfg2.Diagnostics = diag
	cfg2.providerConstructor = func(_ Config, id, _, _ string) port.LLMProvider {
		if id == providerOpenRouter {
			return mockllm.NewWith(
				[]mockllm.Option{record(&openrouterCap)},
				// Turn 1: a Read (unknown-tool on the no-fs catalog) + usage. Seeded
				// 450 + 200 = 650 < 900, so the next boundary proceeds.
				mockllm.ChunksTurn(
					mockllm.ToolCallChunk(session.ToolCall{ID: "r1", Name: "Read", Args: json.RawMessage(`{"file_path":"main.go"}`)}),
					mockllm.UsageChunk(session.Usage{InputTokens: 120, OutputTokens: 80}),
					mockllm.DoneChunk(session.StopEndTurn),
				),
				// Turn 2: a Remember (dispatches on the no-fs catalog) + usage. 650 +
				// 300 = 950 >= 900, so the NEXT boundary trips StopBudget.
				mockllm.ChunksTurn(
					mockllm.ToolCallChunk(session.ToolCall{ID: "m1", Name: "Remember", Args: json.RawMessage(`{"key":"note/restart","value":"survived"}`)}),
					mockllm.UsageChunk(session.Usage{InputTokens: 200, OutputTokens: 100}),
					mockllm.DoneChunk(session.StopEndTurn),
				),
				// Turn 3 would run only if the budget did NOT survive; the (c) leg
				// asserts the run stopped on StopBudget before reaching it.
				mockllm.TextTurn("should-not-reach"),
			)
		}
		return mockllm.NewWith([]mockllm.Option{record(&openaiCap)}, mockllm.TextTurn("openai-default"))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	preOpenRouterCalls := len(preModels)
	run2, err := built2.Service.StartRunContent(ctx, sess.ID, "post-restart selector turn", nil)
	if err != nil {
		t.Fatalf("StartRunContent (post-restart): %v", err)
	}
	events := runEvents(run2)

	// Leg (a): same (no-fs selector) CATALOG survived the restart — a model Read is
	// unknown-tool (no FS tools), the memory six still dispatch.
	if !unknownToolResult(events, "r1") {
		t.Fatal("post-restart Read did NOT come back unknown-tool — the no-fs selector session escalated onto a filesystem catalog")
	}
	if !sawDispatchedTool(events, "m1") {
		t.Fatal("post-restart Remember did not dispatch — the rehydrated catalog lost the memory six")
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "m1" && ev.ToolResult.IsError {
			t.Fatalf("post-restart Remember failed: %s", ev.ToolResult.Content)
		}
	}

	// Leg (b): SAME model — every post-restart request landed on the PERSISTED
	// selector model, and the DEFAULT (openai) provider was NEVER used.
	openrouterCap.mu.Lock()
	postModels := append([]string(nil), openrouterCap.models[preOpenRouterCalls:]...)
	openrouterCap.mu.Unlock()
	if len(postModels) == 0 {
		t.Fatal("no post-restart openrouter request captured — the rehydrated run did not reach the selector model")
	}
	for i, m := range postModels {
		if m != selectorModel {
			t.Fatalf("post-restart request[%d] model = %q, want %q (the selector must survive the restart)", i, m, selectorModel)
		}
	}
	openaiCap.mu.Lock()
	openaiModels := append([]string(nil), openaiCap.models...)
	openaiCap.mu.Unlock()
	if len(openaiModels) != 0 {
		t.Fatalf("the DEFAULT (openai) provider was called %d time(s) with models %v — the selector session DEGRADED onto the default provider after the restart",
			len(openaiModels), openaiModels)
	}

	// Leg (c): budget CONTINUED — the restored pre-restart spend (450, persisted in the
	// snapshot's usage field) accumulated with the post-restart turns and crossed the
	// 900 ceiling, tripping StopBudget; the third scripted turn ("should-not-reach")
	// never ran.
	if final := lastResultStop(events); final != session.StopBudget {
		t.Fatalf("post-restart stop = %q, want %q (the persisted budget must continue across the restart)", final, session.StopBudget)
	}
	for _, ev := range events {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Text == "should-not-reach" {
			t.Fatal("the third post-restart turn ran — the budget did not continue (it re-granted a fresh allowance)")
		}
	}

	// The empty root never reached the shared osfs factory.
	if got := diag.countContaining("EMPTY root reached the shared osfs factory"); got != 0 {
		t.Fatalf("the osfs factory chokepoint fired %d time(s) — rehydration let the empty root through", got)
	}
}

// lastResultStop returns the stop reason of the last EvResult event.
func lastResultStop(events []session.Event) session.StopReason {
	var stop session.StopReason
	for _, ev := range events {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	return stop
}
