package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	toolhiveauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestQueryAuthenticatedCapabilitiesUsesOneScopedCredentialAndQuery(t *testing.T) {
	credentials := &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret", IDToken: "id-secret"}}
	queries := &discoveryQueries{response: &aggregator.BackendCapabilities{
		BackendID: "private",
		Tools: []vmcp.Tool{{
			Name: "status", Description: "safe metadata", BackendID: "private",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
	}}
	process := discoveryProcess(credentials, queries, "provider-private")

	got, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities: %v", err)
	}
	if credentials.calls != 1 || queries.calls != 1 {
		t.Fatalf("credential/query calls = %d/%d, want 1/1", credentials.calls, queries.calls)
	}
	if credentials.provider != "provider-private" || queries.backend != "private" || queries.token != "credential-secret" {
		t.Fatalf("scoped lookup/query = provider=%q backend=%q token=%q", credentials.provider, queries.backend, queries.token)
	}
	if got.Backend != "private" || len(got.Tools) != 1 || got.Tools[0].Name != "mcp__private__status" {
		t.Fatalf("neutral result = %#v", got)
	}
	if strings.Contains(got.Tools[0].Description, "provider-private") || strings.Contains(string(got.Tools[0].Schema), "credential-secret") {
		t.Fatalf("neutral result leaked private material: %#v", got)
	}

	got.Tools[0].Schema[0] = '['
	again, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
	if err != nil || string(again.Tools[0].Schema) != `{"properties":{"query":{"type":"string"}},"type":"object"}` {
		t.Fatalf("copied schema result = %#v, %v", again, err)
	}
}

func TestQueryAuthenticatedCapabilitiesUsesToolHiveScopedQuery(t *testing.T) {
	var requests atomic.Int32
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "status", Description: "safe"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, struct{}, error) {
		return &mcpsdk.CallToolResult{}, struct{}{}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer credential-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		requests.Add(1)
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(server.Close)

	outgoing := vmcpauth.NewDefaultOutgoingAuthRegistry()
	if err := outgoing.RegisterStrategy("upstream_inject", strategies.NewUpstreamInjectStrategy()); err != nil {
		t.Fatalf("RegisterStrategy: %v", err)
	}
	client, err := vmcpclient.NewHTTPBackendClient(outgoing)
	if err != nil {
		t.Fatalf("NewHTTPBackendClient: %v", err)
	}
	resolver, err := aggregator.NewConflictResolver(&vmcpconfig.AggregationConfig{ConflictResolution: vmcp.ConflictStrategyPrefix})
	if err != nil {
		t.Fatalf("NewConflictResolver: %v", err)
	}
	backend := vmcp.Backend{ID: "private", Name: "private", BaseURL: server.URL, TransportType: "streamable-http", AuthConfig: &types.BackendAuthStrategy{Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: "provider-private"}}}
	process := &Process{discovery: &authenticatedDiscovery{
		capabilities: aggregator.NewDefaultAggregator(client, resolver, nil, nil),
		backends:     vmcp.NewImmutableRegistry([]vmcp.Backend{backend}),
		tokens:       &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}},
		providers:    map[string]string{"private": "provider-private"},
	}}

	got, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities: %v", err)
	}
	if requests.Load() == 0 || len(got.Tools) != 1 || got.Tools[0].Name != "mcp__private__status" {
		t.Fatalf("ToolHive query requests/result = %d/%#v", requests.Load(), got)
	}
}

func TestQueryAuthenticatedCapabilitiesRejectsBeforeCredentialLookup(t *testing.T) {
	credentials := &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}}
	queries := &discoveryQueries{}
	process := discoveryProcess(credentials, queries, "provider-private")

	for _, backend := range []string{"unknown", "anonymous"} {
		if _, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", backend); !errors.Is(err, ErrAuthenticatedDiscovery) {
			t.Fatalf("QueryAuthenticatedCapabilities(%q) = %v, want generic discovery failure", backend, err)
		}
	}
	if credentials.calls != 0 || queries.calls != 0 {
		t.Fatalf("unknown/anonymous backend reached credential/query: %d/%d", credentials.calls, queries.calls)
	}
}

func TestQueryAuthenticatedCapabilitiesFailsClosedWithoutLeakingPrivateValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*discoveryCredentials, *discoveryQueries, *authenticatedDiscovery)
	}{
		{name: "missing credential", setup: func(c *discoveryCredentials, _ *discoveryQueries, _ *authenticatedDiscovery) { c.credential = nil }},
		{name: "empty credential", setup: func(c *discoveryCredentials, _ *discoveryQueries, _ *authenticatedDiscovery) {
			c.credential.AccessToken = ""
		}},
		{name: "mapping mismatch", setup: func(_ *discoveryCredentials, _ *discoveryQueries, d *authenticatedDiscovery) {
			d.providers["private"] = "wrong-provider"
		}},
		{name: "nil response", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) { q.response = nil }},
		{name: "backend mismatch", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.BackendID = "other"
		}},
		{name: "candidate mismatch", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.Tools[0].BackendID = "other"
		}},
		{name: "duplicate candidate", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.Tools = append(q.response.Tools, q.response.Tools[0])
		}},
		{name: "malformed tool name", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.Tools[0].Name = "bad name"
		}},
		{name: "malformed schema", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.Tools[0].InputSchema = nil
		}},
		{name: "private candidate metadata", setup: func(_ *discoveryCredentials, q *discoveryQueries, _ *authenticatedDiscovery) {
			q.response.Tools[0].Description = "provider-private credential-secret auth-session-secret"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret", IDToken: "id-secret"}}
			queries := &discoveryQueries{response: validDiscoveryResponse()}
			process := discoveryProcess(credentials, queries, "provider-private")
			test.setup(credentials, queries, process.discovery)

			_, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
			if !errors.Is(err, ErrAuthenticatedDiscovery) {
				t.Fatalf("QueryAuthenticatedCapabilities error = %v", err)
			}
			for _, private := range []string{"provider-private", "credential-secret", "id-secret", "auth-session-secret"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("error leaked %q: %q", private, err)
				}
			}
		})
	}
}

func TestQueryAuthenticatedCapabilitiesRejectsClosedProcessBeforeCredentialLookup(t *testing.T) {
	credentials := &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}}
	queries := &discoveryQueries{response: validDiscoveryResponse()}
	process := discoveryProcess(credentials, queries, "provider-private")
	if err := process.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private"); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("QueryAuthenticatedCapabilities after Close = %v, want generic discovery failure", err)
	}
	if credentials.calls != 0 || queries.calls != 0 {
		t.Fatalf("closed process reached credential/query: %d/%d", credentials.calls, queries.calls)
	}
}

func TestProcessCloseCancelsInFlightAuthenticatedDiscovery(t *testing.T) {
	queries := &blockingDiscoveryQueries{started: make(chan struct{})}
	process := discoveryProcess(&discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}}, queries, "provider-private")
	result := make(chan error, 1)
	go func() {
		_, err := process.QueryAuthenticatedCapabilities(context.Background(), "auth-session-secret", "private")
		result <- err
	}()
	<-queries.started

	if err := process.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAuthenticatedDiscovery) {
			t.Fatalf("QueryAuthenticatedCapabilities error = %v, want generic discovery failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight discovery was not cancelled by Close")
	}
	if !queries.cancelled.Load() {
		t.Fatal("query context was not cancelled by Close")
	}
}

func TestQueryAuthenticatedCapabilitiesRoutesEachBackendToItsConfiguredProvider(t *testing.T) {
	credentials := &recordingDiscoveryCredentials{credentials: map[string]*upstreamtoken.UpstreamCredential{
		"github-provider":   {AccessToken: "github-token"},
		"calendar-provider": {AccessToken: "calendar-token"},
	}}
	queries := &recordingDiscoveryQueries{}
	process := multiDiscoveryProcess(credentials, queries)

	for _, test := range []struct {
		backend, provider, token string
	}{
		{backend: "github", provider: "github-provider", token: "github-token"},
		{backend: "calendar", provider: "calendar-provider", token: "calendar-token"},
	} {
		t.Run(test.backend, func(t *testing.T) {
			got, err := process.QueryAuthenticatedCapabilities(t.Context(), "session", test.backend)
			if err != nil {
				t.Fatalf("QueryAuthenticatedCapabilities: %v", err)
			}
			if got.Backend != test.backend || len(got.Tools) != 1 || got.Tools[0].Name != "mcp__"+test.backend+"__live" {
				t.Fatalf("result = %#v", got)
			}
		})
	}
	if got, want := credentials.lookups(), []credentialLookup{{"session", "github-provider"}, {"session", "calendar-provider"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("credential lookups = %#v, want %#v", got, want)
	}
	if got, want := queries.requests(), []discoveryQuery{{"github", "github-provider", "github-token"}, {"calendar", "calendar-provider", "calendar-token"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped queries = %#v, want %#v", got, want)
	}
}

func TestQueryAuthenticatedCapabilitiesRejectsInOrderBeforeQuery(t *testing.T) {
	for _, test := range []struct {
		name                string
		credential          *upstreamtoken.UpstreamCredential
		provider            string
		wantCredentialReads int
	}{
		{name: "empty credential", credential: &upstreamtoken.UpstreamCredential{}, provider: "provider-private", wantCredentialReads: 1},
		{name: "mapping mismatch", credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}, provider: "wrong-provider", wantCredentialReads: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := &discoveryCredentials{credential: test.credential}
			queries := &discoveryQueries{response: validDiscoveryResponse()}
			process := discoveryProcess(credentials, queries, "provider-private")
			process.discovery.providers["private"] = test.provider

			_, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
			if !errors.Is(err, ErrAuthenticatedDiscovery) {
				t.Fatalf("QueryAuthenticatedCapabilities error = %v", err)
			}
			if credentials.calls != test.wantCredentialReads || queries.calls != 0 {
				t.Fatalf("credential/query calls = %d/%d, want %d/0", credentials.calls, queries.calls, test.wantCredentialReads)
			}
		})
	}
}

func TestQueryAuthenticatedCapabilitiesNeverFallsBackToProtectedStaticDeclarations(t *testing.T) {
	credentials := &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "credential-secret"}}
	queries := &discoveryQueries{response: &aggregator.BackendCapabilities{BackendID: "private", Tools: []vmcp.Tool{{
		Name: "live", Description: "live definition", BackendID: "private", InputSchema: map[string]any{"type": "object"},
	}}}}
	process := discoveryProcess(credentials, queries, "provider-private")
	process.construction.staticByBackend = map[string][]StaticTool{"private": {{
		Name: "protected-static", Description: "protected static declaration", Schema: json.RawMessage(`{"type":"string"}`), ReadOnly: true,
	}}}

	got, err := process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities live response: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "mcp__private__live" {
		t.Fatalf("live response fell back to static declaration: %#v", got)
	}

	queries.response = &aggregator.BackendCapabilities{BackendID: "private"}
	got, err = process.QueryAuthenticatedCapabilities(t.Context(), "auth-session-secret", "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities empty live response: %v", err)
	}
	if len(got.Tools) != 0 {
		t.Fatalf("empty live response fell back to static declaration: %#v", got)
	}
}

func TestQueryAuthenticatedCapabilitiesSanitizesCredentialAndQueryErrors(t *testing.T) {
	const secret = "provider-private/session-secret/token-secret"
	for _, test := range []struct {
		name        string
		credentials upstreamCredentialReader
		queries     capabilityQuerier
	}{
		{name: "credential reader", credentials: errorDiscoveryCredentials{err: errors.New(secret)}, queries: &discoveryQueries{response: validDiscoveryResponse()}},
		{name: "capability query", credentials: &discoveryCredentials{credential: &upstreamtoken.UpstreamCredential{AccessToken: "token-secret"}}, queries: errorDiscoveryQueries{err: errors.New(secret)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := discoveryProcess(test.credentials, test.queries, "provider-private")
			_, err := process.QueryAuthenticatedCapabilities(t.Context(), "session-secret", "private")
			if !errors.Is(err, ErrAuthenticatedDiscovery) || err.Error() != ErrAuthenticatedDiscovery.Error() {
				t.Fatalf("QueryAuthenticatedCapabilities error = %v, want generic discovery failure", err)
			}
			for _, private := range []string{"provider-private", "session-secret", "token-secret"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("error leaked %q: %q", private, err)
				}
			}
		})
	}
}

func TestProcessCloseCancelsCredentialLookupBeforeAnyQuery(t *testing.T) {
	credentials := &blockingDiscoveryCredentials{started: make(chan struct{})}
	queries := &discoveryQueries{response: validDiscoveryResponse()}
	process := discoveryProcess(credentials, queries, "provider-private")
	result := make(chan error, 1)
	go func() {
		_, err := process.QueryAuthenticatedCapabilities(context.Background(), "auth-session-secret", "private")
		result <- err
	}()
	<-credentials.started

	if err := process.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAuthenticatedDiscovery) {
			t.Fatalf("QueryAuthenticatedCapabilities error = %v, want generic discovery failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight credential lookup was not cancelled by Close")
	}
	if !credentials.cancelled.Load() {
		t.Fatal("credential context was not cancelled by Close")
	}
	if queries.calls != 0 {
		t.Fatalf("query calls = %d, want 0", queries.calls)
	}
}

type blockingDiscoveryQueries struct {
	started   chan struct{}
	cancelled atomic.Bool
}

func (q *blockingDiscoveryQueries) QueryCapabilities(ctx context.Context, _ vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	close(q.started)
	<-ctx.Done()
	q.cancelled.Store(true)
	return nil, ctx.Err()
}

type discoveryCredentials struct {
	credential *upstreamtoken.UpstreamCredential
	calls      int
	provider   string
}

func (c *discoveryCredentials) GetValidTokens(_ context.Context, _, provider string) (*upstreamtoken.UpstreamCredential, error) {
	c.calls++
	c.provider = provider
	if c.credential == nil {
		return nil, errors.New("credential unavailable")
	}
	credentialCopy := *c.credential
	return &credentialCopy, nil
}

type discoveryQueries struct {
	response *aggregator.BackendCapabilities
	calls    int
	backend  string
	token    string
}

// This assignment fails if the discovery seam grows an aggregate-query method.
var _ capabilityQuerier = (*discoveryQueries)(nil)

func (q *discoveryQueries) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	q.calls++
	q.backend = backend.ID
	identity, ok := toolhiveauth.IdentityFromContext(ctx)
	if !ok || len(identity.UpstreamTokens) != 1 {
		return nil, errors.New("missing scoped identity")
	}
	q.token = identity.UpstreamTokens[backend.AuthConfig.UpstreamInject.ProviderName]
	return q.response, nil
}

func discoveryProcess(credentials upstreamCredentialReader, queries capabilityQuerier, provider string) *Process {
	backend := vmcp.Backend{ID: "private", Name: "private", AuthConfig: &types.BackendAuthStrategy{
		Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: provider},
	}}
	anonymous := vmcp.Backend{ID: "anonymous", Name: "anonymous"}
	processCtx, cancel := context.WithCancel(context.Background())
	return &Process{ctx: processCtx, cancel: cancel, discovery: &authenticatedDiscovery{
		capabilities: queries, backends: vmcp.NewImmutableRegistry([]vmcp.Backend{backend, anonymous}), tokens: credentials,
		providers: map[string]string{"private": provider},
	}}
}

func validDiscoveryResponse() *aggregator.BackendCapabilities {
	return &aggregator.BackendCapabilities{BackendID: "private", Tools: []vmcp.Tool{{
		Name: "status", Description: "safe", BackendID: "private", InputSchema: map[string]any{"type": "object"},
	}}}
}

type credentialLookup struct {
	session, provider string
}

type recordingDiscoveryCredentials struct {
	mu          sync.Mutex
	credentials map[string]*upstreamtoken.UpstreamCredential
	requests    []credentialLookup
}

func (c *recordingDiscoveryCredentials) GetValidTokens(_ context.Context, session, provider string) (*upstreamtoken.UpstreamCredential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, credentialLookup{session, provider})
	credential := c.credentials[provider]
	if credential == nil {
		return nil, errors.New("credential unavailable")
	}
	copy := *credential
	return &copy, nil
}

func (c *recordingDiscoveryCredentials) lookups() []credentialLookup {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]credentialLookup(nil), c.requests...)
}

type discoveryQuery struct {
	backend, provider, token string
}

type recordingDiscoveryQueries struct {
	mu    sync.Mutex
	calls []discoveryQuery
}

func (q *recordingDiscoveryQueries) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	identity, ok := toolhiveauth.IdentityFromContext(ctx)
	if !ok || backend.AuthConfig == nil || backend.AuthConfig.UpstreamInject == nil {
		return nil, errors.New("missing scoped identity")
	}
	provider := backend.AuthConfig.UpstreamInject.ProviderName
	q.mu.Lock()
	q.calls = append(q.calls, discoveryQuery{backend.ID, provider, identity.UpstreamTokens[provider]})
	q.mu.Unlock()
	return &aggregator.BackendCapabilities{BackendID: backend.ID, Tools: []vmcp.Tool{{
		Name: "live", Description: "live", BackendID: backend.ID, InputSchema: map[string]any{"type": "object"},
	}}}, nil
}

func (q *recordingDiscoveryQueries) requests() []discoveryQuery {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]discoveryQuery(nil), q.calls...)
}

type errorDiscoveryCredentials struct{ err error }

func (c errorDiscoveryCredentials) GetValidTokens(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
	return nil, c.err
}

type errorDiscoveryQueries struct{ err error }

func (q errorDiscoveryQueries) QueryCapabilities(context.Context, vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	return nil, q.err
}

type blockingDiscoveryCredentials struct {
	started   chan struct{}
	cancelled atomic.Bool
}

func (c *blockingDiscoveryCredentials) GetValidTokens(ctx context.Context, _, _ string) (*upstreamtoken.UpstreamCredential, error) {
	close(c.started)
	<-ctx.Done()
	c.cancelled.Store(true)
	return nil, ctx.Err()
}

func multiDiscoveryProcess(credentials upstreamCredentialReader, queries capabilityQuerier) *Process {
	backends := []vmcp.Backend{
		{ID: "github", Name: "github", AuthConfig: &types.BackendAuthStrategy{Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: "github-provider"}}},
		{ID: "calendar", Name: "calendar", AuthConfig: &types.BackendAuthStrategy{Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: "calendar-provider"}}},
	}
	processCtx, cancel := context.WithCancel(context.Background())
	return &Process{ctx: processCtx, cancel: cancel, discovery: &authenticatedDiscovery{
		capabilities: queries, backends: vmcp.NewImmutableRegistry(backends), tokens: credentials,
		providers: map[string]string{"github": "github-provider", "calendar": "calendar-provider"},
	}}
}
