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

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"golang.org/x/oauth2"
)

type queryAllowPolicy struct{}

func (queryAllowPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) port.PermissionResult {
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Allow}}
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

// Native query authorization remains internal; host continuation is covered by
// the canonical SessionAPI tests rather than the retired host attachment path.
func TestCallMcpWithQueryNativeAuthorizationContinuation(t *testing.T) {
	for _, outcome := range []string{"grant", "deny", "cancel", "lost"} {
		t.Run(outcome, func(t *testing.T) {
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
			runtime, err := New(catalogue, func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
				t.Error("raw transport invoked")
				return session.ToolResult{}, nil
			}, WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
				t.Error("unfiltered transport invoked")
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
			}), WithOAuthLoopbackForTest(t, roots), WithOAuthSecretFileReader(func(context.Context, string) (string, error) { return "secret", nil }))
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			attachment, _ := attach(t, runtime, "authorization-session")
			query := attachment.CallMcpWithQueryTool()
			auth, required, err := query.(tool.AuthorizationRequester).RequestAuthorization(t.Context(), call)
			if err != nil || !required || calls.Load() != 0 {
				t.Fatalf("preflight: %v %v", required, err)
			}
			attachment.logical.mu.Lock()
			transaction, err := lookupAuthorization(attachment.logical, auth)
			attachment.logical.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if transaction.callHash != callHash(expected) || transaction.callID != expected.ID {
				t.Fatal("native hash mismatch")
			}
			foreign, _ := attach(t, runtime, "foreign-session")
			if err := foreign.CallMcpWithQueryTool().(tool.AuthorizationRequester).AbortAuthorization(t.Context(), auth); err == nil {
				t.Fatal("foreign attachment cancelled authorization")
			}
			foreignResult, err := foreign.CallMcpWithQueryTool().Execute(t.Context(), call, tool.Environment{})
			if err != nil || !foreignResult.IsError || calls.Load() != 0 {
				t.Fatal("foreign session used another session's grant")
			}
			presentation, err := attachment.PresentAuthorization(t.Context(), auth)
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
				_, err = attachment.CancelAuthorization(t.Context(), auth)
			case "lost":
				_, err = runtime.DeleteSession(t.Context(), "authorization-session")
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := query.Execute(t.Context(), call, tool.Environment{})
			if outcome == "grant" {
				if err != nil || result.IsError || result.Content != "42" || calls.Load() != 1 {
					t.Fatalf("grant: %+v %v calls=%d", result, err, calls.Load())
				}
				replay, err := query.Execute(t.Context(), call, tool.Environment{})
				if err != nil || !replay.IsError || calls.Load() != 1 {
					t.Fatal("protected query replayed")
				}
			} else if calls.Load() != 0 {
				t.Fatalf("%s executed", outcome)
			}
			if strings.Contains(result.Content, "raw_canary") || strings.Contains(result.Content, "secret") {
				t.Fatal("raw response escaped")
			}
		})
	}
}
