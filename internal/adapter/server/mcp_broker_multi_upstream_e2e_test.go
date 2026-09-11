package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type multiUpstreamTokenStore struct {
	*storage.MemoryStorage
	mu       sync.Mutex
	sessions map[string]string
}

func newMultiUpstreamTokenStore() *multiUpstreamTokenStore {
	return &multiUpstreamTokenStore{MemoryStorage: storage.NewMemoryStorage(), sessions: make(map[string]string)}
}

func (s *multiUpstreamTokenStore) StoreUpstreamTokens(ctx context.Context, sessionID, provider string, tokens *storage.UpstreamTokens) error {
	s.mu.Lock()
	s.sessions[provider] = sessionID
	s.mu.Unlock()
	return s.MemoryStorage.StoreUpstreamTokens(ctx, sessionID, provider, tokens)
}

func (s *multiUpstreamTokenStore) expire(t *testing.T, provider string) {
	t.Helper()
	s.mu.Lock()
	sessionID := s.sessions[provider]
	s.mu.Unlock()
	if sessionID == "" {
		t.Fatalf("no ToolHive token session recorded for %q", provider)
	}
	tokens, err := s.GetUpstreamTokens(t.Context(), sessionID, provider)
	if err != nil {
		t.Fatalf("get %s token: %v", provider, err)
	}
	tokens.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.StoreUpstreamTokens(t.Context(), sessionID, provider, tokens); err != nil {
		t.Fatalf("expire %s token: %v", provider, err)
	}
}

type multiUpstreamOIDC struct {
	name, clientID string
	server         *httptest.Server
	key            *rsa.PrivateKey

	mu               sync.Mutex
	nonce            string
	deny             bool
	initialTokens    int
	refreshes        int
	accessToken      string
	refreshedToken   string
	authorizeEntered chan struct{}
	authorizeRelease <-chan struct{}
	enteredOnce      sync.Once
}

func newMultiUpstreamOIDC(t *testing.T, name string) *multiUpstreamOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate %s key: %v", name, err)
	}
	fake := &multiUpstreamOIDC{name: name, clientID: name + "-client", key: key, accessToken: name + "-token", refreshedToken: name + "-refreshed"}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *multiUpstreamOIDC) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeTestJSON(w, map[string]any{
			"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/authorize":
		f.mu.Lock()
		f.nonce = r.URL.Query().Get("nonce")
		deny := f.deny
		entered, release := f.authorizeEntered, f.authorizeRelease
		f.mu.Unlock()
		if entered != nil {
			f.enteredOnce.Do(func() { close(entered) })
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		query := url.Values{"state": {r.URL.Query().Get("state")}}
		if deny {
			query.Set("error", "access_denied")
		} else {
			query.Set("code", f.name+"-code")
		}
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?"+query.Encode(), http.StatusFound)
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.mu.Lock()
			f.refreshes++
			f.mu.Unlock()
			writeTestJSON(w, map[string]any{"access_token": f.refreshedToken, "refresh_token": f.name + "-refresh", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		f.mu.Lock()
		f.initialTokens++
		nonce := f.nonce
		f.mu.Unlock()
		now := time.Now()
		idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": f.server.URL, "sub": "same-test-user", "aud": f.clientID, "nonce": nonce,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		})
		idToken.Header["kid"] = f.name + "-key"
		signed, err := idToken.SignedString(f.key)
		if err != nil {
			http.Error(w, "sign token", http.StatusInternalServerError)
			return
		}
		writeTestJSON(w, map[string]any{"access_token": f.accessToken, "refresh_token": f.name + "-refresh", "id_token": signed, "token_type": "Bearer", "expires_in": 3600})
	case "/jwks":
		writeTestJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "kid": f.name + "-key", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	default:
		http.NotFound(w, r)
	}
}

func (f *multiUpstreamOIDC) counts() (initial, refresh int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.initialTokens, f.refreshes
}

type multiUpstreamMCP struct {
	name   string
	server *httptest.Server

	mu        sync.Mutex
	reject    bool
	headers   []string
	toolCalls int
}

func newMultiUpstreamMCP(t *testing.T, name string) *multiUpstreamMCP {
	t.Helper()
	fake := &multiUpstreamMCP{name: name}
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "whoami", Description: "report backend"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
		fake.mu.Lock()
		fake.toolCalls++
		fake.mu.Unlock()
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: name}}}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.headers = append(fake.headers, r.Header.Get("Authorization"))
		reject := fake.reject
		fake.mu.Unlock()
		if reject {
			http.Error(w, "deterministic authenticated discovery failure", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *multiUpstreamMCP) snapshot() (headers []string, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.headers...), f.toolCalls
}

type multiUpstreamFixture struct {
	t       *testing.T
	issuerA *multiUpstreamOIDC
	issuerB *multiUpstreamOIDC
	mcpA    *multiUpstreamMCP
	mcpB    *multiUpstreamMCP
	store   *multiUpstreamTokenStore
	process *mcpbroker.Process
	service *Service
	gateway *httptest.Server
	session *session.Session

	mu              sync.Mutex
	catalogueBuilds [][]tool.Tool
	failBuilds      int
}

func newMultiUpstreamFixture(t *testing.T, rejectB bool, contexts ...context.Context) *multiUpstreamFixture {
	t.Helper()
	f := &multiUpstreamFixture{t: t, issuerA: newMultiUpstreamOIDC(t, "backend-a"), issuerB: newMultiUpstreamOIDC(t, "backend-b"), mcpA: newMultiUpstreamMCP(t, "backend-a"), mcpB: newMultiUpstreamMCP(t, "backend-b"), store: newMultiUpstreamTokenStore()}
	f.mcpB.reject = rejectB
	mux := http.NewServeMux()
	f.gateway = httptest.NewUnstartedServer(mux)
	f.gateway.StartTLS()
	t.Cleanup(f.gateway.Close)
	roots := x509.NewCertPool()
	roots.AddCert(f.gateway.Certificate())
	profiles := []mcpbroker.ToolHiveProfile{
		{Name: "backend-a", URL: f.mcpA.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerA.server.URL, ClientID: f.issuerA.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
		{Name: "backend-b", URL: f.mcpB.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerB.server.URL, ClientID: f.issuerB.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
	}
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{CallbackURL: f.gateway.URL + "/callback", Profiles: profiles, AuthStorage: f.store},
		mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, f.gateway.Client()))
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	f.process = process
	t.Cleanup(func() { _ = process.Close() })
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount ToolHive handlers: %v", err)
	}

	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(),
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", NewID: func() session.SessionID { return "adr-0298-e2e" },
		MCPBroker: process.Runtime, WorkspaceEnrollment: true,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__backend-a__whoami", "mcp__backend-b__whoami"}}, Provenance: "adr-0298-test"}
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.catalogueBuilds = append(f.catalogueBuilds, append([]tool.Tool(nil), tools...))
			if f.failBuilds > 0 {
				f.failBuilds--
				return SessionEngineResult{}, errors.New("injected engine replacement failure")
			}
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	f.service = svc
	t.Cleanup(svc.Close)
	ctx := t.Context()
	if len(contexts) != 0 {
		ctx = contexts[0]
	}
	f.session, err = svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if loaded, loadErr := svc.cfg.Store.Load(t.Context(), f.session.ID); loadErr != nil || loaded == nil {
		t.Fatalf("load created session %q: %v", f.session.ID, loadErr)
	}
	return f
}

func (f *multiUpstreamFixture) start() WorkspaceEnrollmentProjection {
	f.t.Helper()
	started, err := f.service.ConnectWorkspaceServices(f.t.Context(), f.session.ID)
	if err != nil {
		f.t.Fatalf("ConnectWorkspaceServices start: %v", err)
	}
	if started.Status != brokercontract.WorkspaceEnrollmentPending || started.Ref.RequiredServices != 2 || started.URL == "" {
		f.t.Fatalf("started enrollment = %#v", started)
	}
	return started
}

func (f *multiUpstreamFixture) requestAuthorization(presentation string) (int, error) {
	ctx, cancel := context.WithTimeout(f.t.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, presentation, nil)
	if err != nil {
		return 0, err
	}
	client := f.gateway.Client()
	client.Timeout = 8 * time.Second
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func (f *multiUpstreamFixture) drive(presentation string) int {
	f.t.Helper()
	status, err := f.requestAuthorization(presentation)
	if err != nil {
		f.t.Fatalf("authorization chain: %v", err)
	}
	return status
}

func (f *multiUpstreamFixture) connect() (WorkspaceEnrollmentProjection, []tool.Tool) {
	f.t.Helper()
	connected, err := f.service.ConnectWorkspaceServices(f.t.Context(), f.session.ID)
	if err != nil {
		f.t.Fatalf("ConnectWorkspaceServices observe: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var tools []tool.Tool
	if len(f.catalogueBuilds) != 0 {
		tools = append([]tool.Tool(nil), f.catalogueBuilds[len(f.catalogueBuilds)-1]...)
	}
	return connected, tools
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func toolFromFixture(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Spec().Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q absent from %v", name, fixtureToolNames(tools))
	return nil
}

func fixtureToolNames(tools []tool.Tool) []string {
	names := make([]string, len(tools))
	for i, candidate := range tools {
		names[i] = candidate.Spec().Name
	}
	return names
}

func assertOnlyBearer(t *testing.T, headers []string, allowed ...string) {
	t.Helper()
	if len(headers) == 0 {
		t.Fatal("upstream received no authenticated requests")
	}
	for _, header := range headers {
		if !containsString(allowed, header) {
			t.Fatalf("upstream bearer = %q, want one of %v", header, allowed)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func completeMultiUpstream(t *testing.T) (*multiUpstreamFixture, WorkspaceEnrollmentProjection, []tool.Tool) {
	t.Helper()
	f := newMultiUpstreamFixture(t, false)
	started := f.start()
	if status := f.drive(started.URL); status != http.StatusOK {
		t.Fatalf("authorization callback status = %d", status)
	}
	connected, tools := f.connect()
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("connected enrollment = %#v", connected)
	}
	return f, started, tools
}

func TestADR_0298_ToolHiveTwoUpstreamBrokerBootsE2E(t *testing.T) {
	f := newMultiUpstreamFixture(t, false)
	if !f.process.WorkspaceEnrollmentRequired() {
		t.Fatal("two protected ToolHive profiles did not require enrollment")
	}
	started := f.start()
	if started.Ref.RequiredServices != 2 {
		t.Fatalf("required services = %d, want 2", started.Ref.RequiredServices)
	}
}

func TestADR_0298_ToolHiveCompletesTwoUpstreamEnrollmentE2E(t *testing.T) {
	f := newMultiUpstreamFixture(t, false)
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	f.issuerB.mu.Lock()
	f.issuerB.authorizeEntered = secondEntered
	f.issuerB.authorizeRelease = releaseSecond
	f.issuerB.mu.Unlock()
	started := f.start()
	if got := len(f.catalogueBuilds); got != 1 {
		t.Fatalf("pre-enrollment catalogue builds = %d, want only CreateSession", got)
	}
	type authorizationResult struct {
		status int
		err    error
	}
	result := make(chan authorizationResult, 1)
	go func() {
		status, err := f.requestAuthorization(started.URL)
		result <- authorizationResult{status: status, err: err}
	}()
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("ToolHive did not advance from upstream A to upstream B")
	}
	if a, _ := f.issuerA.counts(); a != 1 {
		t.Fatalf("upstream B started before upstream A completed: A exchanges = %d, want 1", a)
	}
	if b, _ := f.issuerB.counts(); b != 0 {
		t.Fatalf("upstream B exchanged before its authorization was released: exchanges = %d", b)
	}
	pending, partial := f.connect()
	if pending.Status != brokercontract.WorkspaceEnrollmentPending || len(partial) != 0 || len(f.catalogueBuilds) != 1 {
		t.Fatalf("first completed upstream published partial catalogue: status=%s builds=%d tools=%v", pending.Status, len(f.catalogueBuilds), fixtureToolNames(partial))
	}
	close(releaseSecond)
	select {
	case got := <-result:
		if got.err != nil || got.status != http.StatusOK {
			t.Fatalf("authorization callback = status %d, error %v", got.status, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ToolHive two-upstream chain did not finish")
	}
	if a, _ := f.issuerA.counts(); a != 1 {
		t.Fatalf("upstream A token exchanges = %d, want 1", a)
	}
	if b, _ := f.issuerB.counts(); b != 1 {
		t.Fatalf("upstream B token exchanges = %d, want 1", b)
	}
	connected, tools := f.connect()
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected || !reflect.DeepEqual(fixtureToolNames(tools), []string{"mcp__backend-a__whoami", "mcp__backend-b__whoami"}) {
		t.Fatalf("connected/catalogue = %#v / %v", connected, fixtureToolNames(tools))
	}
	if connected.Ref.ID != started.Ref.ID {
		t.Fatalf("mecatl enrollment changed from %q to %q", started.Ref.ID, connected.Ref.ID)
	}
}

type fixtureToolMetadata struct {
	name, description, schema string
	readOnly                  bool
}

func fixtureToolMetadataFor(tools []tool.Tool) []fixtureToolMetadata {
	metadata := make([]fixtureToolMetadata, 0, len(tools))
	for _, candidate := range tools {
		spec := candidate.Spec()
		metadata = append(metadata, fixtureToolMetadata{spec.Name, spec.Description, string(spec.Schema), candidate.ReadOnly()})
	}
	return metadata
}

func TestADR_0298_ToolHiveCompletedEnrollmentRetriesHostCompletionE2E(t *testing.T) {
	f := newMultiUpstreamFixture(t, false)
	started := f.start()
	if status := f.drive(started.URL); status != http.StatusOK {
		t.Fatalf("authorization callback status = %d", status)
	}
	f.mu.Lock()
	f.failBuilds = 1
	f.mu.Unlock()
	if _, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("injected host completion failure unexpectedly succeeded")
	}
	// The real ToolHive broker has published its authenticated snapshot before
	// the host's engine rebuild fails. The failed continuation must restore the
	// claim, so this retry reaches the same snapshot rather than rediscovery.
	f.mu.Lock()
	if got := len(f.catalogueBuilds); got != 2 {
		f.mu.Unlock()
		t.Fatalf("catalogue builds after failed replacement = %d, want create plus one failed replacement", got)
	}
	published := fixtureToolMetadataFor(f.catalogueBuilds[1])
	f.mu.Unlock()
	headersA, _ := f.mcpA.snapshot()
	headersB, _ := f.mcpB.snapshot()
	connected, tools := f.connect()
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected || connected.Ref != started.Ref {
		t.Fatalf("retried completion = %#v, want connected %v", connected, started.Ref)
	}
	if !reflect.DeepEqual(fixtureToolNames(tools), []string{"mcp__backend-a__whoami", "mcp__backend-b__whoami"}) {
		t.Fatalf("retried frozen tools = %v", fixtureToolNames(tools))
	}
	f.mu.Lock()
	if got := len(f.catalogueBuilds); got != 3 {
		f.mu.Unlock()
		t.Fatalf("catalogue builds after retry = %d, want create plus failed and retried replacements", got)
	}
	retried := fixtureToolMetadataFor(f.catalogueBuilds[2])
	f.mu.Unlock()
	if !reflect.DeepEqual(retried, published) {
		t.Fatalf("retry rebuilt a different authenticated snapshot: got %#v, want published %#v", retried, published)
	}
	afterA, _ := f.mcpA.snapshot()
	afterB, _ := f.mcpB.snapshot()
	if len(afterA) != len(headersA) || len(afterB) != len(headersB) {
		t.Fatalf("host retry repeated authenticated discovery: A %d→%d B %d→%d", len(headersA), len(afterA), len(headersB), len(afterB))
	}
}

func TestADR_0298_ToolHiveInjectsIsolatedUpstreamTokensE2E(t *testing.T) {
	f, _, tools := completeMultiUpstream(t)
	for _, backend := range []string{"backend-a", "backend-b"} {
		wrapped := toolFromFixture(t, tools, "mcp__"+backend+"__whoami")
		if _, asks := wrapped.(tool.AuthorizationRequester); asks {
			t.Fatalf("%s wrapper exposed a second mecatl authorization", backend)
		}
		result, err := wrapped.Execute(t.Context(), session.NewToolCall(session.ToolCallID("call-"+backend), wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
		if err != nil || result.IsError || !strings.Contains(result.Content, backend) {
			t.Fatalf("%s invocation = (%+v, %v)", backend, result, err)
		}
	}
	headersA, callsA := f.mcpA.snapshot()
	headersB, callsB := f.mcpB.snapshot()
	assertOnlyBearer(t, headersA, "Bearer backend-a-token")
	assertOnlyBearer(t, headersB, "Bearer backend-b-token")
	if callsA != 1 || callsB != 1 {
		t.Fatalf("tool calls A/B = %d/%d, want 1/1", callsA, callsB)
	}
}

func TestADR_0298_ToolHiveChainFailureDoesNotPublishPartialCatalogueE2E(t *testing.T) {
	// ToolHive v0.45 does not correlate an upstream access_denied response back to
	// mecatl's outer callback, so denial remains internally pending until expiry.
	// Use a deterministic, correlatable second-backend authenticated-discovery
	// failure after both real callbacks; this exercises the same aggregate
	// fail-closed catalogue boundary without pretending mecatl can inspect a leg.
	f := newMultiUpstreamFixture(t, true)
	started := f.start()
	if status := f.drive(started.URL); status != http.StatusOK {
		t.Fatalf("authorization callback status = %d", status)
	}
	failed, tools := f.connect()
	if failed.Status != brokercontract.WorkspaceEnrollmentFailed {
		t.Fatalf("failed enrollment = %#v", failed)
	}
	if len(tools) != 0 || len(f.catalogueBuilds) != 1 {
		t.Fatalf("partial catalogue published: builds=%d tools=%v", len(f.catalogueBuilds), fixtureToolNames(tools))
	}
	headersA, callsA := f.mcpA.snapshot()
	headersB, callsB := f.mcpB.snapshot()
	assertOnlyBearer(t, headersA, "Bearer backend-a-token")
	assertOnlyBearer(t, headersB, "Bearer backend-b-token")
	if callsA != 0 || callsB != 0 {
		t.Fatalf("failed catalogue discovery invoked tools A/B = %d/%d", callsA, callsB)
	}
	loaded, err := f.service.cfg.Store.Load(t.Context(), f.session.ID)
	if err != nil {
		t.Fatalf("load failed enrollment: %v", err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("terminal chain failure left the mecatl pre-prompt gate pending")
	}
}

func TestADR_0298_ToolHiveChainRetryHasNoMecatlGrantStateE2E(t *testing.T) {
	f := newMultiUpstreamFixture(t, true)
	first := f.start()
	if status := f.drive(first.URL); status != http.StatusOK {
		t.Fatalf("first authorization callback status = %d", status)
	}
	failed, _ := f.connect()
	if failed.Status != brokercontract.WorkspaceEnrollmentFailed {
		t.Fatalf("first enrollment = %#v", failed)
	}
	f.mcpB.mu.Lock()
	f.mcpB.reject = false
	f.mcpB.mu.Unlock()
	second := f.start()
	if second.Ref.ID == first.Ref.ID {
		t.Fatal("retry reused the prior opaque ToolHive operation")
	}
	if status := f.drive(second.URL); status != http.StatusOK {
		t.Fatalf("second authorization callback status = %d", status)
	}
	connected, _ := f.connect()
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("retried enrollment = %#v", connected)
	}
	if a, _ := f.issuerA.counts(); a != 2 {
		t.Fatalf("fresh operation did not re-enter upstream A: exchanges=%d", a)
	}
	if b, _ := f.issuerB.counts(); b != 2 {
		t.Fatalf("fresh operation did not re-enter upstream B: exchanges=%d", b)
	}
}

func TestADR_0298_ToolHiveRefreshIsProviderScopedE2E(t *testing.T) {
	f, _, tools := completeMultiUpstream(t)
	f.store.expire(t, "backend-a")
	for _, backend := range []string{"backend-a", "backend-b"} {
		wrapped := toolFromFixture(t, tools, "mcp__"+backend+"__whoami")
		result, err := wrapped.Execute(t.Context(), session.NewToolCall(session.ToolCallID("refresh-"+backend), wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
		if err != nil || result.IsError {
			t.Fatalf("%s invocation after A expiry = (%+v, %v)", backend, result, err)
		}
	}
	_, refreshA := f.issuerA.counts()
	_, refreshB := f.issuerB.counts()
	if refreshA != 1 || refreshB != 0 {
		t.Fatalf("refreshes A/B = %d/%d, want 1/0", refreshA, refreshB)
	}
	headersA, _ := f.mcpA.snapshot()
	headersB, _ := f.mcpB.snapshot()
	assertOnlyBearer(t, headersA, "Bearer backend-a-token", "Bearer backend-a-refreshed")
	if headersA[len(headersA)-1] != "Bearer backend-a-refreshed" {
		t.Fatalf("A final bearer = %q, want refreshed token", headersA[len(headersA)-1])
	}
	assertOnlyBearer(t, headersB, "Bearer backend-b-token")
}

func TestADR_0298_ToolHiveRefreshDoesNotReenrollMecatlSessionE2E(t *testing.T) {
	f, _, tools := completeMultiUpstream(t)
	f.store.expire(t, "backend-a")
	wrapped := toolFromFixture(t, tools, "mcp__backend-a__whoami")
	if _, err := wrapped.Execute(t.Context(), session.NewToolCall("refresh-a", wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{}); err != nil {
		t.Fatalf("invoke A after expiry: %v", err)
	}
	loaded, err := f.service.cfg.Store.Load(t.Context(), f.session.ID)
	if err != nil {
		t.Fatalf("load refreshed session: %v", err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("provider refresh created a new mecatl enrollment")
	}
	authority, ok := loaded.BoundAuthority()
	if !ok || !reflect.DeepEqual(authority.CapabilitySet.Tools, []string{"mcp__backend-a__whoami", "mcp__backend-b__whoami"}) {
		t.Fatalf("frozen authority changed after refresh: %#v, %v", authority, ok)
	}
	initialA, refreshA := f.issuerA.counts()
	initialB, refreshB := f.issuerB.counts()
	if initialA != 1 || initialB != 1 || refreshA != 1 || refreshB != 0 || len(f.catalogueBuilds) != 2 {
		t.Fatalf("refresh created another enrollment/catalogue: initial=%d/%d refresh=%d/%d builds=%d", initialA, initialB, refreshA, refreshB, len(f.catalogueBuilds))
	}
}
