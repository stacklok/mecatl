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
	"sync"
	"testing"
	"time"

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

func TestWorkspaceEnrollmentTerminalObservationIsRetryable(t *testing.T) {
	for _, test := range []struct {
		name, oauthError string
		expire           bool
		want             contract.WorkspaceEnrollmentStatus
	}{
		{name: "denied", oauthError: "access_denied", want: contract.WorkspaceEnrollmentDenied},
		{name: "failed", oauthError: "server_error", want: contract.WorkspaceEnrollmentFailed},
		{name: "expired", expire: true, want: contract.WorkspaceEnrollmentExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("terminal authorization must not exchange a token")
			}))
			defer tokenServer.Close()
			runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github")
			attached, _, err := runtime.AttachSession(t.Context(), session.SessionID("terminal-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			enroller := attached.(contract.WorkspaceEnrollmentAttachment)
			presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if test.expire {
				runtime.oauth.now = func() time.Time { return presentation.Ref.ExpiresAt }
			} else {
				parsed, parseErr := url.Parse(presentation.URL)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				request := httptest.NewRequest(http.MethodGet, "/callback?"+url.Values{"error": {test.oauthError}, "state": {parsed.Query().Get("state")}}.Encode(), nil)
				response := httptest.NewRecorder()
				runtime.CallbackHandler().ServeHTTP(response, request)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("callback status = %d", response.Code)
				}
			}
			for attempt := range 2 {
				result, observeErr := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
				if observeErr != nil || result.Status != test.want || result.Ref != presentation.Ref || !result.Valid() || result.Catalogue != nil {
					t.Fatalf("observe attempt %d = (%+v, %v), want valid %q result with no catalogue", attempt+1, result, observeErr, test.want)
				}
			}
			fresh, err := enroller.BeginWorkspaceEnrollment(t.Context())
			if err != nil || fresh.Ref.ID == presentation.Ref.ID {
				t.Fatalf("begin after terminal acknowledgement = (%+v, %v)", fresh, err)
			}
		})
	}
}

func TestWorkspaceEnrollmentDiscoveryCancellationIsRetryable(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github")
	attached, _, err := runtime.AttachSession(t.Context(), "cancel-retry")
	if err != nil {
		t.Fatal(err)
	}
	attachment := attached.(*Attachment)
	enroller := attached.(contract.WorkspaceEnrollmentAttachment)
	presentation := beginAndGrantWorkspaceEnrollment(t, runtime, enroller)

	entered := make(chan struct{})
	var mu sync.Mutex
	attempts := 0
	runtime.process.queryAuthenticated = func(ctx context.Context, _ oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		if attempt == 1 {
			close(entered)
			<-ctx.Done()
			return AuthenticatedCapabilities{}, ctx.Err()
		}
		return AuthenticatedCapabilities{Backend: backend, Tools: []ToolDefinition{{Backend: backend, Name: "mcp__github__list", Schema: json.RawMessage(`{"type":"object"}`)}}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan error, 1)
	go func() {
		_, err := enroller.ObserveWorkspaceEnrollment(ctx, presentation.Ref)
		observed <- err
	}()
	<-entered
	cancel()
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery error = %v, want context.Canceled", err)
	}

	attachment.logical.mu.RLock()
	transaction := lookupWorkspaceTransactionLocked(attachment.logical, presentation.Ref.ID)
	credential := attachment.logical.brokerCredential
	attachment.logical.mu.RUnlock()
	if transaction == nil || transaction.status != session.AuthorizationGranted || credential == nil {
		t.Fatalf("cancelled discovery consumed grant: transaction=%+v credential=%v", transaction, credential)
	}
	connected, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || connected.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("retry discovery = (%+v, %v)", connected, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("discovery attempts = %d, want 2", attempts)
	}
}

func TestCancelledPeerCannotPublishUncommittedCatalogue(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github")
	firstRaw, _, err := runtime.AttachSession(t.Context(), "cancel-publish")
	if err != nil {
		t.Fatal(err)
	}
	peerRaw, _, err := runtime.AttachSession(t.Context(), "cancel-publish")
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(*Attachment)
	firstEnroller := firstRaw.(contract.WorkspaceEnrollmentAttachment)
	peerEnroller := peerRaw.(contract.WorkspaceEnrollmentAttachment)
	presentation := beginAndGrantWorkspaceEnrollment(t, runtime, firstEnroller)

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	attempts := 0
	runtime.process.queryAuthenticated = func(_ context.Context, _ oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		if attempt == 1 {
			close(entered)
			<-release
		}
		return AuthenticatedCapabilities{Backend: backend, Tools: []ToolDefinition{{Backend: backend, Name: "mcp__github__list", Schema: json.RawMessage(`{"type":"object"}`)}}}, nil
	}

	observed := make(chan error, 1)
	go func() {
		_, err := firstEnroller.ObserveWorkspaceEnrollment(context.Background(), presentation.Ref)
		observed <- err
	}()
	<-entered
	cancelled, err := peerEnroller.CancelWorkspaceEnrollment(t.Context(), presentation.Ref)
	if err != nil || cancelled.Status != contract.WorkspaceEnrollmentCancelled {
		t.Fatalf("peer cancellation = (%+v, %v)", cancelled, err)
	}
	close(release)
	if err := <-observed; !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("discovery after peer cancellation = %v, want state unavailable", err)
	}
	if got := toolNames(first.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
		t.Fatalf("uncommitted catalogue published: %v", got)
	}

	retry := beginAndGrantWorkspaceEnrollment(t, runtime, firstEnroller)
	connected, err := firstEnroller.ObserveWorkspaceEnrollment(t.Context(), retry.Ref)
	if err != nil || connected.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("fresh enrollment after cancelled discovery = (%+v, %v)", connected, err)
	}
}

func beginAndGrantWorkspaceEnrollment(t *testing.T, runtime *Runtime, enroller contract.WorkspaceEnrollmentAttachment) contract.WorkspaceEnrollmentPresentation {
	t.Helper()
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(presentation.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.handleCallback(t.Context(), "auth-code", parsed.Query().Get("state")); err != nil {
		t.Fatalf("grant workspace enrollment: %v", err)
	}
	return presentation
}
