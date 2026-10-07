package mcpbroker

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestExpiredCallbackStatesReleaseCapacityBeforeLogicalRetention(t *testing.T) {
	catalogue, err := Compile(protectedConfig("https://tokens.example/token"), []ToolDefinition{{Backend: "github", Name: "mcp__github__create"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue,
		func(_ context.Context, _ SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
			return session.NewToolResult(call.ID, "ok"), nil
		},
		WithAuthorizedCaller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall, _ oauth2.TokenSource) (session.ToolResult, error) {
			return session.NewToolResult(call.ID, "ok"), nil
		}),
		WithOAuthSecretFileReader(func(context.Context, string) (string, error) { return "secret", nil }),
		WithOAuthLimits(15*time.Millisecond, time.Second),
		WithLimits(Limits{MaxPendingStates: 1, SweepInterval: time.Millisecond, LogicalRetention: time.Hour}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	attachment, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	localSessionHandle, ok := attachment.(*Attachment)
	if !ok {
		t.Fatal("runtime returned unexpected attachment type")
	}
	requester := toolByName(t, localSessionHandle, "mcp__github__create").(tool.AuthorizationRequester)
	first, required, err := requester.RequestAuthorization(t.Context(), session.ToolCall{ID: "first", Name: "mcp__github__create", Args: []byte(`{}`)})
	if err != nil || !required {
		t.Fatalf("first authorization = (%+v, %v, %v)", first, required, err)
	}
	runtime.stateMu.Lock()
	state := runtime.states
	var transaction *authorizationTransaction
	for _, indexed := range state {
		transaction = indexed.transaction
	}
	runtime.stateMu.Unlock()
	if transaction == nil {
		t.Fatal("missing pending callback transaction")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.stateMu.Lock()
		remaining := len(runtime.states)
		runtime.stateMu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	localSessionHandle.logical.mu.Lock()
	retainedSecret := transaction.clientSecret != "" || transaction.verifier != "" || transaction.state != ""
	localSessionHandle.logical.mu.Unlock()
	if retainedSecret {
		t.Fatal("expired transaction retained secret callback material")
	}
	second, required, err := requester.RequestAuthorization(t.Context(), session.ToolCall{ID: "second", Name: "mcp__github__create", Args: []byte(`{}`)})
	if err != nil || !required || second.ID == "" {
		t.Fatalf("expired callback capacity was not recovered: (%+v, %v, %v)", second, required, err)
	}
}

func TestCompileRejectsPlaintextProtectedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"authorization", "token", "callback"} {
		t.Run(endpoint, func(t *testing.T) {
			config := protectedConfig("https://tokens.example/token")
			route := &config.Routes[0]
			switch endpoint {
			case "authorization":
				route.Auth.OAuth.Upstream.OAuth2.AuthorizationEndpoint = "http://accounts.example/authorize"
			case "token":
				route.Auth.OAuth.Upstream.OAuth2.TokenEndpoint = "http://tokens.example/token"
			case "callback":
				config.CallbackURL = "http://client.example/oauth/callback"
			}
			if _, err := Compile(config, []ToolDefinition{{Backend: "github", Name: "mcp__github__create"}}, nil); err == nil {
				t.Fatal("Compile accepted a plaintext protected endpoint")
			}
		})
	}
}

func TestInvariant_singleton_broker_loopback_relaxation_is_test_only(t *testing.T) {
	var requests atomic.Int32
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	catalogue, err := Compile(protectedConfig(tokenServer.URL), []ToolDefinition{{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := AuthorizedCaller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall, _ oauth2.TokenSource) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "ok"), nil
	})
	anonymous := Caller(func(_ context.Context, _ SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "ok"), nil
	})
	production, err := New(catalogue, anonymous, WithAuthorizedCaller(caller))
	if err != nil {
		t.Fatalf("production runtime: %v", err)
	}
	defer production.Close()
	request, err := http.NewRequest(http.MethodPost, tokenServer.URL, strings.NewReader("grant_type=authorization_code"))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("client", "secret")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if response, requestErr := production.oauth.httpClient.Do(request); requestErr == nil {
		_ = response.Body.Close()
		t.Fatal("production OAuth client reached a loopback token endpoint")
	}
	if requests.Load() != 0 {
		t.Fatalf("production OAuth client dispatched %d loopback requests", requests.Load())
	}

	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	testRuntime, err := New(catalogue, anonymous, WithAuthorizedCaller(caller), WithOAuthLoopbackForTest(t, roots))
	if err != nil {
		t.Fatalf("test runtime: %v", err)
	}
	defer testRuntime.Close()
	request, err = http.NewRequest(http.MethodPost, tokenServer.URL, strings.NewReader("grant_type=authorization_code"))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("client", "secret")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := testRuntime.oauth.httpClient.Do(request)
	if err != nil {
		t.Fatalf("test-only loopback request: %v", err)
	}
	_ = response.Body.Close()
	if requests.Load() != 1 {
		t.Fatalf("test-only OAuth client dispatched %d requests, want 1", requests.Load())
	}
}

func protectedConfig(tokenURL string) mcpauthority.BrokerConfig {
	return mcpauthority.BrokerConfig{
		CallbackURL: "https://client.example/oauth/callback",
		Routes: []permconfig.MCPServerProfile{{
			Name: "github", URL: "https://mcp.example/mcp",
			Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{
					AuthorizationEndpoint: "https://accounts.example/authorize", TokenEndpoint: tokenURL,
				}},
				Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "client-id", SecretFile: "testdata/client-secret"}},
				Scopes: []string{"issues:write"}, RequestRefreshToken: true,
			}},
		}},
	}
}

type protectedHarness struct {
	runtime *Runtime
	tokens  []*oauth2.Token
	mu      sync.Mutex
}

func newProtectedHarness(t *testing.T, tokenServer *httptest.Server) *protectedHarness {
	t.Helper()
	catalogue, err := Compile(protectedConfig(tokenServer.URL), []ToolDefinition{{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	harness := &protectedHarness{}
	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	authorized := func(_ context.Context, _ SessionRef, backend string, call session.ToolCall, source oauth2.TokenSource) (session.ToolResult, error) {
		if backend != "github" {
			return session.ToolResult{}, fmt.Errorf("unexpected backend %q", backend)
		}
		token, err := source.Token()
		if err != nil {
			return session.ToolResult{}, err
		}
		harness.mu.Lock()
		harness.tokens = append(harness.tokens, token)
		harness.mu.Unlock()
		return session.NewToolResult(call.ID, "created"), nil
	}
	runtime, err := New(catalogue,
		func(_ context.Context, _ SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
			return session.NewToolResult(call.ID, "anonymous"), nil
		},
		WithAuthorizedCaller(authorized),
		WithOAuthLoopbackForTest(t, roots),
		WithOAuthSecretFileReader(func(context.Context, string) (string, error) { return "client-secret", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.runtime = runtime
	return harness
}

func requestProtected(t *testing.T, attachment *Attachment, call session.ToolCall) (session.ExternalAuthorization, string) {
	t.Helper()
	candidate := toolByName(t, attachment, call.Name)
	requester, ok := candidate.(tool.AuthorizationRequester)
	if !ok {
		t.Fatal("protected wrapper lacks AuthorizationRequester")
	}
	authorization, required, err := requester.RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", authorization, required, err)
	}
	if authorization.ID == "" || authorization.DisplayName != "github" || authorization.Binding == "" || authorization.ExpiresAt.IsZero() {
		t.Fatalf("incomplete safe authorization: %+v", authorization)
	}
	presentation, err := attachment.PresentAuthorization(t.Context(), authorization)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(presentation)
	if err != nil || parsed.Query().Get("state") == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("presentation URL = %q, %v", presentation, err)
	}
	if got := parsed.Query().Get("resource"); got == "" {
		t.Fatalf("presentation URL %q is missing the RFC 8707 resource indicator", presentation)
	}
	return authorization, parsed.Query().Get("state")
}

func callback(t *testing.T, runtime *Runtime, code, state string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil)
	runtime.CallbackHandler().ServeHTTP(recorder, request)
	return recorder
}

func TestADR_0310_LazyAuthorizationDoesNotDiscoverUndeclaredTools(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("requesting lazy authorization must not contact discovery or token endpoints")
	}))
	t.Cleanup(tokenServer.Close)
	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "lazy-static-session")
	before := toolNames(attachment.Tools())
	wrapped := toolByName(t, attachment, "mcp__github__create")
	protected, ok := wrapped.(*protectedSessionTool)
	if !ok {
		t.Fatal("static protected tool lacks protected wrapper")
	}
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{"github": {
		Backend: "github", Tools: []ToolDefinition{{Backend: "github", Name: "mcp__github__undeclared", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}},
	}}}
	harness.runtime.process = &Process{
		Runtime:            harness.runtime,
		construction:       toolHiveConstruction{protectedBackends: []string{"github"}},
		protectedTarget:    protected.route.oauth,
		queryAuthenticated: queries.query,
	}
	requester, ok := wrapped.(tool.AuthorizationRequester)
	if !ok {
		t.Fatal("static protected tool lacks lazy authorization")
	}
	call := session.NewToolCall("lazy-call", wrapped.Spec().Name, json.RawMessage(`{}`))
	if _, required, err := requester.RequestAuthorization(t.Context(), call); err != nil || !required {
		t.Fatalf("RequestAuthorization = required %v, err %v", required, err)
	}
	after := toolNames(attachment.Tools())
	if queries.calls != 0 {
		t.Fatalf("lazy authorization performed %d authenticated discovery queries", queries.calls)
	}
	if _, ok := attachment.lookupRoute("mcp__github__undeclared"); ok {
		t.Fatal("lazy authorization published an undeclared authenticated tool")
	}
	if len(after) != len(before) || len(after) != 1 || after[0] != before[0] {
		t.Fatalf("lazy authorization changed catalogue from %v to %v", before, after)
	}
}

func TestADR_0326_LazyGrantReplacesDeclaredMetadata(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	harness.runtime.catalogue.routes[0].broker = true
	declared := harness.runtime.catalogue.routes[0]
	declared.spec.Description = "static"
	harness.runtime.catalogue.routes[0] = declared
	absent := declared
	absent.spec = tool.ToolSpec{Name: "mcp__github__absent", Description: "static absent", Schema: json.RawMessage(`{"type":"object"}`)}
	slack := declared
	slack.backend = "slack"
	slack.spec = tool.ToolSpec{Name: "mcp__slack__list", Description: "static slack", Schema: json.RawMessage(`{"type":"object"}`)}
	harness.runtime.catalogue.routes = append(harness.runtime.catalogue.routes, absent, slack)
	attachment, _ := attach(t, harness.runtime, "lazy-refresh")
	protected := toolByName(t, attachment, "mcp__github__create").(*protectedSessionTool)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"github": {
			Backend: "github", Tools: []ToolDefinition{
				{Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`{"type":"object","properties":{"live":{"type":"boolean"}}}`), ReadOnly: true},
				{Backend: "github", Name: "mcp__github__undeclared", Description: "hidden", Schema: json.RawMessage(`{"type":"object"}`)},
			},
		},
		"slack": {
			Backend: "slack", Tools: []ToolDefinition{
				{Backend: "slack", Name: "mcp__slack__list", Description: "live slack", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
			},
		},
	}}
	harness.runtime.process = &Process{Runtime: harness.runtime, construction: toolHiveConstruction{protectedBackends: []string{"github", "slack"}}, protectedTarget: protected.route.oauth, queryAuthenticated: queries.query}
	authorization, state := requestProtected(t, attachment, session.NewToolCall("lazy-refresh", protected.Spec().Name, json.RawMessage(`{}`)))
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	forged := authorization
	forged.Binding = "forged-binding"
	if _, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), forged); !errors.Is(err, contract.ErrAuthorizationNotFound) {
		t.Fatalf("forged authorization refresh error = %v", err)
	}
	if got := toolByName(t, attachment, "mcp__github__create").Spec().Description; got != "static" {
		t.Fatalf("forged authorization changed description to %q", got)
	}
	tools, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization)
	if err != nil {
		t.Fatalf("RefreshGrantedAuthorizationCatalogue: %v", err)
	}
	if got := toolNames(tools); !reflect.DeepEqual(got, []string{"mcp__github__create", "mcp__slack__list"}) {
		t.Fatalf("refreshed tools = %v, want declared live tools only", got)
	}
	if !reflect.DeepEqual(queries.order, []string{"github", "slack"}) {
		t.Fatalf("authenticated queries = %v, want complete configured bundle", queries.order)
	}
	live := toolByName(t, attachment, "mcp__github__create")
	if spec := live.Spec(); spec.Description != "live" || !live.ReadOnly() || !strings.Contains(string(spec.Schema), "live") {
		t.Fatalf("live metadata = %+v readOnly=%v", spec, live.ReadOnly())
	}
	if got := toolByName(t, attachment, "mcp__slack__list").Spec().Description; got != "live slack" {
		t.Fatalf("second backend description = %q, want live slack", got)
	}
	if _, ok := attachment.lookupRoute("mcp__github__absent"); ok {
		t.Fatal("absent declared tool remained published")
	}
	if _, ok := attachment.lookupRoute("mcp__github__undeclared"); ok {
		t.Fatal("undeclared authenticated tool was published")
	}
}

func TestADR_0326_LazyGrantRefreshFailureIsAtomic(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	harness.runtime.catalogue.routes[0].broker = true
	declared := harness.runtime.catalogue.routes[0]
	slack := declared
	slack.backend = "slack"
	slack.spec.Name = "mcp__slack__list"
	harness.runtime.catalogue.routes = append(harness.runtime.catalogue.routes, slack)
	attachment, _ := attach(t, harness.runtime, "lazy-refresh-failure")
	protected := toolByName(t, attachment, "mcp__github__create").(*protectedSessionTool)
	queries := &orderedCapabilityQueries{
		responses: map[string]AuthenticatedCapabilities{"github": {
			Backend: "github", Tools: []ToolDefinition{{Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)}},
		}},
		fail: "slack",
	}
	process := &Process{Runtime: harness.runtime, construction: toolHiveConstruction{protectedBackends: []string{"github", "slack"}}, protectedTarget: protected.route.oauth, queryAuthenticated: queries.query}
	harness.runtime.process = process
	type staticMetadata struct {
		name, description, schema string
		readOnly                  bool
	}
	static := func(tools []tool.Tool) []staticMetadata {
		metadata := make([]staticMetadata, 0, len(tools))
		for _, candidate := range tools {
			spec := candidate.Spec()
			metadata = append(metadata, staticMetadata{spec.Name, spec.Description, string(spec.Schema), candidate.ReadOnly()})
		}
		return metadata
	}
	before := static(attachment.Tools())
	authorization, state := requestProtected(t, attachment, session.NewToolCall("lazy-refresh-failure", protected.Spec().Name, json.RawMessage(`{}`)))
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	assertStatic := func(label string) {
		t.Helper()
		if got := static(attachment.Tools()); !reflect.DeepEqual(got, before) {
			t.Fatalf("%s published metadata = %#v, want unchanged static declarations %#v", label, got, before)
		}
	}
	if _, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("RefreshGrantedAuthorizationCatalogue query error = %v", err)
	}
	assertStatic("failed backend query")

	queries.mu.Lock()
	queries.fail = ""
	queries.responses["github"] = AuthenticatedCapabilities{Backend: "github", Tools: []ToolDefinition{{
		Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`[]`),
	}}}
	queries.responses["slack"] = AuthenticatedCapabilities{Backend: "slack"}
	queries.mu.Unlock()
	if _, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("RefreshGrantedAuthorizationCatalogue validation error = %v", err)
	}
	assertStatic("invalid metadata")

	queries.mu.Lock()
	queries.responses["github"] = AuthenticatedCapabilities{Backend: "github", Tools: []ToolDefinition{{
		Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`{"type":"object"}`),
	}}}
	queries.responses["slack"] = AuthenticatedCapabilities{Backend: "slack", Tools: []ToolDefinition{{
		Backend: "slack", Name: "mcp__slack__list", Description: "live", Schema: json.RawMessage(`{"type":"object"}`),
	}}}
	queries.started = make(chan struct{})
	queries.startedOnce = sync.Once{}
	queries.release = make(chan struct{})
	started := queries.started
	release := queries.release
	queries.mu.Unlock()
	refreshDone := make(chan error, 1)
	go func() {
		_, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization)
		refreshDone <- err
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- process.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		process.lifecycleMu.Lock()
		closed := process.closed
		process.lifecycleMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process close did not enter lifecycle race")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-refreshDone; err == nil {
		t.Fatal("process-close race published a catalogue")
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Process.Close: %v", err)
	}
	assertStatic("process-close race")
}

// TestADR_0326_LazyGrantRefreshRejectsInvalidUndeclaredMetadata pins the
// validation-ordering fix: an invalid/oversized undeclared authenticated
// definition must abort the whole refresh, not be silently dropped while a
// valid declared subset still publishes. Before the fix,
// stageAuthenticatedDeclaredRoutes filtered to declared names BEFORE calling
// validateAuthenticatedRoute, so an undeclared definition never reached the
// shared admission boundary at all.
func TestADR_0326_LazyGrantRefreshRejectsInvalidUndeclaredMetadata(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	harness.runtime.catalogue.routes[0].broker = true
	attachment, _ := attach(t, harness.runtime, "lazy-refresh-invalid-undeclared")
	protected := toolByName(t, attachment, "mcp__github__create").(*protectedSessionTool)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"github": {
			Backend: "github", Tools: []ToolDefinition{
				{Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)},
				// Invalid: empty schema fails validateAuthenticatedRoute. This
				// definition is not declared, so the pre-fix code never
				// validated it and would have published the declared subset
				// above regardless.
				{Backend: "github", Name: "mcp__github__undeclared", Description: "hidden", Schema: json.RawMessage(``)},
			},
		},
	}}
	harness.runtime.process = &Process{Runtime: harness.runtime, construction: toolHiveConstruction{protectedBackends: []string{"github"}}, protectedTarget: protected.route.oauth, queryAuthenticated: queries.query}
	authorization, state := requestProtected(t, attachment, session.NewToolCall("lazy-refresh-invalid-undeclared", protected.Spec().Name, json.RawMessage(`{}`)))
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if _, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization); !errors.Is(err, ErrInvalidCatalogue) {
		t.Fatalf("RefreshGrantedAuthorizationCatalogue error = %v, want ErrInvalidCatalogue", err)
	}
	if got := toolByName(t, attachment, "mcp__github__create").Spec().Description; got == "live" {
		t.Fatal("invalid undeclared definition still published the declared subset")
	}
}

// TestADR_0326_LazyGrantRefreshRetryReusesPublishedSnapshot pins the
// transactional-retry fix: once a grant refresh has published a catalogue for
// a given granted transaction, a retry against the SAME transaction (as a
// caller does after restoring the claim following a downstream
// session-engine rebuild failure) must reuse that exact snapshot rather than
// re-running live authenticated discovery, which could return different
// metadata on a second call.
func TestADR_0326_LazyGrantRefreshRetryReusesPublishedSnapshot(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	harness.runtime.catalogue.routes[0].broker = true
	attachment, _ := attach(t, harness.runtime, "lazy-refresh-retry")
	protected := toolByName(t, attachment, "mcp__github__create").(*protectedSessionTool)
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"github": {Backend: "github", Tools: []ToolDefinition{
			{Backend: "github", Name: "mcp__github__create", Description: "live", Schema: json.RawMessage(`{"type":"object"}`)},
		}},
	}}
	harness.runtime.process = &Process{Runtime: harness.runtime, construction: toolHiveConstruction{protectedBackends: []string{"github"}}, protectedTarget: protected.route.oauth, queryAuthenticated: queries.query}
	authorization, state := requestProtected(t, attachment, session.NewToolCall("lazy-refresh-retry", protected.Spec().Name, json.RawMessage(`{}`)))
	if got := callback(t, harness.runtime, "code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	first, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization)
	if err != nil {
		t.Fatalf("first RefreshGrantedAuthorizationCatalogue: %v", err)
	}
	queries.mu.Lock()
	callsAfterFirst := queries.calls
	// Change what discovery would return, proving a retry cannot observe it:
	// a retry that re-queried would publish "changed" instead of "live".
	queries.responses["github"] = AuthenticatedCapabilities{Backend: "github", Tools: []ToolDefinition{
		{Backend: "github", Name: "mcp__github__create", Description: "changed", Schema: json.RawMessage(`{"type":"object"}`)},
	}}
	queries.mu.Unlock()
	second, err := attachment.RefreshGrantedAuthorizationCatalogue(t.Context(), authorization)
	if err != nil {
		t.Fatalf("retry RefreshGrantedAuthorizationCatalogue: %v", err)
	}
	queries.mu.Lock()
	callsAfterSecond := queries.calls
	queries.mu.Unlock()
	if callsAfterSecond != callsAfterFirst {
		t.Fatalf("retry re-queried authenticated discovery: calls %d -> %d", callsAfterFirst, callsAfterSecond)
	}
	if !reflect.DeepEqual(toolNames(first), toolNames(second)) {
		t.Fatalf("retry tools = %v, want %v", toolNames(second), toolNames(first))
	}
	if got := toolByName(t, attachment, "mcp__github__create").Spec().Description; got != "live" {
		t.Fatalf("retry published re-discovered metadata: description = %q, want %q", got, "live")
	}
}

func TestSingletonBrokerRemediation_Scenario5_CallbackCorrelationReplayAndNonDisclosure(t *testing.T) {
	var exchanges atomic.Int32
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		exchanges.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","token_type":"Bearer"}`))
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	defer harness.runtime.Close()
	first, _ := attach(t, harness.runtime, "first-enrollment")
	second, _ := attach(t, harness.runtime, "second-enrollment")
	firstAuth, firstState := requestProtected(t, first, session.NewToolCall("first", "mcp__github__create", json.RawMessage(`{"title":"one"}`)))
	_, secondState := requestProtected(t, second, session.NewToolCall("second", "mcp__github__create", json.RawMessage(`{"title":"two"}`)))
	if len(firstState) < 43 || firstState == secondState {
		t.Fatalf("callback state is not a unique 256-bit opaque value: %q / %q", firstState, secondState)
	}

	genericReject := func(raw string) {
		t.Helper()
		response := httptest.NewRecorder()
		harness.runtime.CallbackHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, raw, nil))
		body := response.Body.String()
		if response.Code != http.StatusBadRequest || body != "invalid OAuth callback\n" {
			t.Fatalf("callback %q = (%d, %q), want one generic rejection", raw, response.Code, body)
		}
		for _, secret := range []string{firstState, secondState, "first-enrollment", "second-enrollment", "authorization-code"} {
			if strings.Contains(body, secret) {
				t.Fatalf("callback rejection disclosed %q in %q", secret, body)
			}
		}
	}
	genericReject("/callback")
	genericReject("/callback?code=x&state=not-a-state")
	genericReject("/callback?code=x&state=" + url.QueryEscape(firstState) + "&session=second-enrollment")
	if exchanges.Load() != 0 {
		t.Fatalf("hostile callbacks reached token exchange %d times", exchanges.Load())
	}
	if status, err := first.AuthorizationStatus(t.Context(), firstAuth); err != nil || status != session.AuthorizationPending {
		t.Fatalf("hostile callback changed first enrollment: %q, %v", status, err)
	}

	if got := callback(t, harness.runtime, "authorization-code", firstState); got.Code != http.StatusOK {
		t.Fatalf("valid final callback status = %d", got.Code)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("valid callback exchanges = %d, want 1", exchanges.Load())
	}
	genericReject("/callback?code=authorization-code&state=" + url.QueryEscape(firstState))
	if exchanges.Load() != 1 {
		t.Fatalf("replayed callback exchanged code %d times", exchanges.Load())
	}

	clock := time.Now().Add(2 * time.Hour)
	harness.runtime.oauth.now = func() time.Time { return clock }
	genericReject("/callback?code=authorization-code&state=" + url.QueryEscape(secondState))
	if exchanges.Load() != 1 {
		t.Fatalf("expired callback exchanged code %d times", exchanges.Load())
	}
}

func TestADR_0312_SingletonBrokerConfidentialClientCustody(t *testing.T) {
	assertToolHiveProtectedClientIsConfidential(t)

	var exchanges, refreshes int
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if !validConfidentialTokenRequest(request, "client-id", "client-secret") {
			t.Errorf("token request violates confidential-client custody")
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Form.Get("grant_type") {
		case "authorization_code":
			exchanges++
			if request.Form.Get("code_verifier") == "" {
				t.Error("exchange omitted PKCE verifier")
			}
			_, _ = w.Write([]byte(`{"access_token":"stale-access","token_type":"Bearer","refresh_token":"rotating-refresh","expires_in":-1}`))
		case "refresh_token":
			refreshes++
			if request.Form.Get("refresh_token") != "rotating-refresh" {
				t.Errorf("refresh token = %q", request.Form.Get("refresh_token"))
			}
			// Omission means preservation, not deletion, of refresh custody.
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`))
		default:
			t.Errorf("grant_type = %q", request.Form.Get("grant_type"))
		}
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "protected-session")
	call := session.NewToolCall("call-protected", "mcp__github__create", json.RawMessage(`{"title":"one"}`))
	authorization, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusBadRequest {
		t.Fatalf("duplicate callback status = %d", got)
	}
	if status, err := attachment.AuthorizationStatus(t.Context(), authorization); err != nil || status != session.AuthorizationGranted {
		t.Fatalf("status = (%q, %v)", status, err)
	}

	requester := toolByName(t, attachment, call.Name).(tool.AuthorizationRequester)
	if got, required, err := requester.RequestAuthorization(t.Context(), call); err != nil || required || got != (session.ExternalAuthorization{}) {
		t.Fatalf("post-grant request = (%+v, %v, %v)", got, required, err)
	}
	result, err := toolByName(t, attachment, call.Name).Execute(t.Context(), call, tool.Environment{})
	if err != nil || result.Content != "created" {
		t.Fatalf("protected Execute = (%+v, %v)", result, err)
	}
	if exchanges != 1 || refreshes != 1 {
		t.Fatalf("exchange/refresh calls = %d/%d", exchanges, refreshes)
	}
	harness.mu.Lock()
	gotToken := harness.tokens[0]
	harness.mu.Unlock()
	if gotToken.AccessToken != "fresh-access" || gotToken.RefreshToken != "rotating-refresh" || gotToken.Expiry.IsZero() {
		t.Fatalf("refreshed token semantics = %+v", gotToken)
	}
	if _, err := toolByName(t, attachment, call.Name).Execute(t.Context(), call, tool.Environment{}); err == nil || !strings.Contains(err.Error(), "replay refused") {
		t.Fatalf("ambiguous replay error = %v", err)
	}
	assertToolHiveRedisPreservesOnlyInnerPendingState(t)
}

func validConfidentialTokenRequest(request *http.Request, clientID, secret string) bool {
	user, password, ok := request.BasicAuth()
	return ok && user == clientID && password == secret && request.Form.Get("client_secret") == ""
}

func TestConfidentialTokenCustodyOracleRejectsPlantedViolations(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://issuer.example/token", strings.NewReader("client_secret=planted"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth("client-id", "client-secret")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if validConfidentialTokenRequest(request, "client-id", "client-secret") {
		t.Fatal("custody oracle accepted a planted form secret")
	}
	request = httptest.NewRequest(http.MethodPost, "https://issuer.example/token", nil)
	request.SetBasicAuth("client-id", "wrong-secret")
	if validConfidentialTokenRequest(request, "client-id", "client-secret") {
		t.Fatal("custody oracle accepted a planted Basic password")
	}
}

func assertToolHiveRedisPreservesOnlyInnerPendingState(t *testing.T) {
	t.Helper()
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})

	var tokenRequests atomic.Int32
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"inner-access","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(tokenServer.Certificate())
	t.Setenv("testdata/client-secret", "upstream-secret")
	config := ToolHiveConfig{
		CallbackURL: "https://broker.example/oauth/callback", AuthRedisClient: redisClient,
		Profiles: []ToolHiveProfile{{Name: "github", URL: "https://mcp.example/mcp", Auth: "oauth", OAuth: &ToolHiveOAuth{
			AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: tokenServer.URL + "/token",
			ClientID: "redis-custody", ClientSecretFile: "testdata/client-secret", Scopes: []string{"read"},
		}, Static: []StaticTool{{Name: "create", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}},
	}
	options := []Option{WithOAuthLoopbackForTest(t, roots), WithOAuthSecretFileReader(func(context.Context, string) (string, error) { return "upstream-secret", nil })}
	first, err := NewToolHiveProcess(t.Context(), config, options...)
	if err != nil {
		t.Fatalf("construct Redis-backed ToolHive process: %v", err)
	}
	attachment, _, err := first.Runtime.AttachSession(t.Context(), "redis-custody")
	if err != nil {
		t.Fatal(err)
	}
	if err := attachment.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	requester := attachment.Tools()[0].(tool.AuthorizationRequester)
	authorization, required, err := requester.RequestAuthorization(t.Context(), session.NewToolCall("redis-call", "mcp__github__create", json.RawMessage(`{}`)))
	if err != nil || !required {
		t.Fatalf("outer authorization = (%+v, %v, %v)", authorization, required, err)
	}
	presentation, err := attachment.PresentAuthorization(t.Context(), authorization)
	if err != nil {
		t.Fatal(err)
	}
	outerURL, err := url.Parse(presentation)
	if err != nil {
		t.Fatal(err)
	}
	outerState := outerURL.Query().Get("state")
	mux := http.NewServeMux()
	if err := first.Handlers.Mount(mux, "/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	mux.ServeHTTP(start, httptest.NewRequest(http.MethodGet, outerURL.RequestURI(), nil))
	if start.Code != http.StatusFound {
		t.Fatalf("embedded authorize status = %d body=%q", start.Code, start.Body.String())
	}
	upstream, err := url.Parse(start.Header().Get("Location"))
	if err != nil || upstream.Query().Get("state") == "" {
		t.Fatalf("upstream redirect = %q, %v", start.Header().Get("Location"), err)
	}
	innerState := upstream.Query().Get("state")
	pendingKey := toolHiveAuthStoragePrefix + "pending:" + strings.ToUpper(innerState)
	if !redisServer.Exists(pendingKey) {
		t.Fatalf("ToolHive pending state was not stored in Redis: %q", pendingKey)
	}
	if dump := redisServer.Dump(); strings.Contains(dump, "upstream-secret") || strings.Contains(dump, outerState) {
		t.Fatalf("Redis crossed the custody boundary with an outer credential or callback state: %s", dump)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	config.AuthRedisClient = redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	replacement, err := NewToolHiveProcess(t.Context(), config, options...)
	if err != nil {
		t.Fatalf("replace Redis-backed ToolHive process: %v", err)
	}
	defer replacement.Close()
	replacementMux := http.NewServeMux()
	if err := replacement.Handlers.Mount(replacementMux, "/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	outer := httptest.NewRecorder()
	replacementMux.ServeHTTP(outer, httptest.NewRequest(http.MethodGet, "/oauth/callback?"+url.Values{"code": {"old-code"}, "state": {outerState}}.Encode(), nil))
	if outer.Code != http.StatusBadRequest || outer.Body.String() != "invalid OAuth callback\n" {
		t.Fatalf("restarted outer callback = (%d, %q)", outer.Code, outer.Body.String())
	}
	if tokenRequests.Load() != 0 {
		t.Fatal("lost outer state reached an upstream token endpoint")
	}

	inner := httptest.NewRecorder()
	replacementMux.ServeHTTP(inner, httptest.NewRequest(http.MethodGet, "/v1/mcp/broker/oauth/callback?"+url.Values{"code": {"invalid-code"}, "state": {innerState}}.Encode(), nil))
	if inner.Code == http.StatusBadRequest && strings.Contains(inner.Body.String(), "not found or expired") {
		t.Fatalf("Redis-backed inner state was lost across restart: %q", inner.Body.String())
	}
	if redisServer.Exists(pendingKey) {
		t.Fatal("ToolHive replay state remained after restarted callback consumption")
	}
}

func TestBrokerCredentialRefreshRetainsCredentialAfterTransientFailure(t *testing.T) {
	var requests int
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"recovered","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	attached, _ := attach(t, harness.runtime, "broker-refresh")
	config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")
	grant := &oauthGrant{config: config, token: &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}}
	attached.logical.mu.Lock()
	attached.logical.brokerCredential = grant
	attached.logical.mu.Unlock()
	source := &brokerTokenSource{runtime: harness.runtime, logical: attached.logical, ctx: context.Background()}
	if _, err := source.Token(); err == nil {
		t.Fatal("transient refresh unexpectedly succeeded")
	}
	attached.logical.mu.RLock()
	retained := attached.logical.brokerCredential
	access, refresh := retained.token.AccessToken, retained.token.RefreshToken
	attached.logical.mu.RUnlock()
	if retained != grant || access != "stale" || refresh != "refresh" {
		t.Fatalf("transient refresh discarded broker credential: %#v", retained)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "recovered" {
		t.Fatalf("retry refresh = (%+v, %v)", token, err)
	}
}

func TestRefreshFailureOnlyRevokesTerminalGrant(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReauth bool
	}{
		{name: "invalid grant", status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, wantReauth: true},
		{name: "transient server error", status: http.StatusInternalServerError, body: `{"error":"server_error"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if requests == 1 {
					_, _ = w.Write([]byte(`{"access_token":"stale-access","token_type":"Bearer","refresh_token":"stale-refresh","expires_in":-1}`))
					return
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer tokenServer.Close()

			harness := newProtectedHarness(t, tokenServer)
			attachment, _ := attach(t, harness.runtime, "refresh-failure-session")
			call := session.NewToolCall("initial-call", "mcp__github__create", json.RawMessage(`{}`))
			_, state := requestProtected(t, attachment, call)
			if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusOK {
				t.Fatalf("callback status = %d", got)
			}

			attachment.logical.mu.Lock()
			grant := attachment.logical.grants["github"]
			attachment.logical.mu.Unlock()
			if grant == nil {
				t.Fatal("callback did not install grant")
			}
			if _, err := toolByName(t, attachment, call.Name).Execute(t.Context(), call, tool.Environment{}); err == nil {
				t.Fatal("refresh failure did not fail protected call")
			}

			next := session.NewToolCall("retry-call", call.Name, json.RawMessage(`{}`))
			requester := toolByName(t, attachment, call.Name).(tool.AuthorizationRequester)
			_, required, err := requester.RequestAuthorization(t.Context(), next)
			if err != nil {
				t.Fatalf("RequestAuthorization after refresh failure: %v", err)
			}
			if required != test.wantReauth {
				t.Fatalf("authorization required = %v, want %v", required, test.wantReauth)
			}
			if test.wantReauth && (grant.token.AccessToken != "" || grant.token.RefreshToken != "") {
				t.Fatal("revoked grant retained credential material")
			}
		})
	}
}

func TestAuthorizationSessionIsolationCancelDeleteCloseAndRestartPosture(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"unused","token_type":"Bearer"}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	first, _ := attach(t, harness.runtime, "first-session")
	second, _ := attach(t, harness.runtime, "second-session")
	call := session.NewToolCall("cancel-call", "mcp__github__create", json.RawMessage(`{}`))
	authorization, state := requestProtected(t, first, call)
	if _, err := second.AuthorizationStatus(t.Context(), authorization); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("cross-session status = %v", err)
	}
	if got, err := first.CancelAuthorization(t.Context(), authorization); err != nil || got != contract.CancelCancelled {
		t.Fatalf("cancel = (%q, %v)", got, err)
	}
	if got, err := first.CancelAuthorization(t.Context(), authorization); err != nil || got != contract.CancelAlreadyCancelled {
		t.Fatalf("second cancel = (%q, %v)", got, err)
	}
	if status, err := first.AuthorizationStatus(t.Context(), authorization); err != nil || status != session.AuthorizationCancelled {
		t.Fatalf("cancelled status = (%q, %v)", status, err)
	}
	if got := callback(t, harness.runtime, "late", state).Code; got != http.StatusBadRequest {
		t.Fatalf("late callback status = %d", got)
	}

	if outcome, err := harness.runtime.DeleteSession(t.Context(), "first-session"); err != nil || outcome != contract.DeleteDeleted {
		t.Fatalf("delete = (%q, %v)", outcome, err)
	}
	if _, err := first.AuthorizationStatus(t.Context(), authorization); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("deleted attachment = %v", err)
	}

	restarted := newProtectedHarness(t, tokenServer)
	restored, _ := attach(t, restarted.runtime, "first-session")
	if _, err := restored.AuthorizationStatus(t.Context(), authorization); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("restart status = %v", err)
	}
	if err := harness.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := harness.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := harness.runtime.AttachSession(t.Context(), "after-close"); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("attach after close = %v", err)
	}
}

func TestCallbackAfterTransactionExpiryIsRejectedAndMarkedExpired(t *testing.T) {
	var exchanges int
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		exchanges++
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "expired-session")
	call := session.NewToolCall("expired-call", "mcp__github__create", json.RawMessage(`{}`))
	authorization, state := requestProtected(t, attachment, call)
	harness.runtime.oauth.now = func() time.Time { return authorization.ExpiresAt }

	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusBadRequest {
		t.Fatalf("expired callback status = %d", got)
	}
	if exchanges != 0 {
		t.Fatalf("expired callback exchanged token %d times", exchanges)
	}
	if status, err := attachment.AuthorizationStatus(t.Context(), authorization); err != nil || status != session.AuthorizationExpired {
		t.Fatalf("expired status = (%q, %v)", status, err)
	}
}

func TestTokenExchangeCarriesResourceIndicator(t *testing.T) {
	const wantResource = "https://mcp.example/mcp"
	var resourceValues []string
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		resourceValues = append(resourceValues, request.Form.Get("resource"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "resource-session")
	call := session.NewToolCall("resource-call", "mcp__github__create", json.RawMessage(`{}`))
	_, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if len(resourceValues) != 1 || resourceValues[0] != wantResource {
		t.Fatalf("token exchange resource values = %v, want [%q]", resourceValues, wantResource)
	}
}

func TestConfidentialTokenExchangeUsesBasicAuthenticationOnce(t *testing.T) {
	var requests int
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		user, password, ok := request.BasicAuth()
		if !ok || user != "client-id" || password != "client-secret" {
			t.Errorf("BasicAuth = (%q, %q, %v)", user, password, ok)
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if secret := request.Form.Get("client_secret"); secret != "" {
			t.Errorf("client_secret form value = %q", secret)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "basic-session")
	call := session.NewToolCall("basic-call", "mcp__github__create", json.RawMessage(`{}`))
	_, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if requests != 1 {
		t.Fatalf("token exchange requests = %d, want 1", requests)
	}
}

func TestOAuthErrorCallbackConsumesStateAndMapsStatus(t *testing.T) {
	for _, test := range []struct {
		name, oauthError string
		want             session.AuthorizationStatus
	}{
		{name: "access denied", oauthError: "access_denied", want: session.AuthorizationDenied},
		{name: "other error", oauthError: "server_error", want: session.AuthorizationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("error callback must not exchange a token")
			}))
			defer tokenServer.Close()

			harness := newProtectedHarness(t, tokenServer)
			attachment, _ := attach(t, harness.runtime, session.SessionID("error-session-"+test.oauthError))
			call := session.NewToolCall("error-call", "mcp__github__create", json.RawMessage(`{}`))
			authorization, state := requestProtected(t, attachment, call)
			request := httptest.NewRequest(http.MethodGet, "/callback?"+url.Values{"error": {test.oauthError}, "state": {state}}.Encode(), nil)
			first := httptest.NewRecorder()
			harness.runtime.CallbackHandler().ServeHTTP(first, request)
			if first.Code != http.StatusBadRequest {
				t.Fatalf("error callback status = %d", first.Code)
			}
			if status, err := attachment.AuthorizationStatus(t.Context(), authorization); err != nil || status != test.want {
				t.Fatalf("status = (%q, %v), want %q", status, err, test.want)
			}
			second := httptest.NewRecorder()
			harness.runtime.CallbackHandler().ServeHTTP(second, request)
			if second.Code != http.StatusBadRequest {
				t.Fatalf("replayed error callback status = %d", second.Code)
			}
		})
	}
}

func TestTokenExchangeRejectsMissingOrNonBearerTokenType(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing token type", body: `{"access_token":"access","expires_in":3600}`},
		{name: "non bearer token type", body: `{"access_token":"access","token_type":"MAC","expires_in":3600}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer tokenServer.Close()

			harness := newProtectedHarness(t, tokenServer)
			attachment, _ := attach(t, harness.runtime, "token-type-session")
			call := session.NewToolCall("token-type-call", "mcp__github__create", json.RawMessage(`{}`))
			authorization, state := requestProtected(t, attachment, call)
			if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusBadRequest {
				t.Fatalf("callback status = %d", got)
			}
			if status, err := attachment.AuthorizationStatus(t.Context(), authorization); err != nil || status != session.AuthorizationFailed {
				t.Fatalf("failed status = (%q, %v)", status, err)
			}
		})
	}
}

func TestTokenExtraMetadataSurvivesScopedTokenSource(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","token_type":"Bearer","expires_in":3600,"id_token":"identity-token"}`))
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	attachment, _ := attach(t, harness.runtime, "extra-session")
	call := session.NewToolCall("extra-call", "mcp__github__create", json.RawMessage(`{}`))
	_, state := requestProtected(t, attachment, call)
	if got := callback(t, harness.runtime, "authorization-code", state).Code; got != http.StatusOK {
		t.Fatalf("callback status = %d", got)
	}
	if _, err := toolByName(t, attachment, call.Name).Execute(t.Context(), call, tool.Environment{}); err != nil {
		t.Fatal(err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.tokens) != 1 || harness.tokens[0].Extra("id_token") != "identity-token" {
		t.Fatalf("Token Extra(id_token) = %v", harness.tokens)
	}
}

// TestSessionHandleCloseWaitsForRequestAuthorization pins P2-8: Close must not
// return while a RequestAuthorization call is still mid-flight (blocked
// resolving its secret), or a caller that tears down owned resources right
// after Close returns can race a transaction this call is about to create.
func TestSessionHandleCloseWaitsForRequestAuthorization(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	catalogue, err := Compile(protectedConfig("https://token.example/token"),
		[]ToolDefinition{{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue,
		func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		},
		WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		}),
		WithOAuthSecretFileReader(func(ctx context.Context, _ string) (string, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "client-secret", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	attachment, _ := attach(t, runtime, "close-race-session")
	call := session.NewToolCall("call-1", "mcp__github__create", json.RawMessage(`{}`))
	requester := toolByName(t, attachment, call.Name).(tool.AuthorizationRequester)

	requestDone := make(chan struct{})
	go func() {
		_, _, _ = requester.RequestAuthorization(context.Background(), call)
		close(requestDone)
	}()
	<-entered // RequestAuthorization is now blocked inside secret resolution.

	closeDone := make(chan struct{})
	go func() {
		_, _ = attachment.Close(context.Background())
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("Attachment.Close returned while RequestAuthorization was still resolving its secret")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestAuthorization never returned")
	}
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Attachment.Close never returned after RequestAuthorization finished")
	}
}

// TestRuntimeCloseDrainsActiveOperationsBeforeReturning pins P2-10: Runtime.Close
// must not return while an attachment operation is still in flight, so a caller
// that tears down owned dependencies (vMCP/authserver) immediately after Close
// cannot pull them out from under that operation.
// TestRuntimeCloseAndDrainWaitsForActiveOperations pins P2-10 at the layer it
// actually applies: a bare Runtime.Close is fire-and-forget (see
// TestRuntimeCloseIsBoundedAndOperationReleaseOwnsCleanup in runtime_test.go),
// but Process.Close/rollback use closeAndDrain specifically because they tear
// down additional owned resources (vMCP/authserver) right after — that must
// not race a still-running operation.
func TestRuntimeCloseAndDrainWaitsForActiveOperations(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	catalogue, err := Compile(protectedConfig("https://token.example/token"),
		[]ToolDefinition{{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue,
		func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		},
		WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		}),
		WithOAuthSecretFileReader(func(_ context.Context, _ string) (string, error) {
			close(entered)
			<-release
			return "client-secret", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	attachment, _ := attach(t, runtime, "runtime-close-race")
	call := session.NewToolCall("call-1", "mcp__github__create", json.RawMessage(`{}`))
	requester := toolByName(t, attachment, call.Name).(tool.AuthorizationRequester)

	requestDone := make(chan struct{})
	go func() {
		_, _, _ = requester.RequestAuthorization(context.Background(), call)
		close(requestDone)
	}()
	<-entered

	closeDone := make(chan struct{})
	go func() {
		_ = runtime.closeAndDrain(closeDrainTimeout)
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("closeAndDrain returned while an attachment operation was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestAuthorization never returned")
	}
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("closeAndDrain never returned after the in-flight operation finished")
	}
}

// TestRuntimeCloseAndDrainIsBoundedWhenOperationHangs pins that a hung
// operation cannot wedge shutdown forever: closeAndDrain gives up waiting
// after closeDrainTimeout.
func TestRuntimeCloseAndDrainIsBoundedWhenOperationHangs(t *testing.T) {
	orig := closeDrainTimeout
	closeDrainTimeout = 50 * time.Millisecond
	defer func() { closeDrainTimeout = orig }()

	entered := make(chan struct{})
	hang := make(chan struct{}) // never closed: simulates a hung operation
	catalogue, err := Compile(protectedConfig("https://token.example/token"),
		[]ToolDefinition{{Backend: "github", Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(catalogue,
		func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		},
		WithAuthorizedCaller(func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error) {
			return session.ToolResult{}, nil
		}),
		WithOAuthSecretFileReader(func(_ context.Context, _ string) (string, error) {
			close(entered)
			<-hang // ignores ctx cancellation on purpose: a genuinely wedged op
			return "client-secret", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	attachment, _ := attach(t, runtime, "runtime-close-bound")
	call := session.NewToolCall("call-1", "mcp__github__create", json.RawMessage(`{}`))
	requester := toolByName(t, attachment, call.Name).(tool.AuthorizationRequester)
	go func() { _, _, _ = requester.RequestAuthorization(context.Background(), call) }()
	<-entered

	closeDone := make(chan struct{})
	go func() {
		_ = runtime.closeAndDrain(closeDrainTimeout)
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("closeAndDrain did not return within its bounded drain timeout")
	}
}

// TestTokenRefreshDoesNotHoldSessionLock pins P2-9: a token-refresh network
// round trip must not run under logical.mu, or a slow/hung upstream token
// endpoint blocks unrelated session deletion for the duration.
func TestTokenRefreshDoesNotHoldSessionLock(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	harness := newProtectedHarness(t, tokenServer)
	attached, _ := attach(t, harness.runtime, "refresh-lock-race")
	config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")
	grant := &oauthGrant{config: config, token: &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}}
	attached.logical.mu.Lock()
	attached.logical.grants["github"] = grant
	attached.logical.mu.Unlock()

	source := &scopedTokenSource{runtime: harness.runtime, logical: attached.logical, backend: "github", ctx: context.Background()}
	tokenDone := make(chan struct{})
	go func() {
		_, _ = source.Token()
		close(tokenDone)
	}()
	<-entered // the refresh is now blocked in the token endpoint

	deleteDone := make(chan struct{})
	go func() {
		_, _ = harness.runtime.DeleteSession(context.Background(), "refresh-lock-race")
		close(deleteDone)
	}()
	select {
	case <-deleteDone:
	case <-time.After(2 * time.Second):
		close(release)
		<-tokenDone
		t.Fatal("DeleteSession blocked behind the in-flight token refresh's lock")
	}
	close(release)
	<-tokenDone
}

// TestTokenRefreshHonoursOperationCancellation pins that a refresh aborts when
// the calling operation's own context is cancelled, instead of ignoring it via
// context.Background() and running until runtime.oauth.timeout regardless.
func TestTokenRefreshHonoursOperationCancellation(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-block:
		case <-request.Context().Done():
		}
	}))
	defer tokenServer.Close()
	defer close(block) // unblocks the handler goroutine, so the Close above (registered first, runs last) doesn't hang
	harness := newProtectedHarness(t, tokenServer)
	attached, _ := attach(t, harness.runtime, "refresh-cancel")
	config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")
	grant := &oauthGrant{config: config, token: &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}}
	attached.logical.mu.Lock()
	attached.logical.grants["github"] = grant
	attached.logical.mu.Unlock()

	opCtx, cancelOp := context.WithCancel(context.Background())
	source := &scopedTokenSource{runtime: harness.runtime, logical: attached.logical, backend: "github", ctx: opCtx}
	tokenDone := make(chan error, 1)
	go func() {
		_, err := source.Token()
		tokenDone <- err
	}()
	<-entered
	cancelOp()

	select {
	case err := <-tokenDone:
		if err == nil {
			t.Fatal("Token succeeded despite the operation context being cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Token did not honour operation cancellation (waited for runtime.oauth.timeout instead)")
	}
}

func TestRefreshCarriesResourceIndicator(t *testing.T) {
	const wantResource = "https://mcp.example/mcp"
	for _, brokerWide := range []bool{false, true} {
		name := "scoped"
		if brokerWide {
			name = "broker"
		}
		t.Run(name, func(t *testing.T) {
			var gotResource string
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if err := request.ParseForm(); err != nil {
					t.Error(err)
				}
				gotResource = request.Form.Get("resource")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
			}))
			defer tokenServer.Close()

			harness := newProtectedHarness(t, tokenServer)
			attached, _ := attach(t, harness.runtime, session.SessionID("resource-refresh-"+name))
			config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")
			grant := &oauthGrant{
				config: config, resource: wantResource,
				token: &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)},
			}
			attached.logical.mu.Lock()
			if brokerWide {
				attached.logical.brokerCredential = grant
			} else {
				attached.logical.grants["github"] = grant
			}
			attached.logical.mu.Unlock()

			var source oauth2.TokenSource = &scopedTokenSource{runtime: harness.runtime, logical: attached.logical, backend: "github", ctx: t.Context()}
			if brokerWide {
				source = &brokerTokenSource{runtime: harness.runtime, logical: attached.logical, ctx: t.Context()}
			}
			if _, err := source.Token(); err != nil {
				t.Fatalf("Token: %v", err)
			}
			if gotResource != wantResource {
				t.Fatalf("refresh resource = %q, want %q", gotResource, wantResource)
			}
		})
	}
}

func TestConcurrentRefreshSerializesEachGrant(t *testing.T) {
	for _, brokerWide := range []bool{false, true} {
		name := "route"
		if brokerWide {
			name = "broker"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			firstEntered := make(chan struct{})
			secondEntered := make(chan struct{})
			release := make(chan struct{})
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				calls++
				call := calls
				mu.Unlock()
				switch call {
				case 1:
					close(firstEntered)
				case 2:
					close(secondEntered)
				}
				<-release
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
			}))
			defer tokenServer.Close()

			harness := newProtectedHarness(t, tokenServer)
			attached, _ := attach(t, harness.runtime, session.SessionID("concurrent-refresh-"+name))
			config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")
			grant := &oauthGrant{config: config, token: &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}}
			attached.logical.mu.Lock()
			if brokerWide {
				attached.logical.brokerCredential = grant
			} else {
				attached.logical.grants["github"] = grant
			}
			attached.logical.mu.Unlock()

			var source oauth2.TokenSource
			if brokerWide {
				source = &brokerTokenSource{runtime: harness.runtime, logical: attached.logical, ctx: context.Background()}
			} else {
				source = &scopedTokenSource{runtime: harness.runtime, logical: attached.logical, backend: "github", ctx: context.Background()}
			}
			results := make(chan error, 2)
			start := make(chan struct{})
			for range 2 {
				go func() {
					<-start
					token, err := source.Token()
					if err == nil && token.AccessToken != "fresh" {
						err = fmt.Errorf("access token = %q", token.AccessToken)
					}
					results <- err
				}()
			}
			close(start)
			<-firstEntered
			select {
			case <-secondEntered:
				close(release)
				t.Fatal("same grant was refreshed concurrently")
			case <-time.After(25 * time.Millisecond):
			}
			close(release)
			for range 2 {
				if err := <-results; err != nil {
					t.Fatalf("Token: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("refresh requests = %d, want 1", calls)
			}
		})
	}
}
