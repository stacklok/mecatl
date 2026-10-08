package app

import (
	"context"
	"strings"
	"testing"
	"time"

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
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// allMemoryToolNames is the full six-tool memory family (project memory +
// user-model) the per-session catalogs were silently missing (issue #42).
var allMemoryToolNames = []string{
	memory.RememberToolName,
	memory.RecallToolName,
	memory.SearchMemoryToolName,
	memory.InspectMemoryToolName,
	memory.ForgetMemoryToolName,
	memory.UndoMemoryToolName,
	memory.RememberUserToolName,
	memory.RecallUserToolName,
	memory.SearchUserModelToolName,
	memory.InspectUserMemoryToolName,
	memory.ForgetUserMemoryToolName,
	memory.UndoUserMemoryToolName,
}

// memoryStoresForTest opens REAL flocked stores in temp dirs — the same concrete
// *memory.Store the composition threads through catalogAssets.
func memoryStoresForTest(t *testing.T) (*memory.Store, *memory.Store) {
	t.Helper()
	mem, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(project): %v", err)
	}
	um, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New(usermodel): %v", err)
	}
	return mem, um
}

// memoryToolFactory builds a sessionEngineFactory over a two-provider registry
// (openai default, openrouter selectable) with the supplied assets threaded —
// the shape Build wires after the issue-#42 fix.
func memoryToolFactory(t *testing.T, assets catalogAssets) server.SessionEngineFactory {
	t.Helper()
	oa := mockllm.New(mockllm.TextTurn("OPENAI-REPLY"))
	or := mockllm.New(mockllm.TextTurn("OPENROUTER-REPLY"))
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	return sessionEngineFactory(Config{Model: "gpt-5"}, reg, oa, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
}

// TestSessionEngineFactoryRegistersMemoryToolsForSelector is the headline issue-#42
// regression guard: a NON-default selector session (what the mecatui /models picker
// always produces) must carry ALL SIX memory tools when the stores are configured.
// Before the fix the per-session catalog registered core+MCP+Subagent/Team only, so
// picking a model silently stripped Remember/Recall/SearchMemory (+ user-model) —
// while the turn-0 prompt still advertised the memory index.
func TestSessionEngineFactoryRegistersMemoryToolsForSelector(t *testing.T) {
	mem, um := memoryStoresForTest(t)
	factory := memoryToolFactory(t, catalogAssets{memStore: mem, userModelStore: um})

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(selector): %v", err)
	}
	defer func() { _ = res.Close() }()

	for _, name := range allMemoryToolNames {
		if !res.Engine.HasTool(name) {
			t.Errorf("selector engine is missing the memory tool %q (issue #42: memory tools stripped under model selection)", name)
		}
	}
}

// TestSessionEngineFactoryRegistersMemoryToolsForClientMCP covers the OTHER
// per-session trigger: a zero-selector session that gets a per-session engine only
// because client MCP specs are attached must also carry the six memory tools.
func TestSessionEngineFactoryRegistersMemoryToolsForClientMCP(t *testing.T) {
	url := newMCPTestServer(t)

	mem, um := memoryStoresForTest(t)
	factory := memoryToolFactory(t, catalogAssets{memStore: mem, userModelStore: um})

	res, err := factory(context.Background(), server.ProviderSelector{}, []mcp.ServerConfig{{Name: "cli", URL: url}}, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory(client specs): %v", err)
	}
	defer func() { _ = res.Close() }()

	for _, name := range allMemoryToolNames {
		if !res.Engine.HasTool(name) {
			t.Errorf("client-MCP session engine is missing the memory tool %q (issue #42)", name)
		}
	}
	// Sanity: it really is the client-MCP path (the client tool mounted too).
	if !res.Engine.HasTool("mcp__cli__echo") {
		t.Fatal("client MCP tool missing — the test did not exercise the client-MCP path")
	}
}

// TestSessionEngineFactoryOmitsMemoryToolsWhenUnconfigured proves the opt-in
// posture survives the fix: with NO stores configured (nil concrete pointers in
// the assets), no memory tool appears on a per-session engine.
func TestSessionEngineFactoryOmitsMemoryToolsWhenUnconfigured(t *testing.T) {
	factory := memoryToolFactory(t, catalogAssets{})

	res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	for _, name := range allMemoryToolNames {
		if res.Engine.HasTool(name) {
			t.Errorf("memory disabled (nil stores): per-session engine should NOT carry %q", name)
		}
	}
}

// TestSelectorSessionMemoryPromptHasMatchingTools is the issue's SYMPTOM on the
// wire: the turn-0 prompt of a selector session advertises the tier-0
// <memory-index> (the instruction assembler reads the store), so the SAME request
// must also carry the memory tool specs — before the fix the model was told about
// its memory but given no tool to Recall it. A mock request observer captures the
// exact port.LLMRequest the selector engine sends and asserts BOTH halves.
func TestSelectorSessionMemoryPromptHasMatchingTools(t *testing.T) {
	ctx := context.Background()
	memStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := memStore.Remember(ctx, tool.MemoryEntry{Key: "build/test-runner", Value: "Run the suite with `task test`."}, tool.MemoryCurrent{}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	var captured []port.LLMRequest
	or := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = append(captured, req) })},
		mockllm.TextTurn("ok"),
	)
	oa := mockllm.New(mockllm.TextTurn("unused"))
	reg := twoProviderReg(oa, providerOpenAI, "gpt-5", or, providerOpenRouter)

	// The REAL instruction chain Build wires when a memory store is configured.
	instructions := buildInstructionAssembler(nil, nil, nil, memStore, nil, false)
	factory := sessionEngineFactory(Config{Model: "gpt-5"}, reg, oa, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, instructions,
		catalogAssets{memStore: memStore}, nil)

	res, err := factory(ctx, server.ProviderSelector{ProviderID: providerOpenRouter}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Now())
	drainRun(res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi", Parts: nil}))

	if len(captured) == 0 {
		t.Fatal("no LLM request captured — the selector engine never called its provider")
	}
	req := captured[0]

	var hasIndex bool
	for _, m := range req.Messages {
		if strings.Contains(m.Text, "<memory-index>") {
			hasIndex = true
			break
		}
	}
	if !hasIndex {
		t.Fatal("selector session's LLMRequest carries no <memory-index> turn-0 message — the instruction chain regressed (precondition for the symptom check)")
	}

	var hasRecall bool
	for _, ts := range req.Tools {
		if ts.Name == memory.RecallToolName {
			hasRecall = true
			break
		}
	}
	if !hasRecall {
		t.Fatal("selector session's LLMRequest advertises the <memory-index> but carries NO Recall tool spec — the model is told about memory it cannot read (issue #42)")
	}
}
