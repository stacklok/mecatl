package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const entryRuntimeTool = "mcp__svc__echo"

type oldEntryArgs struct {
	Text    string `json:"text"`
	OldOnly string `json:"old_only,omitempty"`
}

type newEntryArgs struct {
	Text    string `json:"text"`
	NewOnly string `json:"new_only,omitempty"`
}

func newEntryRuntimeServer(t *testing.T, version string, started chan<- struct{}, release <-chan struct{}) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: version, Version: "v1"}, nil)
	var once sync.Once
	wait := func(ctx context.Context) error {
		if started == nil {
			return nil
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if version == "old" {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "old runtime schema"}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in oldEntryArgs) (*mcpsdk.CallToolResult, any, error) {
			if err := wait(ctx); err != nil {
				return nil, nil, err
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "old:" + in.Text}}}, nil, nil
		})
	} else {
		mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "new runtime schema"}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in newEntryArgs) (*mcpsdk.CallToolResult, any, error) {
			if err := wait(ctx); err != nil {
				return nil, nil, err
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "new:" + in.Text}}}, nil, nil
		})
	}
	srv.AddPrompt(&mcpsdk.Prompt{Name: "version", Description: version + " runtime prompt"}, func(ctx context.Context, _ *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		if err := wait(ctx); err != nil {
			return nil, err
		}
		return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: version + " prompt body"}}}}, nil
	})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

type entryRuntimeHarness struct {
	t        *testing.T
	ctx      context.Context
	store    *memstore.Store
	runtimes *mcpRuntimeSet
	old      *mcpReconcileCandidate
	new      *mcpReconcileCandidate
	factory  server.SessionEngineFactory
	shared   *agent.Engine
	cfg      Config
}

func newEntryRuntimeHarness(t *testing.T, provider port.LLMProvider, oldStarted chan<- struct{}, oldRelease <-chan struct{}, configure func(*Config)) *entryRuntimeHarness {
	t.Helper()
	oldManager := connectMainManager(t, "svc", newEntryRuntimeServer(t, "old", oldStarted, oldRelease))
	newManager := connectMainManager(t, "svc", newEntryRuntimeServer(t, "new", nil, nil))
	runtimes := newMCPRuntimeSet(nil)
	oldRuntime := &mcpReconcileCandidate{manager: oldManager, generation: 1, tools: toolMetadata(oldManager.Tools()), prompts: []mcp.Prompt{{Server: "svc", Name: "version", Description: "old runtime prompt"}}}
	newRuntime := &mcpReconcileCandidate{manager: newManager, generation: 2, tools: toolMetadata(newManager.Tools()), prompts: []mcp.Prompt{{Server: "svc", Name: "version", Description: "new runtime prompt"}}}
	if !runtimes.publish(nil, oldRuntime) {
		t.Fatal("publish old runtime")
	}
	store := memstore.New()
	cfg := Config{Model: "test-model", Workspace: t.TempDir(), MCPPrompts: true}
	if configure != nil {
		configure(&cfg)
	}
	var err error
	cfg.authorityEvaluator, _, err = selectAuthorityEvaluator("local", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	assets := catalogAssets{mcpRuntimes: runtimes, agentReg: agents.NewRegistry(nil)}
	factory := sessionEngineFactory(cfg, reg, provider, store, policy, hookexec.New(nil), runtimes, prompt.RootAssembler{}, assets, nil)
	pinned, release, err := runtimes.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sharedResult, err := factory(pinned, server.ProviderSelector{}, nil, server.ProfileDefault, cfg.Workspace, session.ModeDefault)
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sharedResult.Close()
		runtimes.close()
	})
	return &entryRuntimeHarness{t: t, ctx: context.Background(), store: store, runtimes: runtimes, old: oldRuntime, new: newRuntime, factory: factory, shared: sharedResult.Engine, cfg: cfg}
}

func (h *entryRuntimeHarness) service() *server.Service {
	h.t.Helper()
	names := []string{entryRuntimeTool, "Parallel", "Team"}
	for name := range agent.MemberToolNames() {
		names = append(names, name)
	}
	svc, err := newTestServerService(server.Config{
		Engine: h.shared, Store: h.store, SessionEngine: h.factory,
		SharedEngineRoot: h.cfg.Workspace, SharedEngineRevision: 1,
		OperationPin: h.runtimes.pin, OperationRevision: mcpOperationRevision, MCPProvider: h.runtimes,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: names, FileSystem: true, RemainingDelegationDepth: 2}, Provenance: "test", DefinitionIdentity: "test"}
		},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(svc.Close)
	return svc
}

func publishEntryRuntime(t *testing.T, h *entryRuntimeHarness) {
	t.Helper()
	if !h.runtimes.publish(h.old, h.new) {
		t.Fatal("publish new runtime")
	}
}

func drainEntryRun(t *testing.T, svc *server.Service, id session.SessionID, run *agent.Run) []session.Event {
	t.Helper()
	var events []session.Event
	for ev := range run.Events() {
		events = append(events, ev)
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	svc.FinishRun(id, run)
	return events
}

func parallelChildID(t *testing.T, events []session.Event) session.SessionID {
	t.Helper()
	for _, ev := range events {
		if ev.Type == session.EvParallelBranch && ev.Parallel != nil && ev.Parallel.ChildID != "" {
			return session.SessionID(ev.Parallel.ChildID)
		}
	}
	t.Fatal("run omitted Parallel branch identity")
	return ""
}

func teamMemberSessionID(t *testing.T, events []session.Event) session.SessionID {
	t.Helper()
	for _, ev := range events {
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.MemberSessionID != "" {
			return session.SessionID(ev.Team.MemberSessionID)
		}
	}
	t.Fatal("run omitted Team member identity")
	return ""
}

func assertEntrySchema(t *testing.T, req port.LLMRequest, marker string) {
	t.Helper()
	for _, spec := range req.Tools {
		if spec.Name == entryRuntimeTool {
			if !strings.Contains(string(spec.Schema), marker) {
				t.Fatalf("%s schema = %s, want marker %q", spec.Name, spec.Schema, marker)
			}
			return
		}
	}
	t.Fatalf("request omitted %s", entryRuntimeTool)
}

func TestMCPRuntimeConsistency_FailedStepRetryPinsOperationEntry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		once.Do(func() {
			assertEntrySchema(t, req, "old_only")
			close(started)
			<-release
		})
	})},
		mockllm.ToolCallTurn(session.NewToolCall("retry-old", entryRuntimeTool, json.RawMessage(`{"text":"retry"}`))),
		mockllm.TextTurn("retry complete"),
		mockllm.ToolCallTurn(session.NewToolCall("retry-new", entryRuntimeTool, json.RawMessage(`{"text":"next"}`))),
		mockllm.TextTurn("next complete"),
	)
	h := newEntryRuntimeHarness(t, provider, nil, nil, nil)
	svc := h.service()
	sess, err := svc.CreateSession(h.ctx, session.ModeDefault, session.Limits{MaxTurns: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPrompt("original", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordFailureMetadata(session.RetryDispositionRetryable, session.StreamProgressPrecommit); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Save(h.ctx, sess); err != nil {
		t.Fatal(err)
	}
	runCh := make(chan *agent.Run, 1)
	errCh := make(chan error, 1)
	go func() {
		run, retryErr := svc.RetryFailedRun(h.ctx, sess.ID)
		runCh <- run
		errCh <- retryErr
	}()
	<-started
	publishEntryRuntime(t, h)
	if h.old.isClosed() {
		t.Fatal("failed-step retry released its old operation pin before model dispatch")
	}
	close(release)
	run, err := <-runCh, <-errCh
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, run)
	got, err := h.store.Load(h.ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result := latestToolResult(got, "retry-old"); !strings.Contains(result, "old:retry") {
		t.Fatalf("retry tool result = %q, want old runtime", result)
	}
	next, err := svc.StartRun(h.ctx, sess.ID, "next operation")
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, next)
	got, _ = h.store.Load(h.ctx, sess.ID)
	if result := latestToolResult(got, "retry-new"); !strings.Contains(result, "new:next") {
		t.Fatalf("next tool result = %q, want new runtime", result)
	}
}

func TestMCPRuntimeConsistency_RestoredAwaitingApprovalPinsOperationEntry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("await-old", entryRuntimeTool, json.RawMessage(`{"text":"approved"}`))),
		mockllm.TextTurn("approved complete"),
		mockllm.ToolCallTurn(session.NewToolCall("await-new", entryRuntimeTool, json.RawMessage(`{"text":"next"}`))),
		mockllm.TextTurn("next complete"),
	)
	h := newEntryRuntimeHarness(t, provider, started, release, nil)
	svc1 := h.service()
	sess, err := svc1.CreateSession(h.ctx, session.ModeDefault, session.Limits{MaxTurns: 8})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := svc1.StartRun(h.ctx, sess.ID, "call the service")
	if err != nil {
		t.Fatal(err)
	}
	var askID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID = ev.Ask.AskID
			svc1.Persist(h.ctx, sess.ID)
			break
		}
	}
	if askID == "" {
		t.Fatal("MCP call did not park awaiting approval")
	}
	svc1.FinishRun(sess.ID, run1)
	svc1.Close()
	svc2 := h.service()
	runCh := make(chan *agent.Run, 1)
	errCh := make(chan error, 1)
	go func() {
		run, approveErr := svc2.ApproveRun(h.ctx, sess.ID, askID, session.VerdictAllowOnce, "")
		runCh <- run
		errCh <- approveErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("restored approval did not execute the old manager")
	}
	publishEntryRuntime(t, h)
	if h.old.isClosed() {
		t.Fatal("restored approval released its operation pin during the pending call")
	}
	close(release)
	run2, err := <-runCh, <-errCh
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc2, sess.ID, run2)
	got, _ := h.store.Load(h.ctx, sess.ID)
	if result := latestToolResult(got, "await-old"); !strings.Contains(result, "old:approved") {
		t.Fatalf("restored approval result = %q, want old runtime", result)
	}
	next, err := svc2.StartRun(h.ctx, sess.ID, "next operation")
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc2, sess.ID, next)
	got, _ = h.store.Load(h.ctx, sess.ID)
	if result := latestToolResult(got, "await-new"); !strings.Contains(result, "new:next") {
		t.Fatalf("next approval-path result = %q, want new runtime", result)
	}
}

func TestMCPRuntimeConsistency_ParallelToolInheritsRootOperationPin(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	nextOperation := make(chan struct{})
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		if requestHasTool(req, "Parallel") {
			marker := "old_only"
			select {
			case <-nextOperation:
				marker = "new_only"
			default:
			}
			assertEntrySchema(t, req, marker)
			return
		}
		once.Do(func() {
			close(started)
			<-release
		})
	})},
		mockllm.ToolCallTurn(session.NewToolCall("parallel-old", "Parallel", json.RawMessage(`{"tasks":["inspect the workspace"],"join":"all"}`))),
		mockllm.TextTurn("old branch complete"), mockllm.TextTurn("old parent complete"),
		mockllm.ToolCallTurn(session.NewToolCall("parallel-new", entryRuntimeTool, json.RawMessage(`{"text":"parallel"}`))),
		mockllm.TextTurn("new parent complete"),
	)
	h := newEntryRuntimeHarness(t, provider, nil, nil, func(cfg *Config) { cfg.EnableParallel = true })
	svc := h.service()
	sess, err := svc.CreateSession(h.ctx, session.ModeDefault, session.Limits{MaxTurns: 12})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(h.ctx, sess.ID, "parallel old")
	if err != nil {
		t.Fatal(err)
	}
	eventsCh := make(chan []session.Event, 1)
	go func() { eventsCh <- drainEntryRun(t, svc, sess.ID, run) }()
	<-started
	publishEntryRuntime(t, h)
	if h.old.isClosed() {
		t.Fatal("Parallel root operation did not retain the old runtime")
	}
	close(release)
	events := <-eventsCh
	if _, err := h.store.Load(h.ctx, parallelChildID(t, events)); err != nil {
		t.Fatal(err)
	}
	close(nextOperation)
	next, err := svc.StartRun(h.ctx, sess.ID, "use the current MCP runtime")
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, next)
	got, err := h.store.Load(h.ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result := latestToolResult(got, "parallel-new"); !strings.Contains(result, "new:parallel") {
		t.Fatalf("operation after Parallel result = %q, want new runtime", result)
	}
}

func TestMCPRuntimeConsistency_InLoopTeamToolInheritsRootOperationPin(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	nextOperation := make(chan struct{})
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		if requestHasTool(req, "Team") {
			marker := "old_only"
			select {
			case <-nextOperation:
				marker = "new_only"
			default:
			}
			assertEntrySchema(t, req, marker)
			return
		}
		once.Do(func() {
			close(started)
			<-release
		})
	})},
		mockllm.ToolCallTurn(session.NewToolCall("team-old", "Team", json.RawMessage(`{"goal":"inspect once","members":[{"name":"lead","role":"inspect the workspace"}]}`))),
		mockllm.TextTurn("old member complete"), mockllm.TextTurn("old team report"), mockllm.TextTurn("old parent complete"),
		mockllm.ToolCallTurn(session.NewToolCall("team-new", entryRuntimeTool, json.RawMessage(`{"text":"team"}`))),
		mockllm.TextTurn("new parent complete"),
	)
	h := newEntryRuntimeHarness(t, provider, nil, nil, func(cfg *Config) { cfg.EnableTeams = true })
	svc := h.service()
	sess, err := svc.CreateSession(h.ctx, session.ModeDefault, session.Limits{MaxTurns: 14})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(h.ctx, sess.ID, "team old")
	if err != nil {
		t.Fatal(err)
	}
	eventsCh := make(chan []session.Event, 1)
	go func() { eventsCh <- drainEntryRun(t, svc, sess.ID, run) }()
	<-started
	publishEntryRuntime(t, h)
	if h.old.isClosed() {
		t.Fatal("in-loop Team root operation did not retain the old runtime")
	}
	close(release)
	events := <-eventsCh
	if _, err := h.store.Load(h.ctx, teamMemberSessionID(t, events)); err != nil {
		t.Fatal(err)
	}
	close(nextOperation)
	next, err := svc.StartRun(h.ctx, sess.ID, "use the current MCP runtime")
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, next)
	got, err := h.store.Load(h.ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result := latestToolResult(got, "team-new"); !strings.Contains(result, "new:team") {
		t.Fatalf("operation after in-loop Team result = %q, want new runtime", result)
	}
}

func TestMCPRuntimeConsistency_InRunPromptExpansionUsesOperationPin(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests []port.LLMRequest
	var mu sync.Mutex
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
	})}, mockllm.TextTurn("old expansion complete"), mockllm.TextTurn("new expansion complete"))
	h := newEntryRuntimeHarness(t, provider, started, release, nil)
	svc := h.service()
	sess, err := svc.CreateSession(h.ctx, session.ModeDefault, session.Limits{MaxTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	runCh := make(chan *agent.Run, 1)
	errCh := make(chan error, 1)
	go func() {
		run, runErr := svc.StartRun(h.ctx, sess.ID, "/mcp__svc__version")
		runCh <- run
		errCh <- runErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("in-run prompt expansion did not reach the old manager")
	}
	publishEntryRuntime(t, h)
	if h.old.isClosed() {
		t.Fatal("prompt expansion released the root operation pin during GetPrompt")
	}
	close(release)
	run, err := <-runCh, <-errCh
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, run)
	next, err := svc.StartRun(h.ctx, sess.ID, "/mcp__svc__version")
	if err != nil {
		t.Fatal(err)
	}
	drainEntryRun(t, svc, sess.ID, next)
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(requests))
	}
	if !requestContainsText(requests[0], "old prompt body") {
		t.Fatalf("active operation prompt = %+v, want old expansion", requests[0].Messages)
	}
	if !requestContainsText(requests[1], "new prompt body") {
		t.Fatalf("next operation prompt = %+v, want new expansion", requests[1].Messages)
	}
}

func requestContainsText(req port.LLMRequest, want string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text, want) {
			return true
		}
	}
	return false
}
