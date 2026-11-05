package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type grantedSchemaTool struct{ spec tool.ToolSpec }

func (t grantedSchemaTool) Spec() tool.ToolSpec { return t.spec }
func (grantedSchemaTool) ReadOnly() bool        { return true }
func (grantedSchemaTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

func grantingTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPrepareGrantedAuthorizationReturnsCatalogueWithQueryToolAndVerdict(t *testing.T) {
	harness := newProtectedHarness(t, grantingTokenServer(t))
	harness.runtime.queryCaller = func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error) {
		return session.ToolResult{}, errors.New("query must not run")
	}
	attachment, _ := attach(t, harness.runtime, "granted-prepare")
	call := session.NewToolCall("granted-call", "mcp__github__create", json.RawMessage(`{}`))
	authorization, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}

	granted, err := attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: authorization, Call: call})
	if err != nil {
		t.Fatalf("PrepareGrantedAuthorization: %v", err)
	}
	if got := toolNames(granted.Tools); !reflect.DeepEqual(got, []string{"mcp__github__create", mcpadapter.CallMcpWithQuerySpec().Name}) {
		t.Fatalf("granted tools = %v, want the catalogue plus CallMcpWithQuery", got)
	}
	if granted.ArgumentsErr != nil {
		t.Fatalf("conforming parked arguments refused: %v", granted.ArgumentsErr)
	}

	malformed := session.NewToolCall("granted-call", "mcp__github__create", json.RawMessage(`[]`))
	granted, err = attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: authorization, Call: malformed})
	if err != nil || granted.ArgumentsErr == nil {
		t.Fatalf("PrepareGrantedAuthorization(non-object args) = (%v, %v), want an arguments verdict and no error", granted.ArgumentsErr, err)
	}

	queryName := mcpadapter.CallMcpWithQuerySpec().Name
	query := session.NewToolCall("granted-call", queryName, json.RawMessage(`{"server":"github","tool":"create","args":{},"jq_filter":"."}`))
	granted, err = attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: authorization, Call: query})
	if err != nil || granted.ArgumentsErr != nil {
		t.Fatalf("PrepareGrantedAuthorization(parked CallMcpWithQuery) = (%v, %v), want a conforming verdict", granted.ArgumentsErr, err)
	}
	foreign := session.NewToolCall("granted-call", queryName, json.RawMessage(`{"server":"github","tool":"missing","args":{},"jq_filter":"."}`))
	granted, err = attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: authorization, Call: foreign})
	if err != nil || granted.ArgumentsErr == nil {
		t.Fatalf("PrepareGrantedAuthorization(CallMcpWithQuery to an unknown target) = (%v, %v), want an arguments verdict", granted.ArgumentsErr, err)
	}

	forged := authorization
	forged.Binding = "forged-binding"
	if _, err := attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: forged, Call: call}); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("forged authorization error = %v, want ErrAuthorizationNotFound", err)
	}
}

func TestPrepareGrantedAuthorizationWithoutQueryToolRefusesParkedQueryCall(t *testing.T) {
	harness := newProtectedHarness(t, grantingTokenServer(t))
	attachment, _ := attach(t, harness.runtime, "granted-no-query")
	call := session.NewToolCall("granted-call", "mcp__github__create", json.RawMessage(`{}`))
	authorization, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	query := session.NewToolCall("granted-call", mcpadapter.CallMcpWithQuerySpec().Name, json.RawMessage(`{"server":"github","tool":"create","args":{},"jq_filter":"."}`))
	granted, err := attachment.PrepareGrantedAuthorization(t.Context(), session.PendingAuthorization{Authorization: authorization, Call: query})
	if err != nil {
		t.Fatalf("PrepareGrantedAuthorization: %v", err)
	}
	if got := toolNames(granted.Tools); !reflect.DeepEqual(got, []string{"mcp__github__create"}) {
		t.Fatalf("granted tools = %v, want only the catalogue", got)
	}
	if granted.ArgumentsErr == nil {
		t.Fatal("parked CallMcpWithQuery accepted without a CallMcpWithQuery tool")
	}
}

func TestAttachmentSettlingOperations(t *testing.T) {
	harness := newProtectedHarness(t, grantingTokenServer(t))
	attachment, _ := attach(t, harness.runtime, "settling")
	request := func(id string) session.ExternalAuthorization {
		authorization, _ := requestProtected(t, attachment, session.NewToolCall(session.ToolCallID(id), "mcp__github__create", json.RawMessage(`{}`)))
		return authorization
	}

	presented := request("present")
	if url, err := contract.PresentPendingAuthorization(t.Context(), attachment, presented); err != nil || url == "" {
		t.Fatalf("PresentPendingAuthorization(pending) = (%q, %v), want a live URL", url, err)
	}
	if status, err := contract.SettleAuthorization(t.Context(), attachment, presented, session.AuthorizationCancelled); err != nil || status != session.AuthorizationCancelled {
		t.Fatalf("SettleAuthorization(pending) = (%q, %v)", status, err)
	}
	if status, err := contract.SettleAuthorization(t.Context(), attachment, presented, session.AuthorizationExpired); err != nil || status != session.AuthorizationCancelled {
		t.Fatalf("SettleAuthorization(cancelled) = (%q, %v)", status, err)
	}
	if _, err := contract.PresentPendingAuthorization(t.Context(), attachment, presented); !errors.Is(err, contract.ErrAuthorizationNotPending) {
		t.Fatalf("PresentPendingAuthorization(cancelled) error = %v, want ErrAuthorizationNotPending", err)
	}
	if status, err := contract.ExpireAuthorization(t.Context(), attachment, presented); err != nil || status != session.AuthorizationCancelled {
		t.Fatalf("ExpireAuthorization(cancelled) = (%q, %v), want the unchanged cancelled status", status, err)
	}

	expired := request("expire")
	if status, err := contract.ExpireAuthorization(t.Context(), attachment, expired); err != nil || status != session.AuthorizationExpired {
		t.Fatalf("ExpireAuthorization(pending) = (%q, %v), want expired", status, err)
	}

	unknown := expired
	unknown.Binding = "forged-binding"
	var cancelFailed *contract.CancelFailedError
	if _, err := contract.SettleAuthorization(t.Context(), attachment, unknown, session.AuthorizationCancelled); !errors.Is(err, contract.ErrAuthorizationNotFound) || !errors.As(err, &cancelFailed) {
		t.Fatalf("SettleAuthorization(unknown) error = %v, want a failed cancellation with ErrAuthorizationNotFound", err)
	}
}

func TestValidateGrantedAuthorizationArgumentsFailsClosed(t *testing.T) {
	validSchema := json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)
	cases := []struct {
		name   string
		schema json.RawMessage
		args   json.RawMessage
		wantOK bool
	}{
		{name: "valid", schema: validSchema, args: json.RawMessage(`{"title":"ok"}`), wantOK: true},
		{name: "malformed schema", schema: json.RawMessage(`{`), args: json.RawMessage(`{}`)},
		{name: "non-object schema", schema: json.RawMessage(`true`), args: json.RawMessage(`{}`)},
		{name: "unresolvable schema", schema: json.RawMessage(`{"$ref":"https://schemas.example/required.json"}`), args: json.RawMessage(`{}`)},
		{name: "malformed args", schema: validSchema, args: json.RawMessage(`{`)},
		{name: "non-object args", schema: validSchema, args: json.RawMessage(`[]`)},
		{name: "schema mismatch", schema: validSchema, args: json.RawMessage(`{"title":1}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := []tool.Tool{grantedSchemaTool{spec: tool.ToolSpec{Name: "protected", Schema: tc.schema}}}
			err := validateGrantedAuthorizationArguments(tools, session.NewToolCall("call", "protected", tc.args))
			if (err == nil) != tc.wantOK {
				t.Fatalf("validateGrantedAuthorizationArguments() error = %v, wantOK %v", err, tc.wantOK)
			}
		})
	}
}

// TestValidateGrantedAuthorizationArgumentsCanonicalizesQueryArgs pins the same
// JSON-encoded args-object recovery that CallMcpWithQuery applies before invoking a
// remote protected target. The transition validates the recovered target against
// the authenticated target schema, so restart/lazy continuation preserves that
// execution-compatible input without accepting malformed remote arguments.
func TestValidateGrantedAuthorizationArgumentsCanonicalizesQueryArgs(t *testing.T) {
	querySpec := mcpadapter.CallMcpWithQuerySpec()
	targetSpec := tool.ToolSpec{Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)}
	tools := []tool.Tool{grantedSchemaTool{spec: querySpec}, grantedSchemaTool{spec: targetSpec}}
	cases := []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{name: "encoded object", args: json.RawMessage(`{"server":"github","tool":"create","args":"{\"title\":\"ok\"}","jq_filter":"."}`), want: true},
		{name: "encoded malformed object", args: json.RawMessage(`{"server":"github","tool":"create","args":"{","jq_filter":"."}`)},
		{name: "encoded non-object", args: json.RawMessage(`{"server":"github","tool":"create","args":"[]","jq_filter":"."}`)},
		{name: "authenticated target mismatch", args: json.RawMessage(`{"server":"github","tool":"create","args":"{\"title\":1}","jq_filter":"."}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGrantedAuthorizationArguments(tools, session.NewToolCall("query", querySpec.Name, tc.args))
			if (err == nil) != tc.want {
				t.Fatalf("validateGrantedAuthorizationArguments() error = %v, want success %v", err, tc.want)
			}
		})
	}
}
