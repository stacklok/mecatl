package agent_test

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

type rootCaptureProvider struct {
	mu     sync.Mutex
	active []session.SessionID
	root   []session.SessionID
	inner  port.LLMProvider
}

func (p *rootCaptureProvider) Capabilities() port.ProviderCapabilities {
	if p.inner != nil {
		return p.inner.Capabilities()
	}
	return port.ProviderCapabilities{}
}

func (p *rootCaptureProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	active, _ := port.SessionIDFromContext(ctx)
	root, _ := port.RootSessionIDFromContext(ctx)
	p.mu.Lock()
	p.active = append(p.active, active)
	p.root = append(p.root, root)
	p.mu.Unlock()
	if p.inner != nil {
		return p.inner.Stream(ctx, req)
	}
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func TestRootSessionProviderCorrelation_Scenario1_NestedRunsPreserveRoot(t *testing.T) {
	childProvider := &rootCaptureProvider{inner: mockllm.New(mockllm.TextTurn("child done"))}
	childEngine := agent.NewEngine(agent.Deps{LLM: childProvider, Catalog: tool.NewCatalog(), Model: "child"})
	subagent := agent.NewSubagentTool(childEngine)
	catalog := tool.NewCatalog()
	catalog.MustRegister(subagent)
	parentProvider := &rootCaptureProvider{inner: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"inspect"}`))),
		mockllm.TextTurn("parent done"),
	)}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	main := session.New("main", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	eng := agent.NewEngine(agent.Deps{
		LLM: parentProvider, Catalog: catalog, Model: "parent",
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
	})
	for range eng.Run(context.Background(), main, env, agent.RunRequest{Text: "delegate"}).Events() {
	}

	if len(parentProvider.active) != 2 || parentProvider.active[0] != "main" || parentProvider.root[0] != "main" || parentProvider.root[1] != "main" {
		t.Fatalf("parent correlation = active %v root %v", parentProvider.active, parentProvider.root)
	}
	if len(childProvider.active) != 1 || childProvider.active[0] == "main" || childProvider.root[0] != "main" {
		t.Fatalf("nested Subagent correlation = active %v root %v", childProvider.active, childProvider.root)
	}

	compactor := &rootCaptureCompactor{}
	compactProvider := &rootCaptureProvider{inner: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("echo", "Echo", []byte(`{}`))),
		mockllm.TextTurn("done"),
	)}
	compactCatalog := tool.NewCatalog()
	compactCatalog.MustRegister(correlationTool{})
	compactSession := session.New("compact-main", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	compactEngine := agent.NewEngine(agent.Deps{
		LLM: compactProvider, Catalog: compactCatalog, Model: "compact",
		Policy:    permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Compactor: compactor, TokenCounter: alwaysCompactCounter{}, ContextWindow: func() int { return 2 }, CompactionRatio: 0.5,
	})
	for range compactEngine.Run(context.Background(), compactSession, env, agent.RunRequest{Text: "compact"}).Events() {
	}
	if len(compactor.roots) == 0 || compactor.active[0] != "compact-main" || compactor.roots[0] != "compact-main" {
		t.Fatalf("automatic compaction correlation = active %v root %v, want compact-main", compactor.active, compactor.roots)
	}
	for i := range compactProvider.active {
		if compactProvider.active[i] != "compact-main" || compactProvider.root[i] != "compact-main" {
			t.Fatalf("post-compaction correlation = active %v root %v", compactProvider.active, compactProvider.root)
		}
	}
}

func TestRootSessionProviderCorrelation_Scenario1_DetachedReviewerPreservesRoot(t *testing.T) {
	child := shellChildEngine(mockllm.New(substitutionAskTurns(1)...), &fakeShell{})
	task := agent.NewSubagentTool(child)
	reviewer := &scriptedAdjudicator{script: []adjOutcome{allow("safe")}}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("delegate", "Subagent", []byte(`{"prompt":"inspect"}`))),
		mockllm.TextTurn("done"),
	)
	eng := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), ChildAskReviewer: reviewer})
	ctx := port.WithRootSessionID(context.Background(), "conversation-root")
	_ = drainWithTimeout(t, eng.Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	if roots := reviewer.capturedRoots(); len(roots) != 1 || roots[0] != "conversation-root" {
		t.Fatalf("detached reviewer roots = %v, want [conversation-root]", roots)
	}
}

type rootCaptureCompactor struct {
	active []session.SessionID
	roots  []session.SessionID
}

func (c *rootCaptureCompactor) Compact(ctx context.Context, conv *session.Conversation) ([]session.Message, string, error) {
	active, _ := port.SessionIDFromContext(ctx)
	root, _ := port.RootSessionIDFromContext(ctx)
	c.active = append(c.active, active)
	c.roots = append(c.roots, root)
	return session.CloneMessages(conv.Messages), "compacted", nil
}

func TestRootSessionProviderCorrelation_Scenario1_ResumeUsesCurrentCausalRoot(t *testing.T) {
	store := memstore.New()
	childProvider := &rootCaptureProvider{inner: mockllm.New(
		mockllm.TextTurn("first child run"),
		mockllm.TextTurn("resumed child run"),
	)}
	childEngine := agent.NewEngine(agent.Deps{LLM: childProvider, Catalog: tool.NewCatalog(), Model: "child"})
	subagent := agent.NewSubagentTool(childEngine, agent.WithSubagentStore(store))
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)

	runParent := func(id session.SessionID, llm port.LLMProvider) {
		t.Helper()
		catalog := tool.NewCatalog()
		catalog.MustRegister(subagent)
		eng := agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: catalog, Model: "parent",
			Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		})
		parent := session.New(id, session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
		for range eng.Run(context.Background(), parent, env, agent.RunRequest{Text: "delegate"}).Events() {
		}
	}

	runParent("first-main", mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("first-call", "Subagent", []byte(`{"prompt":"first"}`))),
		mockllm.TextTurn("done"),
	))
	childID := session.SessionID("subagent-first-main-first-call")
	before, err := store.Load(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	originalRelationship := before.Relationship

	runParent("current-main", mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("resume-call", "Subagent", []byte(`{"resume":"subagent-first-main-first-call","prompt":"continue"}`))),
		mockllm.TextTurn("done"),
	))
	after, err := store.Load(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != childID || after.Relationship != originalRelationship {
		t.Fatalf("resumed child identity changed: id=%q relationship=%+v, want id=%q relationship=%+v", after.ID, after.Relationship, childID, originalRelationship)
	}
	if len(childProvider.active) != 2 || childProvider.active[0] != childID || childProvider.active[1] != childID ||
		childProvider.root[0] != "first-main" || childProvider.root[1] != "current-main" {
		t.Fatalf("resumed correlation = active %v root %v", childProvider.active, childProvider.root)
	}
}

func (p *rootCaptureProvider) snapshot() (active, root []session.SessionID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.SessionID(nil), p.active...), append([]session.SessionID(nil), p.root...)
}

// TestRootSessionProviderCorrelation_Scenario1_EveryChildFamilyInheritsRoot proves
// each delegated or auxiliary engine keeps its own active session while inheriting
// the causal root from the context it is started under. A new child family belongs
// in this table: a row that fails means the family detached from its parent context.
func TestRootSessionProviderCorrelation_Scenario1_EveryChildFamilyInheritsRoot(t *testing.T) {
	const parentID, root = session.SessionID("s1"), session.SessionID("main")
	// delegated rows run parent turns that call the family's tool(s); the parent
	// session is the root because the run is entered with no inherited root.
	delegated := func(t *testing.T, calls []session.ToolCall, tools ...tool.Tool) {
		t.Helper()
		turns := make([]mockllm.Turn, 0, len(calls)+1)
		for _, call := range calls {
			turns = append(turns, mockllm.ToolCallTurn(call))
		}
		parentLLM := mockllm.New(append(turns, mockllm.TextTurn("parent done"))...)
		eng := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tools...)})
		_ = drainWithTimeout(t, eng.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	}
	rooted := port.WithRootSessionID(context.Background(), root)
	families := []struct {
		name       string
		wantPrefix string
		wantRoot   session.SessionID
		drive      func(t *testing.T, llm port.LLMProvider)
	}{
		{"subagent-foreground", agent.SubagentSessionPrefix, parentID, func(t *testing.T, llm port.LLMProvider) {
			delegated(t, []session.ToolCall{session.NewToolCall("fg", "Subagent", []byte(`{"prompt":"inspect"}`))},
				agent.NewSubagentTool(childEngineWith(llm, tool.NewCatalog())))
		}},
		{"subagent-background", agent.SubagentSessionPrefix, parentID, func(t *testing.T, llm port.LLMProvider) {
			// The status wait parks the parent until the detached child finishes.
			delegated(t, []session.ToolCall{
				session.NewToolCall("bg", "Subagent", []byte(`{"prompt":"inspect","background":true}`)),
				session.NewToolCall("wait", "SubagentStatus", []byte(`{"agent_id":"subagent-s1-bg","wait_ms":30000}`)),
			}, agent.NewSubagentTool(childEngineWith(llm, tool.NewCatalog())), agent.NewSubagentStatusTool())
		}},
		{"parallel-branch", agent.ParallelSessionPrefix, parentID, func(t *testing.T, llm port.LLMProvider) {
			delegated(t, []session.ToolCall{session.NewToolCall("par", "Parallel", []byte(`{"tasks":["one","two"]}`))},
				agent.NewParallelTool(childEngineWith(llm, tool.NewCatalog()), &memForker{}))
		}},
		{"team-member", agent.TeamSessionPrefix, parentID, func(t *testing.T, llm port.LLMProvider) {
			factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
				cat := tool.NewCatalog()
				for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
					cat.MustRegister(tl)
				}
				return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allowAll(), Hooks: noopHooks{}, Model: "m"})}
			}
			delegated(t, []session.ToolCall{session.NewToolCall("tm", "Team", []byte(`{"goal":"inspect","members":[{"name":"lead","role":"inspect the tree"}]}`))},
				agent.NewTeamTool(factory, agent.WithTeamToolReadOnlyForker(&recordingSubagentForker{})))
		}},
		{"guardrail-checker", "guardrail-checker-", root, func(_ *testing.T, llm port.LLMProvider) {
			_, _ = agent.RunGuardrailCheck(rooted, newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}), "check this")
		}},
		{"model-router", "model-router-", root, func(_ *testing.T, llm port.LLMProvider) {
			_, _, _, _ = agent.RunModelRouter(rooted, newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}), agent.ModelRouteRequest{
				TaskPrompt: "inspect", Categories: []agent.ModelRouteCategory{{Name: "fast"}},
			})
		}},
		{"fork-judge", "fork-judge-", root, func(_ *testing.T, llm port.LLMProvider) {
			judge := agent.NewEngineJudge(newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}))
			_, _, _ = judge.Judge(rooted, []agent.BranchSummary{{Label: "branch-0", Summary: "a"}, {Label: "branch-1", Summary: "b"}}, "pick one")
		}},
		{"ask-reviewer", "ask-reviewer-", root, func(_ *testing.T, llm port.LLMProvider) {
			reviewer := agent.NewEngineAskReviewer(newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}))
			_, _ = reviewer.Review(rooted, agent.ChildAskReviewRequest{Ask: session.PendingAsk{AskID: "ask-1", Tool: "Shell", Reason: "review"}})
		}},
	}
	for _, tc := range families {
		t.Run(tc.name, func(t *testing.T) {
			provider := &rootCaptureProvider{}
			tc.drive(t, provider)
			active, roots := provider.snapshot()
			if len(active) == 0 {
				t.Fatal("family made no provider call")
			}
			for i := range active {
				if !strings.HasPrefix(string(active[i]), tc.wantPrefix) || roots[i] != tc.wantRoot {
					t.Fatalf("call %d correlation = active %q root %q, want active prefix %q root %q",
						i+1, active[i], roots[i], tc.wantPrefix, tc.wantRoot)
				}
			}
		})
	}
}
