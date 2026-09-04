package mcpbroker

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// newWorkspaceEnrollmentRuntime wires a Runtime + Process pair the way
// newToolHiveProcess does for the OAuth-transaction/discovery pieces this test
// exercises, without pulling in the whole ToolHive vMCP server.
func newWorkspaceEnrollmentRuntime(t *testing.T, tokenServer *httptest.Server, queries *orderedCapabilityQueries, backends ...string) *Runtime {
	t.Helper()
	catalogue := &Catalogue{routes: []route{{backend: "anonymous", spec: tool.ToolSpec{Name: "mcp__anonymous__status", Schema: json.RawMessage(`{"type":"object"}`)}, readOnly: true}}}
	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	target := &oauthRoute{
		authorizationEndpoint: "https://accounts.example/authorize",
		tokenEndpoint:         tokenServer.URL,
		callbackURL:           "https://client.example/oauth/callback",
		clientID:              "broker-client",
		scopes:                []string{"openid", "offline_access"},
		requestRefresh:        true,
	}
	runtime, err := New(catalogue,
		func(_ context.Context, _ SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
			return session.NewToolResult(call.ID, "anonymous"), nil
		},
		WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
			return session.ToolResult{}, errors.New("execution not under test")
		}),
		WithOAuthLoopbackForTest(t, roots),
		withHardenedTokenEndpoint(tokenServer.URL),
	)
	if err != nil {
		t.Fatal(err)
	}
	process := &Process{
		Runtime:            runtime,
		construction:       toolHiveConstruction{protectedBackends: backends},
		protectedTarget:    target,
		queryAuthenticated: queries.query,
		occupied:           []string{"Read"},
		ctx:                context.Background(),
		cancel:             func() {},
	}
	runtime.process = process
	return runtime
}

func TestADR_0298_OpaqueBrokerCredentialIsNotDecodedOrCopied(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-broker-credential","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"github": {Backend: "github", Tools: []ToolDefinition{
			{Backend: "github", Name: "mcp__github__list_issues", Description: "list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		}},
	}}
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, queries, "github")
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	attachment := attached.(*Attachment)
	enroller, ok := attached.(contract.WorkspaceEnrollmentAttachment)
	if !ok {
		t.Fatal("Attachment does not implement WorkspaceEnrollmentAttachment")
	}

	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil || !presentation.Valid() {
		t.Fatalf("BeginWorkspaceEnrollment = (%+v, %v)", presentation, err)
	}
	if presentation.Ref.RequiredServices != 1 {
		t.Fatalf("RequiredServices = %d, want 1", presentation.Ref.RequiredServices)
	}
	parsed, err := url.Parse(presentation.URL)
	if err != nil || parsed.Query().Get("state") == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("presentation URL = %q, %v", presentation.URL, err)
	}

	// Idempotent re-begin while pending returns the same reference.
	again, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil || again.Ref.ID != presentation.Ref.ID {
		t.Fatalf("re-begin = (%+v, %v), want same ref as %+v", again, err, presentation.Ref)
	}

	pending, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || pending.Status != contract.WorkspaceEnrollmentPending {
		t.Fatalf("observe before callback = (%+v, %v)", pending, err)
	}

	if err := runtime.handleCallback(t.Context(), "auth-code", parsed.Query().Get("state")); err != nil {
		t.Fatalf("handleCallback direct: %v", err)
	}

	connected, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || !connected.Valid() || connected.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("observe after grant = (%+v, %v)", connected, err)
	}
	if got, want := connected.Catalogue.ToolNames(), []string{"mcp__anonymous__status", "mcp__github__list_issues"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("frozen catalogue tools = %v, want %v", got, want)
	}
	if got := toolNames(attachment.Tools()); len(got) != 2 {
		t.Fatalf("attachment tools after freeze = %v, want anonymous + discovered", got)
	}
	attachment.logical.mu.RLock()
	backendGrants := len(attachment.logical.grants)
	attachment.logical.mu.RUnlock()
	if backendGrants != 0 {
		t.Fatalf("ToolHive enrollment copied opaque broker credential into %d backend grants", backendGrants)
	}

	// The admitted route uses the already-connected ToolHive broker credential;
	// it does not create a second backend-specific mecatl authorization.
	route, ok := attachment.lookupRoute("mcp__github__list_issues")
	if !ok || route.backend != "github" || !route.broker || route.oauth != nil {
		t.Fatalf("discovered route = %+v, %v", route, ok)
	}
	wrapped := toolByName(t, attachment, route.spec.Name)
	if _, asksAgain := wrapped.(tool.AuthorizationRequester); asksAgain {
		t.Fatal("connected ToolHive wrapper requests a second mecatl authorization")
	}

	// The server may need to retry its own engine replacement or session save after
	// observing this exact result. Reattachment must materialize fresh wrappers from
	// token-free logical state without repeating authenticated discovery.
	if _, err := attachment.Close(t.Context()); err != nil {
		t.Fatalf("close enrolled attachment: %v", err)
	}
	reopened, outcome, err := runtime.AttachSession(t.Context(), "session")
	if err != nil || outcome != contract.AttachReattached {
		t.Fatalf("reattach = (%v, %q, %v)", reopened, outcome, err)
	}
	reobserved, err := reopened.(contract.WorkspaceEnrollmentAttachment).ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || reobserved.Status != contract.WorkspaceEnrollmentConnected || !reflect.DeepEqual(reobserved.Catalogue.ToolNames(), connected.Catalogue.ToolNames()) {
		t.Fatalf("reobserve completed enrollment = (%+v, %v)", reobserved, err)
	}
	queries.mu.Lock()
	calls := queries.calls
	queries.mu.Unlock()
	if calls != 1 {
		t.Fatalf("authenticated discovery calls = %d, want 1", calls)
	}
	route, ok = reopened.(*Attachment).lookupRoute("mcp__github__list_issues")
	if !ok || !route.broker || route.oauth != nil {
		t.Fatalf("reattached broker route = %+v, %v", route, ok)
	}
	if _, err := reopened.(contract.WorkspaceEnrollmentAttachment).BeginWorkspaceEnrollment(t.Context()); !errors.Is(err, errWorkspaceEnrollmentAlreadyCompleted) {
		t.Fatalf("BeginWorkspaceEnrollment after completion error = %v, want completed enrollment rejection", err)
	}
}

func TestWorkspaceEnrollmentControlsRequireExactAggregateReference(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tokenServer.Close()
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github", "calendar")
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	enroller := attached.(contract.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mismatched := presentation.Ref
	mismatched.RequiredServices--
	if _, err := enroller.ObserveWorkspaceEnrollment(t.Context(), mismatched); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("ObserveWorkspaceEnrollment mismatch = %v", err)
	}
	if _, err := enroller.CancelWorkspaceEnrollment(t.Context(), mismatched); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("CancelWorkspaceEnrollment mismatch = %v", err)
	}
	pending, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || pending.Status != contract.WorkspaceEnrollmentPending {
		t.Fatalf("exact enrollment was disturbed = (%+v, %v)", pending, err)
	}
}

func TestWorkspaceEnrollmentCancelClearsBundleState(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"unused","token_type":"Bearer"}`))
	}))
	defer tokenServer.Close()
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{}}
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, queries, "github")
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	enroller := attached.(contract.WorkspaceEnrollmentAttachment)

	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := enroller.CancelWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || cancelled.Status != contract.WorkspaceEnrollmentCancelled {
		t.Fatalf("cancel = (%+v, %v)", cancelled, err)
	}

	// A stale re-observe on the cancelled ref fails closed.
	if _, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref); err == nil {
		t.Fatal("observe after cancel unexpectedly succeeded")
	}

	// Beginning again mints a fresh transaction rather than reusing dead state.
	restarted, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil || restarted.Ref.ID == presentation.Ref.ID {
		t.Fatalf("restart after cancel = (%+v, %v)", restarted, err)
	}
}

func TestWorkspaceEnrollmentUnsupportedWithoutDynamicBackend(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tokenServer.Close()
	runtime := testAnonymousRuntime(t)
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	enroller, ok := attached.(contract.WorkspaceEnrollmentAttachment)
	if !ok {
		t.Fatal("Attachment does not implement WorkspaceEnrollmentAttachment")
	}
	if _, err := enroller.BeginWorkspaceEnrollment(t.Context()); !errors.Is(err, ErrWorkspaceEnrollmentUnsupported) {
		t.Fatalf("begin without process = %v, want ErrWorkspaceEnrollmentUnsupported", err)
	}
}
