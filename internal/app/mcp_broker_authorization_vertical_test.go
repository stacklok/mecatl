package app_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

// TestSessionMCPAuthorization_GrantRegressionParksAndResumes is the full
// reconstructed vertical for a statically declared protected route. It uses
// app.Build, the real dispatcher and Service controls, and real TLS OAuth and
// streaming-HTTP MCP fixtures. A terminal RFC 6749 refresh failure must revoke
// the route grant before the model's next exact call reaches dispatch.
func TestSessionMCPAuthorization_GrantRegressionParksAndResumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	const (
		call1    = "protected-call-1"
		call2    = "protected-call-2"
		call3    = "protected-call-3"
		toolName = "mcp__github__protected"
	)

	callbackMux := http.NewServeMux()
	callbackServer := httptest.NewUnstartedServer(callbackMux)
	callbackServer.StartTLS()
	t.Cleanup(callbackServer.Close)
	fixture := newBrokerAuthorizationFixture(t, callbackServer.URL+"/oauth/callback")
	roots := x509.NewCertPool()
	roots.AddCert(callbackServer.Certificate())
	roots.AddCert(fixture.oauth.Certificate())
	roots.AddCert(fixture.mcp.Certificate())

	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall(call1, toolName, json.RawMessage(`{"request":"first"}`))),
		mockllm.TextTurn("first continuation complete"),
		mockllm.ToolCallTurn(session.NewToolCall(call2, toolName, json.RawMessage(`{"request":"regressed"}`))),
		mockllm.ToolCallTurn(session.NewToolCall(call3, toolName, json.RawMessage(`{"request":"retry"}`))),
		mockllm.TextTurn("second continuation complete"),
	)
	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{
		CallbackURL: callbackServer.URL + "/oauth/callback",
		Routes: []permconfig.MCPServerProfile{{
			Name: "github",
			URL:  fixture.mcp.URL,
			Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{
					AuthorizationEndpoint: fixture.oauth.URL + "/authorize",
					TokenEndpoint:         fixture.oauth.URL + "/token",
				}},
				Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{
					ID: "vertical-client", SecretEnv: "MECATL_VERTICAL_CLIENT_SECRET",
				}},
				Scopes: []string{"read"}, RequestRefreshToken: true,
			}},
		}},
	})
	fixture.mcpClient = fixture.clientWithRoots(roots)
	built, err := app.Build(ctx, app.Config{
		Workspace:         t.TempDir(),
		StoreDir:          t.TempDir(),
		MockProvider:      provider,
		NoSoul:            true,
		AllowAllTools:     true,
		OwnershipEnforced: true,
		MCPAuthority:      declaration,
		MCPBrokerDiscovered: []mcpbroker.ToolDefinition{{
			Backend: "github", Name: toolName, Description: "read protected data", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true,
		}},
		MCPBrokerCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
			return session.ToolResult{}, fmt.Errorf("anonymous protected call")
		},
		MCPBrokerAuthorizedCaller: fixture.callProtected,
		MCPBrokerOptions: []mcpbroker.Option{
			mcpbroker.WithOAuthLoopbackForTest(t, roots),
			mcpbroker.WithOAuthLimits(2*time.Minute, 3*time.Second),
			mcpbroker.WithOAuthSecretResolver(func(context.Context, string) (string, error) { return "vertical-secret", nil }),
		},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker == nil {
		t.Fatal("app.Build did not construct the broker runtime")
	}
	if err := built.MountMCPBrokerHandlers(callbackMux); err != nil {
		t.Fatalf("mount callback: %v", err)
	}

	owner := &session.Principal{Issuer: "https://identity.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ownerCtx := session.WithPrincipal(ctx, owner)
	intruderCtx := session.WithPrincipal(ctx, &session.Principal{Issuer: owner.Issuer, Subject: "mallory", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { built.Service.CloseSession(sess.ID) })

	firstEvents, firstRun := runAndDrain(t, ownerCtx, built.Service, sess.ID, "read protected data")
	if firstRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("first run outcome = %v, want authorization pending", firstRun.Outcome())
	}
	if got := fixture.backendCalls.Load(); got != 0 {
		t.Fatalf("backend calls before authorization = %d, want 0", got)
	}
	firstPending := requiredAuthorization(t, firstEvents, call1)
	assertDurablePending(t, built.Service, ownerCtx, sess.ID, call1, firstPending.AuthorizationID)
	firstControl := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: firstPending.AuthorizationID}
	if _, err := built.Service.MCPAuthorizationPresentation(intruderCtx, sess.ID, firstControl); err == nil {
		t.Fatal("non-owner obtained authorization presentation")
	}
	completeBrowserAuthorization(t, built.Service, ownerCtx, sess.ID, firstControl, fixture.browserClient(roots))
	firstContinuation, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, firstControl)
	if err != nil || firstContinuation.Status != session.AuthorizationGranted || firstContinuation.Run == nil {
		t.Fatalf("first recheck = %+v, %v", firstContinuation, err)
	}
	drainAndFinish(t, ownerCtx, built.Service, sess.ID, firstContinuation.Run)
	assertToolResult(t, built.Service, ownerCtx, sess.ID, call1, false, "protected result")
	assertAuthorizedCalls(t, fixture, sess.ID, []observedProtectedCall{{backend: "github", call: session.NewToolCall(call1, toolName, json.RawMessage(`{"request":"first"}`))}})
	if got := fixture.backendCalls.Load(); got != 1 {
		t.Fatalf("backend calls after exact first continuation = %d, want 1", got)
	}

	// Every issued token is immediately stale. Successful refreshes still service
	// the current request, while the next request refreshes again deterministically.
	// Switching the fixture to invalid_grant therefore needs no wall-clock sleep.
	fixture.rejectRefresh.Store(true)
	secondEvents, secondRun := runAndDrain(t, ownerCtx, built.Service, sess.ID, "read after credential regression")
	if secondRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("regression run outcome = %v, want authorization pending", secondRun.Outcome())
	}
	if got := fixture.invalidGrantResponses.Load(); got != 1 {
		t.Fatalf("terminal invalid_grant responses = %d, want exactly 1", got)
	}
	assertTokenSequence(t, fixture, []string{"exchange", "refresh:refresh-initial-1:ok", "refresh:refresh-1:invalid_grant"})
	if got := fixture.backendCalls.Load(); got != 1 {
		t.Fatalf("backend calls after invalid_grant and re-park = %d, want 1", got)
	}
	assertExactToolResult(t, built.Service, ownerCtx, sess.ID, call2, true, `tool "mcp__github__protected" failed: OAuth token refresh failed`)
	assertAuthorizedCalls(t, fixture, sess.ID, []observedProtectedCall{
		{backend: "github", call: session.NewToolCall(call1, toolName, json.RawMessage(`{"request":"first"}`))},
		{backend: "github", call: session.NewToolCall(call2, toolName, json.RawMessage(`{"request":"regressed"}`))},
	})
	secondPending := requiredAuthorization(t, secondEvents, call3)
	assertDurablePending(t, built.Service, ownerCtx, sess.ID, call3, secondPending.AuthorizationID)

	fixture.rejectRefresh.Store(false)
	secondControl := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: secondPending.AuthorizationID}
	completeBrowserAuthorization(t, built.Service, ownerCtx, sess.ID, secondControl, fixture.browserClient(roots))
	secondContinuation, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, secondControl)
	if err != nil || secondContinuation.Status != session.AuthorizationGranted || secondContinuation.Run == nil {
		t.Fatalf("second recheck = %+v, %v", secondContinuation, err)
	}
	drainAndFinish(t, ownerCtx, built.Service, sess.ID, secondContinuation.Run)
	assertTokenSequence(t, fixture, []string{
		"exchange", "refresh:refresh-initial-1:ok", "refresh:refresh-1:invalid_grant",
		"exchange", "refresh:refresh-initial-2:ok",
	})
	assertAuthorizedCalls(t, fixture, sess.ID, []observedProtectedCall{
		{backend: "github", call: session.NewToolCall(call1, toolName, json.RawMessage(`{"request":"first"}`))},
		{backend: "github", call: session.NewToolCall(call2, toolName, json.RawMessage(`{"request":"regressed"}`))},
		{backend: "github", call: session.NewToolCall(call3, toolName, json.RawMessage(`{"request":"retry"}`))},
	})
	if got := fixture.backendCalls.Load(); got != 2 {
		t.Fatalf("backend calls after exact second continuation = %d, want 2", got)
	}
	assertToolResult(t, built.Service, ownerCtx, sess.ID, call3, false, "protected result")

	finished, err := built.Service.GetSession(ownerCtx, sess.ID)
	if err != nil {
		t.Fatalf("load completed session: %v", err)
	}
	if finished.State != session.StateCompleted {
		t.Fatalf("final state = %s, want completed", finished.State)
	}
	if err := session.ValidateToolPairing(finished.Conversation.Messages); err != nil {
		t.Fatalf("final tool pairing: %v", err)
	}
}

func runAndDrain(t *testing.T, ctx context.Context, svc *server.Service, id session.SessionID, prompt string) ([]session.Event, *agent.Run) {
	t.Helper()
	run, err := svc.StartInteractiveRunContent(ctx, id, prompt, nil)
	if err != nil {
		t.Fatalf("StartInteractiveRunContent: %v", err)
	}
	events := drainEvents(t, ctx, run)
	svc.FinishRun(id, run)
	return events, run
}

func drainAndFinish(t *testing.T, ctx context.Context, svc *server.Service, id session.SessionID, run *agent.Run) {
	t.Helper()
	_ = drainEvents(t, ctx, run)
	svc.FinishRun(id, run)
}

func drainEvents(t *testing.T, ctx context.Context, run *agent.Run) []session.Event {
	t.Helper()
	var events []session.Event
	for {
		select {
		case event, ok := <-run.Events():
			if !ok {
				return events
			}
			events = append(events, event)
		case <-ctx.Done():
			run.Cancel()
			t.Fatalf("timed out draining run events: %v", ctx.Err())
		}
	}
}

func requiredAuthorization(t *testing.T, events []session.Event, call session.ToolCallID) session.AuthorizationPayload {
	t.Helper()
	for _, event := range events {
		if event.Type == session.EvAuthorizationRequired && event.Authorization != nil {
			if event.Authorization.Call != call {
				t.Fatalf("authorization call = %q, want %q", event.Authorization.Call, call)
			}
			return *event.Authorization
		}
	}
	t.Fatalf("no authorization.required event for %q", call)
	return session.AuthorizationPayload{}
}

func assertDurablePending(t *testing.T, svc *server.Service, ctx context.Context, id session.SessionID, call session.ToolCallID, authorizationID string) {
	t.Helper()
	parked, err := svc.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("load parked session: %v", err)
	}
	pending, ok := parked.PendingAuthorization()
	if parked.State != session.StateAuthorizing || !ok || pending.Call.ID != call || pending.Authorization.ID != authorizationID {
		t.Fatalf("durable pending state = state:%s pending:%+v", parked.State, pending)
	}
}

func completeBrowserAuthorization(t *testing.T, svc *server.Service, ctx context.Context, id session.SessionID, control server.MCPAuthorizationControl, client *http.Client) {
	t.Helper()
	presentation, err := svc.MCPAuthorizationPresentation(ctx, id, control)
	if err != nil || presentation == "" {
		t.Fatalf("authorization presentation = %q, %v", presentation, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, presentation, nil)
	if err != nil {
		t.Fatalf("create browser request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("browser authorization: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
}

func assertToolResult(t *testing.T, svc *server.Service, ctx context.Context, id session.SessionID, call session.ToolCallID, wantError bool, contains string) {
	t.Helper()
	loaded, err := svc.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("load session for result: %v", err)
	}
	for _, message := range loaded.Conversation.Messages {
		if message.ToolResult != nil && message.ToolResult.CallID == call {
			if message.ToolResult.IsError != wantError || !strings.Contains(message.ToolResult.Content, contains) {
				t.Fatalf("tool result for %q = %+v", call, message.ToolResult)
			}
			return
		}
	}
	t.Fatalf("no tool result for %q", call)
}

func assertExactToolResult(t *testing.T, svc *server.Service, ctx context.Context, id session.SessionID, call session.ToolCallID, wantError bool, content string) {
	t.Helper()
	loaded, err := svc.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("load session for exact result: %v", err)
	}
	for _, message := range loaded.Conversation.Messages {
		if message.ToolResult != nil && message.ToolResult.CallID == call {
			if message.ToolResult.IsError != wantError || message.ToolResult.Content != content {
				t.Fatalf("tool result for %q = %+v, want error=%v content=%q", call, message.ToolResult, wantError, content)
			}
			return
		}
	}
	t.Fatalf("no tool result for %q", call)
}

type observedProtectedCall struct {
	ref     session.SessionID
	backend string
	call    session.ToolCall
}

func assertAuthorizedCalls(t *testing.T, fixture *brokerAuthorizationFixture, id session.SessionID, want []observedProtectedCall) {
	t.Helper()
	fixture.mu.Lock()
	got := append([]observedProtectedCall(nil), fixture.calls...)
	fixture.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("authorized calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ref != id || got[i].backend != want[i].backend || got[i].call.ID != want[i].call.ID || got[i].call.Name != want[i].call.Name || string(got[i].call.Args) != string(want[i].call.Args) {
			t.Fatalf("authorized call %d = %+v, want ref=%q call=%+v", i, got[i], id, want[i])
		}
	}
}

func assertTokenSequence(t *testing.T, fixture *brokerAuthorizationFixture, want []string) {
	t.Helper()
	fixture.mu.Lock()
	got := append([]string(nil), fixture.tokenSequence...)
	fixture.mu.Unlock()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("OAuth token sequence = %v, want %v", got, want)
	}
}

type brokerAuthorizationFixture struct {
	t                     *testing.T
	callbackURL           string
	oauth                 *httptest.Server
	mcp                   *httptest.Server
	mcpClient             *http.Client
	mu                    sync.Mutex
	calls                 []observedProtectedCall
	tokenSequence         []string
	backendCalls          atomic.Int32
	exchanges             atomic.Int32
	refreshes             atomic.Int32
	invalidGrantResponses atomic.Int32
	rejectRefresh         atomic.Bool
}

func newBrokerAuthorizationFixture(t *testing.T, callbackURL string) *brokerAuthorizationFixture {
	t.Helper()
	fixture := &brokerAuthorizationFixture{t: t, callbackURL: callbackURL}
	protected := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected-fixture", Version: "1"}, nil)
	mcpsdk.AddTool(protected, &mcpsdk.Tool{Name: "protected"}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, struct{}, error) {
		fixture.backendCalls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "protected result"}}}, struct{}{}, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return protected }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fixture.mcp = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fresh-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.mcp.Close)

	oauthMux := http.NewServeMux()
	fixture.oauth = httptest.NewUnstartedServer(oauthMux)
	oauthMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{"code": {"fixture-code"}, "state": {r.URL.Query().Get("state")}}
		http.Redirect(w, r, fixture.callbackURL+"?"+query.Encode(), http.StatusFound)
	})
	oauthMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			fixture.refreshes.Add(1)
			refreshToken := r.Form.Get("refresh_token")
			if fixture.rejectRefresh.Load() {
				fixture.invalidGrantResponses.Add(1)
				fixture.recordTokenStep("refresh:" + refreshToken + ":invalid_grant")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			fixture.recordTokenStep("refresh:" + refreshToken + ":ok")
			n := fixture.refreshes.Load()
			_, _ = fmt.Fprintf(w, `{"access_token":"fresh-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":-1}`, n, n)
			return
		}
		exchange := fixture.exchanges.Add(1)
		fixture.recordTokenStep("exchange")
		_, _ = fmt.Fprintf(w, `{"access_token":"stale","refresh_token":"refresh-initial-%d","token_type":"Bearer","expires_in":-1}`, exchange)
	})
	fixture.oauth.StartTLS()
	t.Cleanup(fixture.oauth.Close)
	return fixture
}

func (f *brokerAuthorizationFixture) recordTokenStep(step string) {
	f.mu.Lock()
	f.tokenSequence = append(f.tokenSequence, step)
	f.mu.Unlock()
}

func (f *brokerAuthorizationFixture) clientWithRoots(roots *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	f.t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func (f *brokerAuthorizationFixture) browserClient(roots *x509.CertPool) *http.Client {
	return f.clientWithRoots(roots)
}

func (f *brokerAuthorizationFixture) callProtected(ctx context.Context, ref mcpbroker.SessionRef, backend string, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, observedProtectedCall{ref: ref.SessionID(), backend: backend, call: session.NewToolCall(call.ID, call.Name, append(json.RawMessage(nil), call.Args...))})
	f.mu.Unlock()
	token, err := tokens.Token()
	if err != nil {
		return session.ToolResult{}, err
	}
	client := &http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(token), Base: f.mcpClient.Transport}, Timeout: 3 * time.Second}
	upstream, err := mcpadapter.Connect(ctx, mcpadapter.ServerConfig{Name: backend, URL: f.mcp.URL, HTTPClient: client}, nil)
	if err != nil {
		return session.ToolResult{}, err
	}
	defer upstream.Close()
	for _, candidate := range upstream.Tools() {
		if candidate.Spec().Name == call.Name {
			return candidate.Execute(ctx, call, tool.Environment{})
		}
	}
	return session.ToolResult{}, fmt.Errorf("fixture omitted protected tool %q", call.Name)
}
