package app_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// TestSingletonBrokerRemediation_Scenario5_Stage3RemoteVertical is the full
// production-equivalent vertical for a statically declared protected route. It uses
// app.Build, the real dispatcher and Service controls, and real TLS OAuth and
// streaming-HTTP MCP fixtures. A terminal RFC 6749 refresh failure must revoke
// the route grant before the model's next exact call reaches dispatch.
func TestSingletonBrokerRemediation_Scenario5_Stage3RemoteVertical(t *testing.T) {
	runSingletonBrokerStage3RemoteVertical(t)
}

// TestSingletonBrokerRemediation_Scenario5_RestartBoundary replaces the real
// production lifecycle at a stable endpoint. A parked attachment must fail closed,
// while a fresh pre-prompt factory may attach to the replacement incarnation.
func TestSingletonBrokerRemediation_Scenario5_RestartBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	identity := newStage3WorkloadIssuer(t)
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificateSource.Close)
	address := reserveStage3Address(t)
	callbackURL := "https://" + address + "/oauth/callback"
	credentialDir := t.TempDir()
	caFile := filepath.Join(credentialDir, "broker-ca.pem")
	tokenFile := filepath.Join(credentialDir, "broker-token")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateSource.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(identity.token(t, "mecak8s", "mecak8s")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	productionConfig := func() mcpbrokerserver.ProductionConfig {
		return mcpbrokerserver.ProductionConfig{
			PublicAddress: address,
			AdminAddress:  "127.0.0.1:0",
			TLSConfig:     &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
			OIDC: mcpbrokerserver.OIDCConfig{
				Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s",
				AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute,
			},
			ToolHive: mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{
				Name: "github", URL: "https://unused.example/mcp", Auth: "oauth",
				OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token", ClientID: "restart-client", ClientSecretEnv: "MECATL_RESTART_CLIENT_SECRET"},
				Static: []mcpbroker.StaticTool{{Name: "protected", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
			}}},
			PropagationWait: time.Millisecond,
			DrainTimeout:    time.Second,
		}
	}
	t.Setenv("MECATL_RESTART_CLIENT_SECRET", "restart-secret")
	remoteConfig := mcpbrokergrpc.RemoteFactoryConfig{
		Target: address, CAFile: caFile, ServerName: "example.com", TokenFile: tokenFile,
		Transport: mcpbrokergrpc.DefaultConfig(),
	}

	oldLifecycle, err := mcpbrokerserver.NewProduction(ctx, productionConfig())
	if err != nil {
		t.Fatalf("construct old production lifecycle: %v", err)
	}
	oldLifecycle.Start()
	oldService, closeOldClient, err := mcpbrokergrpc.NewRemoteFactory(remoteConfig)(ctx)
	if err != nil {
		t.Fatalf("construct old production client: %v", err)
	}
	defer func() { _ = closeOldClient() }()
	oldAttachment, _, err := oldService.AttachSession(ctx, "scenario5-restart")
	if err != nil {
		t.Fatalf("attach old production client: %v", err)
	}
	if err := oldAttachment.Commit(ctx); err != nil {
		t.Fatalf("commit old attachment: %v", err)
	}
	oldTool := oldAttachment.Tools()[0].(tool.AuthorizationRequester)
	parkedCall := session.NewToolCall("parked-call", "mcp__github__protected", json.RawMessage(`{}`))
	parked, _, err := oldTool.RequestAuthorization(ctx, parkedCall)
	if err != nil || parked.ID == "" {
		t.Fatalf("park authorization before restart = (%+v, %v)", parked, err)
	}
	if err := oldLifecycle.Close(ctx); err != nil {
		t.Fatalf("close old production lifecycle: %v", err)
	}

	newLifecycle, err := mcpbrokerserver.NewProduction(ctx, productionConfig())
	if err != nil {
		t.Fatalf("construct replacement production lifecycle: %v", err)
	}
	defer func() { _ = newLifecycle.Close(context.Background()) }()
	newLifecycle.Start()
	if _, _, err := oldTool.RequestAuthorization(ctx, parkedCall); !errors.Is(err, brokercontract.ErrStateUnavailable) {
		t.Fatalf("parked continuation after replacement = %v, want state unavailable without redispatch", err)
	}

	freshService, closeFreshClient, err := mcpbrokergrpc.NewRemoteFactory(remoteConfig)(ctx)
	if err != nil {
		t.Fatalf("construct replacement production client: %v", err)
	}
	defer func() { _ = closeFreshClient() }()
	freshAttachment, _, err := freshService.AttachSession(ctx, "scenario5-restart")
	if err != nil {
		t.Fatalf("fresh pre-prompt attach: %v", err)
	}
	if freshAttachment.Binding() == oldAttachment.Binding() {
		t.Fatal("replacement lifecycle reused the parked attachment binding")
	}
	if err := freshAttachment.Commit(ctx); err != nil {
		t.Fatalf("commit fresh attachment: %v", err)
	}
	freshCall := session.NewToolCall("fresh-call", "mcp__github__protected", json.RawMessage(`{}`))
	freshAuthorization, _, err := freshAttachment.Tools()[0].(tool.AuthorizationRequester).RequestAuthorization(ctx, freshCall)
	if err != nil || freshAuthorization.ID == "" || freshAuthorization.ID == parked.ID {
		t.Fatalf("fresh pre-prompt recovery authorization = (%+v, %v), parked=%+v", freshAuthorization, err, parked)
	}
}

// This structural invariant independently pins both expensive Scenario 5 proofs
// to the production constructor and remote factory and rejects hand-built gRPC.
func TestInvariant_singleton_broker_named_proofs_use_production_paths(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "mcp_broker_authorization_vertical_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	proofs := map[string]map[string]int{
		"runSingletonBrokerStage3RemoteVertical":                   {},
		"TestSingletonBrokerRemediation_Scenario5_RestartBoundary": {},
	}
	var current map[string]int
	ast.Inspect(file, func(node ast.Node) bool {
		if fn, ok := node.(*ast.FuncDecl); ok {
			current = proofs[fn.Name.Name]
			return current != nil
		}
		if node == nil || current == nil {
			return true
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok {
			current[pkg.Name+"."+selector.Sel.Name]++
		}
		return true
	})
	for proof, seen := range proofs {
		for _, required := range []string{"mcpbrokerserver.NewProduction", "mcpbrokergrpc.NewRemoteFactory"} {
			if seen[required] == 0 {
				t.Errorf("%s bypassed %s", proof, required)
			}
		}
		for _, forbidden := range []string{"grpc.NewServer", "mcpbrokergrpc.NewServer", "mcpbrokergrpc.NewServerWithConfig", "mcpbrokergrpc.RegisterServer"} {
			if seen[forbidden] != 0 {
				t.Errorf("%s hand-builds broker transport through %s", proof, forbidden)
			}
		}
	}
	if proofs["runSingletonBrokerStage3RemoteVertical"]["app.Build"] == 0 {
		t.Error("Stage 3 production vertical bypassed app.Build")
	}
}

func runSingletonBrokerStage3RemoteVertical(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	const (
		call1    = "protected-call-1"
		call2    = "protected-call-2"
		call3    = "protected-call-3"
		toolName = "mcp__github__protected"
	)

	identity := newStage3WorkloadIssuer(t)
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificateSource.Close)
	callbackAddress := reserveStage3Address(t)
	callbackURL := "https://" + callbackAddress + "/oauth/callback"
	fixture := newBrokerAuthorizationFixture(t, callbackURL)
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	fixture.mcpClient = fixture.clientWithRoots(roots)

	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{
		CallbackURL: callbackURL,
		Routes: []permconfig.MCPServerProfile{{
			Name: "github", URL: fixture.mcp.URL,
			Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token"}},
				Client:   permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "vertical-client", SecretEnv: "MECATL_VERTICAL_CLIENT_SECRET"}},
				Scopes:   []string{"read"}, RequestRefreshToken: true,
			}},
		}},
	})
	t.Setenv("MECATL_VERTICAL_CLIENT_SECRET", "vertical-secret")
	brokerHTTPClient := fixture.clientWithRoots(roots)
	lifecycle, err := mcpbrokerserver.NewProduction(ctx, mcpbrokerserver.ProductionConfig{
		PublicAddress: callbackAddress, AdminAddress: "127.0.0.1:0",
		TLSConfig:       &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
		OIDC:            mcpbrokerserver.OIDCConfig{Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s", AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute},
		ToolHive:        mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{Name: "github", URL: fixture.mcp.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token", ClientID: "vertical-client", ClientSecretEnv: "MECATL_VERTICAL_CLIENT_SECRET", Scopes: []string{"read"}, RequestRefreshToken: true}, Static: []mcpbroker.StaticTool{{Name: "protected", Description: "read protected data", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}}},
		ToolHiveOptions: []mcpbroker.Option{mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, brokerHTTPClient), mcpbroker.WithOAuthLimits(2*time.Minute, 3*time.Second), mcpbroker.WithOAuthSecretResolver(func(context.Context, string) (string, error) { return "vertical-secret", nil })},
		PropagationWait: time.Millisecond, DrainTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("construct production broker lifecycle: %v", err)
	}
	t.Cleanup(func() { _ = lifecycle.Close(context.Background()) })
	lifecycle.Start()

	credentialDir := t.TempDir()
	caFile := filepath.Join(credentialDir, "broker-ca.pem")
	tokenFile := filepath.Join(credentialDir, "broker-token")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateSource.Certificate().Raw})
	if err := os.WriteFile(caFile, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	workloadToken := identity.token(t, "mecak8s", "mecak8s")
	if err := os.WriteFile(tokenFile, []byte(workloadToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := mcpbrokergrpc.NewRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{
		Target: lifecycle.PublicAddress(), CAFile: caFile, ServerName: "example.com", TokenFile: tokenFile,
		Transport: mcpbrokergrpc.DefaultConfig(),
	})
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall(call1, toolName, json.RawMessage(`{"request":"first"}`))),
		mockllm.TextTurn("first continuation complete"),
		mockllm.ToolCallTurn(session.NewToolCall(call2, toolName, json.RawMessage(`{"request":"regressed"}`))),
		mockllm.ToolCallTurn(session.NewToolCall(call3, toolName, json.RawMessage(`{"request":"retry"}`))),
		mockllm.TextTurn("second continuation complete"),
	)
	built, err := app.Build(ctx, app.Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), MockProvider: provider, NoSoul: true,
		AllowAllTools: true, OwnershipEnforced: true, MCPAuthority: declaration,
		MCPBrokerFactory: factory, MCPBrokerFactoryRequired: true,
	})
	if err != nil {
		t.Fatalf("build remote Stage 3 client: %v", err)
	}
	t.Cleanup(built.Close)
	if built.MCPBroker != nil || !built.MCPBrokerHandlers.Empty() {
		t.Fatal("remote Stage 3 composition bypassed the production factory")
	}

	owner := &session.Principal{Issuer: "https://identity.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ownerCtx := session.WithPrincipal(ctx, owner)
	intruderCtx := session.WithPrincipal(ctx, &session.Principal{Issuer: owner.Issuer, Subject: "mallory", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !lifecycle.Ready(ctx) {
		t.Fatal("production broker lifecycle is not ready after authenticated attach")
	}
	t.Cleanup(func() { built.Service.CloseSession(sess.ID) })

	firstEvents, firstRun := runAndDrain(ownerCtx, t, built.Service, sess.ID, "read protected data")
	if firstRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("first run outcome = %v, want authorization pending", firstRun.Outcome())
	}
	if got := fixture.backendCalls.Load(); got != 0 {
		t.Fatalf("backend calls before authorization = %d, want 0", got)
	}
	firstPending := requiredAuthorization(t, firstEvents, call1)
	assertDurablePending(ownerCtx, t, built.Service, sess.ID, call1, firstPending.AuthorizationID)
	firstControl := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: firstPending.AuthorizationID}
	if _, err := built.Service.MCPAuthorizationPresentation(intruderCtx, sess.ID, firstControl); err == nil {
		t.Fatal("non-owner obtained authorization presentation")
	}
	completeBrowserAuthorization(ownerCtx, t, built.Service, sess.ID, firstControl, fixture.browserClient(roots))
	firstContinuation, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, firstControl)
	if err != nil || firstContinuation.Status != session.AuthorizationGranted || firstContinuation.Run == nil {
		t.Fatalf("first recheck = %+v, %v", firstContinuation, err)
	}
	drainAndFinish(ownerCtx, t, built.Service, sess.ID, firstContinuation.Run)
	assertToolResult(ownerCtx, t, built.Service, sess.ID, call1, false, "protected result")
	if got := fixture.backendCalls.Load(); got != 1 {
		t.Fatalf("backend calls after exact first continuation = %d, want 1", got)
	}

	// Every issued token is immediately stale. Successful refreshes still service
	// the current request, while the next request refreshes again deterministically.
	// Switching the fixture to invalid_grant therefore needs no wall-clock sleep.
	fixture.rejectRefresh.Store(true)
	secondEvents, secondRun := runAndDrain(ownerCtx, t, built.Service, sess.ID, "read after credential regression")
	if secondRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		last := secondEvents[len(secondEvents)-1]
		t.Fatalf("regression run outcome = %v, result=%+v backend=%d, want authorization pending", secondRun.Outcome(), last.Result, fixture.backendCalls.Load())
	}
	if got := fixture.invalidGrantResponses.Load(); got == 0 {
		t.Fatal("terminal invalid_grant was not observed")
	}
	if got := fixture.backendCalls.Load(); got != 1 {
		t.Fatalf("backend calls after invalid_grant and re-park = %d, want 1", got)
	}
	assertToolResult(ownerCtx, t, built.Service, sess.ID, call2, true, "remote tool outcome is unknown")
	secondPending := requiredAuthorization(t, secondEvents, call3)
	assertDurablePending(ownerCtx, t, built.Service, sess.ID, call3, secondPending.AuthorizationID)

	fixture.rejectRefresh.Store(false)
	secondControl := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: secondPending.AuthorizationID}
	completeBrowserAuthorization(ownerCtx, t, built.Service, sess.ID, secondControl, fixture.browserClient(roots))
	secondContinuation, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, secondControl)
	if err != nil || secondContinuation.Status != session.AuthorizationGranted || secondContinuation.Run == nil {
		t.Fatalf("second recheck = %+v, %v", secondContinuation, err)
	}
	drainAndFinish(ownerCtx, t, built.Service, sess.ID, secondContinuation.Run)
	if got := fixture.exchanges.Load(); got != 2 {
		t.Fatalf("authorization exchanges = %d, want 2", got)
	}
	if got := fixture.backendCalls.Load(); got != 2 {
		t.Fatalf("backend calls after exact second continuation = %d, want 2", got)
	}
	assertToolResult(ownerCtx, t, built.Service, sess.ID, call3, false, "protected result")

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

func runAndDrain(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, prompt string) ([]session.Event, *agent.Run) {
	t.Helper()
	run, err := svc.StartInteractiveRunContent(ctx, id, prompt, nil)
	if err != nil {
		t.Fatalf("StartInteractiveRunContent: %v", err)
	}
	events := drainEvents(ctx, t, run)
	svc.FinishRun(id, run)
	return events, run
}

func drainAndFinish(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, run *agent.Run) {
	t.Helper()
	_ = drainEvents(ctx, t, run)
	svc.FinishRun(id, run)
}

func drainEvents(ctx context.Context, t *testing.T, run *agent.Run) []session.Event {
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

func assertDurablePending(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, call session.ToolCallID, authorizationID string) {
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

func completeBrowserAuthorization(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, control server.MCPAuthorizationControl, client *http.Client) {
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
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
}

func assertToolResult(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, call session.ToolCallID, wantError bool, contains string) {
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

type brokerAuthorizationFixture struct {
	t                     *testing.T
	callbackURL           string
	oauth                 *httptest.Server
	mcp                   *httptest.Server
	mcpClient             *http.Client
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
	fixture.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		callback := r.URL.Query().Get("redirect_uri")
		if callback == "" {
			callback = fixture.callbackURL
		}
		http.Redirect(w, r, callback+"?"+query.Encode(), http.StatusFound)
	})
	oauthMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			fixture.refreshes.Add(1)
			if fixture.rejectRefresh.Load() {
				fixture.invalidGrantResponses.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			n := fixture.refreshes.Load()
			_, _ = fmt.Fprintf(w, `{"access_token":"fresh-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":-1}`, n, n)
			return
		}
		exchange := fixture.exchanges.Add(1)
		_, _ = fmt.Fprintf(w, `{"access_token":"stale","refresh_token":"refresh-initial-%d","token_type":"Bearer","expires_in":-1}`, exchange)
	})
	fixture.oauth.Start()
	t.Cleanup(fixture.oauth.Close)
	return fixture
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

type stage3WorkloadIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newStage3WorkloadIssuer(t *testing.T) *stage3WorkloadIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &stage3WorkloadIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "stage3-workload",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	})
	fixture.server = httptest.NewTLSServer(mux)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *stage3WorkloadIssuer) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})
}

func (f *stage3WorkloadIssuer) token(t *testing.T, audience, subject string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": f.server.URL, "sub": subject, "aud": audience,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	token.Header["kid"] = "stage3-workload"
	signed, err := token.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func reserveStage3Address(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
