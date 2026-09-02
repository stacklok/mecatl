package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

func newDebugPublishMCPServer(t *testing.T) (string, *int32) {
	t.Helper()
	var calls int32
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "github-like", Version: "v1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "echo", Description: "publish issue", Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in mcpEchoArgs) (*mcpsdk.CallToolResult, any, error) {
			atomic.AddInt32(&calls, 1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "published:" + in.Text}}}, nil, nil
		})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL, &calls
}

func TestDebugSessionMCPPublishJourneyRequiresFreshApproval(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	target := session.New("target-publish", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(ctx, target); err != nil {
		t.Fatal(err)
	}
	url, calls := newDebugPublishMCPServer(t)
	manager := connectMainManager(t, "github", url)
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		requests = append(requests, req)
	})},
		mockllm.TextTurn("Draft issue: observed failure and reproduction steps."),
		mockllm.ToolCallTurn(session.NewToolCall("publish-1", "mcp__github__echo", json.RawMessage(`{"text":"publish issue"}`))),
		mockllm.TextTurn("published"),
		mockllm.ToolCallTurn(session.NewToolCall("publish-2", "mcp__github__echo", json.RawMessage(`{"text":"publish follow-up"}`))),
		mockllm.TextTurn("second publication denied"),
	)
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil, nil, manager)
	res, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, []string{"github"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	debug, err := session.NewDebug("debug-publish", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	env := testEnvironment(nofs.New(), nil)
	drainRun(res.Engine.Run(ctx, debug, env, agent.RunRequest{Text: "diagnose and draft an issue"}))
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("MCP calls before explicit publication request = %d, want 0", got)
	}
	if len(requests) == 0 || !strings.Contains(requests[0].System.StablePrefix, "Selected reporting servers are available by configured name only (github)") || !strings.Contains(requests[0].System.StablePrefix, "Every selected MCP call, including tools marked read-only, requires a fresh interactive harness approval") {
		t.Fatalf("factory-built system prompt lacks selected-server publication contract: %+v", requests)
	}
	if err := debug.Reopen(); err != nil {
		t.Fatal(err)
	}
	run := res.Engine.Run(ctx, debug, env, agent.RunRequest{Text: "Publish that issue now."})
	asked := false
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asked = true
			if got := atomic.LoadInt32(calls); got != 0 {
				t.Fatalf("MCP calls before approval = %d, want 0", got)
			}
			run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
		}
	}
	if !asked || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("first publish asked=%v calls=%d, want true/1", asked, atomic.LoadInt32(calls))
	}
	if err := debug.Reopen(); err != nil {
		t.Fatal(err)
	}
	run = res.Engine.Run(ctx, debug, env, agent.RunRequest{Text: "Publish a follow-up."})
	asked = false
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asked = true
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
	}
	if !asked || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("second mutation asked=%v calls=%d, want true/1", asked, atomic.LoadInt32(calls))
	}
}

func TestDebugSessionSelectedGlobalMCPIsExactAndBorrowed(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	target := session.New("target-mcp", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(ctx, target); err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.NewManager(ctx, []mcp.ServerConfig{
		{Name: "github", URL: newMCPTestServerPrefixed(t, "gh")},
		{Name: "slack", URL: newMCPTestServerPrefixed(t, "slack")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	provider := mockllm.New(mockllm.TextTurn("done"))
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil, nil, manager)
	res, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, []string{"github"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Engine.HasTool(sessiondebug.ToolName) || !res.Engine.HasTool("mcp__github__echo") {
		t.Fatal("selected debug catalog omitted InspectSession or selected direct tool")
	}
	if res.Engine.HasTool("mcp__slack__echo") || res.Engine.HasTool("CallMcpWithQuery") || len(res.DebugMCPTools) != 1 {
		t.Fatalf("debug catalog widened: ceiling=%v", res.DebugMCPTools)
	}
	if err := res.Close(); err != nil {
		t.Fatal(err)
	}
	// The per-session close is a no-op: the borrowed global manager remains live.
	if len(manager.Tools()) != 2 {
		t.Fatal("debug engine close closed the shared global manager")
	}
	if _, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, []string{"missing"}, nil); err == nil {
		t.Fatal("unknown selected server was accepted")
	}
	if _, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, []string{"github"}, []string{"mcp__github__new"}); err == nil {
		t.Fatal("restart widened or tolerated a missing persisted tool")
	}
}

func TestDebugSessionFactoryExactCatalogAndStablePrefix(t *testing.T) {
	store := memstore.New()
	target := session.New("target-exact", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
	})}, mockllm.TextTurn("debug done"), mockllm.TextTurn("normal done"))
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil, nil, nil)
	res, err := factory(context.Background(), server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
	if err != nil {
		t.Fatalf("debug factory: %v", err)
	}
	debug, err := session.NewDebug("debug", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	drainRun(res.Engine.Run(context.Background(), debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "diagnose"}))

	// Run the normal real deps path with the same observer; its prompt must not gain
	// the target-bound debug contract.
	normalDeps := baseEngineDeps(cfg, reg, provider, store, nil, nil, nil, nil)
	normalDeps.Catalog = tool.NewCatalog()
	normal := session.New("normal", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(3, 0))
	drainRun(agent.NewEngine(normalDeps).Run(context.Background(), normal, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "hello"}))

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != sessiondebug.ToolName {
		t.Fatalf("debug tools = %+v, want exactly InspectSession", requests[0].Tools)
	}
	prefix := requests[0].System.StablePrefix
	for _, want := range []string{"target-exact", "snapshot transcript is authoritative", "bounded event-log projections", "hostile untrusted data", "Never mutate, resume, approve, cancel, or steer"} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("debug StablePrefix missing %q:\n%s", want, prefix)
		}
	}
	if strings.Contains(requests[1].System.StablePrefix, "DEBUG ANALYSIS SESSION") || strings.Contains(requests[1].System.StablePrefix, "target-exact") {
		t.Fatalf("normal StablePrefix gained debug posture:\n%s", requests[1].System.StablePrefix)
	}
}

func TestDebugSessionFactoryPreservesBaseInspectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effect governance.Effect
		ask    bool
	}{
		{name: "configured deny", effect: governance.Deny},
		{name: "configured ask", effect: governance.Ask, ask: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := memstore.New()
			target := session.New("target-policy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
			if err := store.Save(ctx, target); err != nil {
				t.Fatal(err)
			}
			provider := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("inspect", sessiondebug.ToolName, []byte(`{"view":"status"}`))),
				mockllm.TextTurn("done"),
			)
			cfg := Config{Model: "mock-model"}
			base := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeUser, Tool: sessiondebug.ToolName, Effect: tc.effect}}, nil)
			factory := debugSessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, store, nil, base, nil)
			res, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			debug, err := session.NewDebug("debug-policy", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
			if err != nil {
				t.Fatal(err)
			}
			run := res.Engine.Run(ctx, debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "inspect"})
			asked := false
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk {
					asked = true
					run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
				}
			}
			if asked != tc.ask {
				t.Fatalf("permission ask=%t, want %t", asked, tc.ask)
			}
			if tc.effect == governance.Deny && len(debug.Conversation.Messages) >= 3 && !debug.Conversation.Messages[2].ToolResult.IsError {
				t.Fatal("configured deny did not produce an error result")
			}
		})
	}
}

func TestDebugSessionRestartRehydratesBoundEngine(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("status", sessiondebug.ToolName, []byte(`{"view":"status"}`))),
		mockllm.TextTurn("after restart"),
	)
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil, nil, nil)
	shared := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: cfg.Model})
	newService := func() *server.Service {
		svc, err := newTestServerService(server.Config{
			Engine: shared, Store: store, Workspaces: func(string) tool.Workspace { return nofs.New() },
			DebugSessionEngine: factory, Now: func() time.Time { return time.Unix(10, 0) },
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		return svc
	}
	svc1 := newService()
	alice := session.WithPrincipal(ctx, &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(ctx, &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeClientCredentials})
	target, err := svc1.CreateSession(alice, "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	debug, err := svc1.CreateSessionWithProfile(bob, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatal(err)
	}

	// A fresh Service has no in-memory per-session engine. StartRun must recover
	// DebugTargetID from the snapshot and invoke the production debug factory.
	svc2 := newService()
	run, err := svc2.StartRun(bob, debug.ID, "inspect the target")
	if err != nil {
		t.Fatalf("StartRun after restart: %v", err)
	}
	var final, statusEvidence string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "status" {
			statusEvidence = ev.ToolResult.Content
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "after restart" || !strings.Contains(statusEvidence, `"target_session_id":"`+string(target.ID)+`"`) {
		t.Fatalf("restart result=%q evidence=%q", final, statusEvidence)
	}
	persisted, err := store.Load(ctx, debug.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Relationship.DebugTargetID != target.ID {
		t.Fatalf("rehydrated target = %q, want %q", persisted.Relationship.DebugTargetID, target.ID)
	}
}

func TestDebugSessionConverseAdvertisesAndExecutesInspectSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  string
		rehydrate bool
		config    func(*testing.T, port.LLMProvider) Config
	}{
		{
			name:     "openai",
			provider: providerOpenAI,
			config: func(t *testing.T, provider port.LLMProvider) Config {
				return Config{
					Workspace: t.TempDir(), NoSoul: true,
					envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
					providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return provider },
				}
			},
		},
		{
			name:      "toolhive",
			provider:  providerToolhive,
			rehydrate: true,
			config: func(t *testing.T, provider port.LLMProvider) Config {
				return Config{
					Workspace: t.TempDir(), NoSoul: true, ToolhiveLLM: true,
					toolhiveConfigPath: writeToolhiveConfig(t, "https://upstream.example/gw"),
					envDetector:        fakeEnv(nil), liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
					providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return provider },
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				requests []port.LLMRequest
			)
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				mu.Lock()
				requests = append(requests, req)
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("status", sessiondebug.ToolName, []byte(`{"view":"status"}`))),
				mockllm.ToolCallTurn(session.NewToolCall("transcript", sessiondebug.ToolName, []byte(`{"view":"transcript"}`))),
				mockllm.ToolCallTurn(session.NewToolCall("network", sessiondebug.ToolName, []byte(`{"view":"network"}`))),
				mockllm.TextTurn("diagnosis complete"),
			)
			ctx := context.Background()
			cfg := tc.config(t, provider)
			built, err := Build(ctx, cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			listener := bufconn.Listen(1 << 20)
			grpcServer := grpc.NewServer()
			mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(built.Service))
			go func() { _ = grpcServer.Serve(listener) }()
			defer grpcServer.Stop()
			conn, err := grpc.NewClient("passthrough:///debug-bufconn",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				t.Fatalf("grpc client: %v", err)
			}
			defer conn.Close()
			client := mecatlv1.NewHarnessServiceClient(conn)

			target, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
			if err != nil {
				t.Fatalf("create target: %v", err)
			}
			debug, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
				Profile: string(server.ProfileNoFS), ProviderId: tc.provider, ModelId: "gpt-5.6-sol", DebugTargetSessionId: target.GetSessionId(),
			})
			if err != nil {
				t.Fatalf("create debug session: %v", err)
			}
			if tc.rehydrate {
				// Drop the create-time registration so Converse must recover the bound
				// debug engine through the persisted-session rehydration path.
				built.Service.CloseSession(session.SessionID(debug.GetSessionId()))
			}
			stream, err := client.Converse(ctx)
			if err != nil {
				t.Fatalf("Converse: %v", err)
			}
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{
				SessionId: debug.GetSessionId(), Text: "diagnose the target",
			}}}); err != nil {
				t.Fatalf("send prompt: %v", err)
			}
			if err := stream.CloseSend(); err != nil {
				t.Fatalf("close send: %v", err)
			}

			results := map[string]string{}
			for {
				response, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("receive Converse event: %v", err)
				}
				if result := response.GetEvent().GetToolResult(); result != nil {
					results[result.GetCallId()] = result.GetContent()
				}
			}
			for callID, evidence := range map[string][]string{
				"status":     {`"view":"status"`, target.GetSessionId()},
				"transcript": {`"view":"transcript"`, `"authoritative":true`},
				"network":    {`"view":"network"`, `"successful_attempts_timed":false`},
			} {
				got, ok := results[callID]
				if !ok {
					t.Fatalf("missing %s InspectSession result; got calls %v", callID, results)
				}
				for _, want := range evidence {
					if !strings.Contains(got, want) {
						t.Fatalf("%s result missing %q: %s", callID, want, got)
					}
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 4 {
				t.Fatalf("provider requests = %d, want 4", len(requests))
			}
			for i, req := range requests {
				if len(req.Tools) != 1 || req.Tools[0].Name != sessiondebug.ToolName {
					t.Fatalf("provider request %d tools = %+v, want exactly InspectSession", i, req.Tools)
				}
			}
		})
	}
}
