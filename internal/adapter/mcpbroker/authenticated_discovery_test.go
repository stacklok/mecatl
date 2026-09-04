package mcpbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	toolhiveauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"golang.org/x/oauth2"
)

func staticTokenSource(token string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"})
}

func identityMiddleware(wantBrokerToken, provider, upstreamToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+wantBrokerToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			identity := &toolhiveauth.Identity{
				PrincipalInfo: toolhiveauth.PrincipalInfo{Subject: "toolhive-user"},
				TokenType:     "Bearer", UpstreamTokens: map[string]string{provider: upstreamToken},
			}
			next.ServeHTTP(w, request.WithContext(toolhiveauth.WithIdentity(request.Context(), identity)))
		})
	}
}

func TestADR_0298_AuthenticatedDiscoveryUsesToolHiveIdentityMiddleware(t *testing.T) {
	queries := &discoveryQueries{response: validDiscoveryResponse()}
	process := discoveryProcess(queries, identityMiddleware("opaque-broker", "provider-private", "upstream-private"), "provider-private")

	got, err := process.QueryAuthenticatedCapabilities(t.Context(), staticTokenSource("opaque-broker"), "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities: %v", err)
	}
	if queries.calls != 1 || queries.token != "upstream-private" {
		t.Fatalf("ToolHive identity query calls/token = %d/%q", queries.calls, queries.token)
	}
	if got.Backend != "private" || len(got.Tools) != 1 || got.Tools[0].Name != "mcp__private__status" {
		t.Fatalf("neutral result = %#v", got)
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
		if request.Header.Get("Authorization") != "Bearer upstream-private" {
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
	aggregationConfig := toolHiveAggregationConfig()
	resolver, err := aggregator.NewConflictResolver(aggregationConfig)
	if err != nil {
		t.Fatalf("NewConflictResolver: %v", err)
	}
	backend := vmcp.Backend{ID: "private", Name: "private", BaseURL: server.URL, TransportType: "streamable-http", AuthConfig: &types.BackendAuthStrategy{Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: "provider-private"}}}
	processCtx, cancel := context.WithCancel(context.Background())
	process := &Process{ctx: processCtx, cancel: cancel, discovery: &authenticatedDiscovery{
		capabilities: aggregator.NewDefaultAggregator(client, resolver, aggregationConfig, nil),
		backends:     vmcp.NewImmutableRegistry([]vmcp.Backend{backend}),
		incoming:     identityMiddleware("opaque-broker", "provider-private", "upstream-private"),
	}}

	got, err := process.QueryAuthenticatedCapabilities(t.Context(), staticTokenSource("opaque-broker"), "private")
	if err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities: %v", err)
	}
	if requests.Load() == 0 || len(got.Tools) != 1 || got.Tools[0].Name != "mcp__private__status" {
		t.Fatalf("ToolHive query requests/result = %d/%#v", requests.Load(), got)
	}
}

func TestQueryAuthenticatedCapabilitiesRejectsUnknownBackendBeforeCredentialUse(t *testing.T) {
	queries := &discoveryQueries{response: validDiscoveryResponse()}
	process := discoveryProcess(queries, identityMiddleware("opaque-broker", "provider-private", "upstream-private"), "provider-private")
	credential := &countingTokenSource{token: &oauth2.Token{AccessToken: "opaque-broker", TokenType: "Bearer"}}

	if _, err := process.QueryAuthenticatedCapabilities(t.Context(), credential, "unknown"); !errors.Is(err, ErrAuthenticatedDiscovery) {
		t.Fatalf("unknown backend error = %v", err)
	}
	if credential.calls.Load() != 0 || queries.calls != 0 {
		t.Fatalf("unknown backend reached credential/query = %d/%d", credential.calls.Load(), queries.calls)
	}
}

func TestQueryAuthenticatedCapabilitiesSanitizesPrivateFailures(t *testing.T) {
	const secret = "opaque-broker-secret"
	queries := &discoveryQueries{err: errors.New("upstream-private leaked")}
	process := discoveryProcess(queries, identityMiddleware(secret, "provider-private", "upstream-private"), "provider-private")
	_, err := process.QueryAuthenticatedCapabilities(t.Context(), staticTokenSource(secret), "private")
	if !errors.Is(err, ErrAuthenticatedDiscovery) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "upstream-private") {
		t.Fatalf("discovery error leaked private state: %v", err)
	}
}

func TestProcessCloseCancelsInFlightAuthenticatedDiscovery(t *testing.T) {
	queries := &blockingDiscoveryQueries{started: make(chan struct{})}
	process := discoveryProcess(queries, identityMiddleware("opaque-broker", "provider-private", "upstream-private"), "provider-private")
	result := make(chan error, 1)
	go func() {
		_, err := process.QueryAuthenticatedCapabilities(context.Background(), staticTokenSource("opaque-broker"), "private")
		result <- err
	}()
	<-queries.started
	if err := process.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAuthenticatedDiscovery) {
			t.Fatalf("QueryAuthenticatedCapabilities error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight discovery was not cancelled")
	}
}

type countingTokenSource struct {
	token *oauth2.Token
	calls atomic.Int32
}

func (s *countingTokenSource) Token() (*oauth2.Token, error) {
	s.calls.Add(1)
	return s.token, nil
}

type discoveryQueries struct {
	response *aggregator.BackendCapabilities
	err      error
	calls    int
	token    string
}

func (q *discoveryQueries) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	q.calls++
	identity, ok := toolhiveauth.IdentityFromContext(ctx)
	if !ok || len(identity.UpstreamTokens) == 0 {
		return nil, errors.New("missing ToolHive identity")
	}
	q.token = identity.UpstreamTokens[backend.AuthConfig.UpstreamInject.ProviderName]
	if q.err != nil {
		return nil, q.err
	}
	return q.response, nil
}

type blockingDiscoveryQueries struct {
	started chan struct{}
}

func (q *blockingDiscoveryQueries) QueryCapabilities(ctx context.Context, _ vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	close(q.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func discoveryProcess(queries capabilityQuerier, incoming func(http.Handler) http.Handler, provider string) *Process {
	backend := vmcp.Backend{ID: "private", Name: "private", AuthConfig: &types.BackendAuthStrategy{
		Type: "upstream_inject", UpstreamInject: &types.UpstreamInjectConfig{ProviderName: provider},
	}}
	processCtx, cancel := context.WithCancel(context.Background())
	return &Process{ctx: processCtx, cancel: cancel, discovery: &authenticatedDiscovery{
		capabilities: queries, backends: vmcp.NewImmutableRegistry([]vmcp.Backend{backend}), incoming: incoming,
	}}
}

func validDiscoveryResponse() *aggregator.BackendCapabilities {
	return &aggregator.BackendCapabilities{BackendID: "private", Tools: []vmcp.Tool{{
		Name: "status", Description: "safe", BackendID: "private", InputSchema: map[string]any{"type": "object"},
	}}}
}
