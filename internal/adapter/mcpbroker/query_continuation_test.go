package mcpbroker

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type queryAllowPolicy struct{}

func (queryAllowPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (queryAllowPolicy) Learn(session.SessionID, session.ToolCall) {}

type queryPlacement struct{}

func (queryPlacement) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/query", Revision: "v1"}, memfs.NewWorkspace("/query"), memledger.New(), nil)
	return server.PlacementBinding{Ref: env.Ref(), Environment: env}, nil
}
func (queryPlacement) Reattach(_ context.Context, r server.PlacementReattachRequest) (server.PlacementBinding, error) {
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/query", Revision: "v1"}, memfs.NewWorkspace("/query"), memledger.New(), nil)
	return server.PlacementBinding{Ref: r.Ref, Environment: env}, nil
}

func TestCallMcpWithQueryBrokerSupport_Scenario1_AuthorizationContinuation(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, outcome := range []string{"grant", "deny", "cancel", "lost"} {
			t.Run(outcome+map[bool]string{false: "/live", true: "/restart"}[restart], func(t *testing.T) {
				tokens := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`))
				}))
				defer tokens.Close()
				roots := x509.NewCertPool()
				roots.AddCert(tokens.Certificate())
				catalogue, err := Compile(protectedConfig(tokens.URL), []ToolDefinition{{Backend: "github", Name: "mcp__github__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				call := session.NewToolCall("query-call", "CallMcpWithQuery", json.RawMessage(`{"server":"github","tool":"list","args":"{\"page\":1}","jq_filter":".keep"}`))
				expected := session.NewToolCall(call.ID, "mcp__github__list", json.RawMessage(`{"page":1}`))
				rawCaller := func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
					t.Error("raw transport invoked")
					return session.ToolResult{}, nil
				}
				runtime, err := New(catalogue, rawCaller, WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
					t.Error("native transport invoked")
					return session.ToolResult{}, nil
				}), WithQueryCaller(func(ctx context.Context, ref SessionRef, backend string, native session.ToolCall, source oauth2.TokenSource, filter string) (session.ToolResult, error) {
					calls.Add(1)
					if native.ID != expected.ID || native.Name != expected.Name || string(native.Args) != string(expected.Args) || filter != ".keep" || backend != "github" || ref.SessionID() != "authorization-session" {
						t.Errorf("native mismatch: %+v %q", native, filter)
					}
					if _, err := source.Token(); err != nil {
						return session.ToolResult{}, err
					}
					return mcp.FilterCallResult(ctx, native.ID, "github", "list", filter, mcp.CallResult{StructuredContent: json.RawMessage(`{"keep":42,"raw_canary":"secret"}`)})
				}), WithOAuthLoopbackForTest(t, roots), WithOAuthSecretResolver(func(context.Context, string) (string, error) { return "secret", nil }))
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
				store := memstore.New()
				buildHost := func(first bool) *server.Service {
					factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode, tools []tool.Tool) (server.SessionEngineResult, error) {
						cat := tool.NewCatalog()
						for _, candidate := range tools {
							cat.MustRegister(candidate)
						}
						turns := []mockllm.Turn{mockllm.TextTurn("done")}
						if first {
							turns = append([]mockllm.Turn{mockllm.ToolCallTurn(call)}, turns...)
						}
						return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(turns...), Catalog: cat, Policy: queryAllowPolicy{}, Store: store}), BuiltForMode: mode, Close: func() error { return nil }}, nil
					}
					svc, err := server.NewService(server.Config{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Policy: queryAllowPolicy{}}), Store: store, MCPBroker: runtime, SessionEngineWithTools: factory, SessionEngine: func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode) (server.SessionEngineResult, error) {
						return factory(ctx, sel, specs, profile, workspace, mode, nil)
					}, PlacementProvider: queryPlacement{}, PlacementScope: "test", NewID: func() session.SessionID { return "authorization-session" }})
					if err != nil {
						t.Fatal(err)
					}
					return svc
				}
				host := buildHost(true)
				defer host.Close()
				sess, err := host.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				run, err := host.StartInteractiveRunContent(t.Context(), sess.ID, "query", nil)
				if err != nil {
					t.Fatal(err)
				}
				for ev := range run.Events() {
					if ev.ToolResult != nil {
						t.Logf("initial result: %+v", ev.ToolResult)
					}
				}
				host.FinishRun(sess.ID, run)
				parked, err := store.Load(t.Context(), sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				pending, ok := parked.PendingAuthorization()
				if !ok {
					t.Fatalf("did not park: %v", parked.State)
				}
				if pending.Call.Name != call.Name || string(pending.Call.Args) != string(call.Args) || calls.Load() != 0 {
					t.Fatalf("pending envelope changed: %+v", pending)
				}
				attachment, _ := attach(t, runtime, sess.ID)
				attachment.logical.mu.Lock()
				transaction, err := lookupAuthorization(attachment.logical, pending.Authorization)
				attachment.logical.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if transaction.callHash != callHash(expected) || transaction.callID != expected.ID {
					t.Fatal("native hash mismatch")
				}
				foreign, _ := attach(t, runtime, "foreign-session")
				if err := foreign.CallMcpWithQueryTool().(tool.AuthorizationRequester).AbortAuthorization(t.Context(), pending.Authorization); err == nil {
					t.Fatal("foreign attachment cancelled authorization")
				}
				foreignResult, err := foreign.CallMcpWithQueryTool().Execute(t.Context(), call, tool.Environment{})
				if err != nil || !foreignResult.IsError || calls.Load() != 0 {
					t.Fatal("foreign session used another session's grant")
				}
				if restart {
					// Simulate a crash, not graceful shutdown (which cancels pending authorization).
					host = buildHost(false)
					defer host.Close()
				}
				presentation, err := attachment.PresentAuthorization(t.Context(), pending.Authorization)
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := url.Parse(presentation)
				if err != nil {
					t.Fatal(err)
				}
				switch outcome {
				case "grant":
					err = runtime.handleCallback(t.Context(), "code", parsed.Query().Get("state"))
				case "deny":
					err = runtime.handleCallbackError(parsed.Query().Get("state"), "access_denied")
				case "cancel":
					_, err = attachment.CancelAuthorization(t.Context(), pending.Authorization)
				case "lost":
					_, err = runtime.DeleteSession(t.Context(), sess.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				control := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: pending.Authorization.ID}
				result, err := host.RecheckMCPAuthorization(t.Context(), sess.ID, control)
				if outcome == "lost" {
					if err == nil && result.Status == session.AuthorizationGranted {
						t.Fatal("lost binding granted")
					}
					if result.Run != nil {
						for range result.Run.Events() {
						}
						host.FinishRun(sess.ID, result.Run)
					}
					stored, loadErr := store.Load(t.Context(), sess.ID)
					if loadErr != nil || stored.ExternalBinding != parked.ExternalBinding {
						t.Fatal("lost binding was silently adopted")
					}
					if calls.Load() != 0 {
						t.Fatal("lost binding executed")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				found := false
				if result.Run != nil {
					for ev := range result.Run.Events() {
						if ev.ToolResult != nil {
							if strings.Contains(ev.ToolResult.Content, "raw_canary") || strings.Contains(ev.ToolResult.Content, "secret") {
								t.Fatal("raw response escaped")
							}
							if ev.ToolResult.CallID == call.ID && ev.ToolResult.Content == "42" {
								found = true
							}
						}
					}
					host.FinishRun(sess.ID, result.Run)
				}
				if outcome == "grant" {
					if calls.Load() != 1 || !found {
						t.Fatalf("grant calls=%d projection=%v", calls.Load(), found)
					}
					replay, replayErr := attachment.CallMcpWithQueryTool().Execute(t.Context(), call, tool.Environment{})
					if replayErr != nil || !replay.IsError || calls.Load() != 1 {
						t.Fatal("protected query replayed")
					}
					stored, err := store.Load(t.Context(), sess.ID)
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(stored.Conversation)
					if err != nil || strings.Contains(string(encoded), "raw_canary") || strings.Contains(string(encoded), "secret") {
						t.Fatal("raw response persisted")
					}
				} else if calls.Load() != 0 {
					t.Fatalf("%s executed", outcome)
				}
			})
		}
	}
}
