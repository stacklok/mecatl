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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
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
	_ = runSingletonBrokerStage3RemoteVertical(t)
}

func TestADR_0312_SingletonBrokerConfidentialClientCustody(t *testing.T) {
	evidence := runSingletonBrokerStage3RemoteVertical(t)
	evidence.assertSecretAbsent(t, "vertical-secret")
}

// TestSingletonBrokerRemediation_Scenario5_RestartBoundary persists a real Stage-3
// parked continuation, then replaces the production lifecycle at its exact endpoint.
// The old binding must fail closed without dispatching upstream; a session created after replacement must attach and retry normally.
func TestSingletonBrokerRemediation_Scenario5_RestartBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	identity := newStage3WorkloadIssuer(t)
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificateSource.Close)
	address := reserveStage3Address(t)
	callbackURL := "https://" + address + "/oauth/callback"
	fixture := newBrokerAuthorizationFixture(t, callbackURL)
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	fixture.mcpClient = fixture.clientWithRoots(roots)
	credentialDir := t.TempDir()
	clientSecretFile := filepath.Join(credentialDir, "client-secret")
	if err := os.WriteFile(clientSecretFile, []byte("restart-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
			PublicAddress: address, AdminAddress: "127.0.0.1:0",
			TLSConfig: &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
			WorkloadJWT: mcpbrokerserver.WorkloadJWTConfig{
				Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s",
				AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute,
			},
			ToolHive:        mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{Name: "github", URL: fixture.mcp.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token", ClientID: "restart-client", ClientSecretFile: clientSecretFile, Scopes: []string{"read"}, RequestRefreshToken: true}, Static: []mcpbroker.StaticTool{{Name: "protected", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}}},
			ToolHiveOptions: []mcpbroker.Option{mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, fixture.clientWithRoots(roots))},
			PropagationWait: time.Millisecond, DrainTimeout: time.Second,
		}
	}
	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{CallbackURL: callbackURL, Routes: []permconfig.MCPServerProfile{{Name: "github", URL: fixture.mcp.URL, Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token"}}, Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "restart-client", SecretFile: clientSecretFile}}, Scopes: []string{"read"}, RequestRefreshToken: true}}}}})
	remoteConfig := mcpbrokergrpc.RemoteFactoryConfig{Target: address, CAFile: caFile, ServerName: "example.com", TokenFile: tokenFile, Transport: mcpbrokergrpc.DefaultConfig()}
	remoteFactory := mcpbrokergrpc.NewRemoteFactory(remoteConfig)
	storeDir := t.TempDir()
	restartStoreDir := t.TempDir()
	workspace := t.TempDir()
	build := func(provider *mockllm.Provider, store string) *app.Built {
		built, err := app.Build(ctx, app.Config{Workspace: workspace, StoreDir: store, MockProvider: provider, NoSoul: true, AllowAllTools: true, OwnershipEnforced: true, MCPAuthority: declaration, MCPBrokerFactory: remoteFactory, MCPBrokerFactoryRequired: true})
		if err != nil {
			t.Fatalf("build Stage-3 remote client: %v", err)
		}
		return built
	}
	ownerCtx := session.WithPrincipal(ctx, &session.Principal{Issuer: "https://identity.example", Subject: "alice", GrantType: session.GrantTypeUser})

	oldLifecycle, err := mcpbrokerserver.NewProduction(ctx, productionConfig())
	if err != nil {
		t.Fatalf("construct old production lifecycle: %v", err)
	}
	oldLifecycle.Start()
	oldBuilt := build(mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("parked-call", "mcp__github__protected", json.RawMessage(`{}`)))), storeDir)
	parkedSession, err := oldBuilt.Service.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create parked session: %v", err)
	}
	parkedEvents, parkedRun := runAndDrain(ownerCtx, t, oldBuilt.Service, parkedSession.ID, "park protected call")
	if parkedRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("parked run outcome = %v, want authorization pending", parkedRun.Outcome())
	}
	parked := requiredAuthorization(t, parkedEvents, "parked-call")
	assertDurablePending(ownerCtx, t, oldBuilt.Service, parkedSession.ID, "parked-call", parked.AuthorizationID)
	copyStage3Store(t, storeDir, restartStoreDir)
	oldBuilt.Close()
	if err := oldLifecycle.Close(ctx); err != nil {
		t.Fatalf("close old production lifecycle: %v", err)
	}

	newLifecycle, err := mcpbrokerserver.NewProduction(ctx, productionConfig())
	if err != nil {
		t.Fatalf("construct replacement production lifecycle: %v", err)
	}
	t.Cleanup(func() { _ = newLifecycle.Close(context.Background()) })
	newLifecycle.Start()
	newBuilt := build(mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("fresh-call", "mcp__github__protected", json.RawMessage(`{}`))), mockllm.TextTurn("fresh continuation complete")), restartStoreDir)
	t.Cleanup(newBuilt.Close)

	stale, err := newBuilt.Service.RecheckMCPAuthorization(ownerCtx, parkedSession.ID, server.MCPAuthorizationControl{SessionID: parkedSession.ID, AuthorizationID: parked.AuthorizationID})
	if !errors.Is(err, server.ErrBrokerBindingMismatch) || stale.Run != nil {
		t.Fatalf("stale parked recheck = (%+v, %v), want binding mismatch without continuation", stale, err)
	}
	if got := fixture.backendCalls.Load(); got != 0 {
		t.Fatalf("stale parked continuation dispatched upstream %d times, want 0", got)
	}
	assertDurablePending(ownerCtx, t, newBuilt.Service, parkedSession.ID, "parked-call", parked.AuthorizationID)

	freshSession, err := newBuilt.Service.CreateSession(ownerCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create fresh post-restart session: %v", err)
	}
	freshEvents, freshRun := runAndDrain(ownerCtx, t, newBuilt.Service, freshSession.ID, "retry protected call")
	if freshRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("fresh run outcome = %v, want authorization pending", freshRun.Outcome())
	}
	fresh := requiredAuthorization(t, freshEvents, "fresh-call")
	if fresh.AuthorizationID == parked.AuthorizationID {
		t.Fatal("fresh pre-prompt session reused the parked authorization")
	}
	if got := fixture.backendCalls.Load(); got != 0 {
		t.Fatalf("fresh pre-prompt retry dispatched upstream %d times before authorization, want 0", got)
	}
	freshControl := server.MCPAuthorizationControl{SessionID: freshSession.ID, AuthorizationID: fresh.AuthorizationID}
	completeBrowserAuthorization(ownerCtx, t, newBuilt.Service, freshSession.ID, freshControl, fixture.browserClient(roots))
	continuation, err := newBuilt.Service.RecheckMCPAuthorization(ownerCtx, freshSession.ID, freshControl)
	if err != nil || continuation.Status != session.AuthorizationGranted || continuation.Run == nil {
		t.Fatalf("fresh recheck = (%+v, %v), want granted continuation", continuation, err)
	}
	drainAndFinish(ownerCtx, t, newBuilt.Service, freshSession.ID, continuation.Run)
	assertToolResult(ownerCtx, t, newBuilt.Service, freshSession.ID, "fresh-call", false, "protected result")
	if got := fixture.backendCalls.Load(); got != 1 {
		t.Fatalf("fresh post-restart continuation dispatched upstream %d times, want 1", got)
	}
}

func TestSingletonBrokerRemediation_Scenario5_CallbackCorrelationReplayAndNonDisclosure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	identity := newStage3WorkloadIssuer(t)
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificateSource.Close)
	address := reserveStage3Address(t)
	callbackURL := "https://" + address + "/oauth/callback"
	fixture := newBrokerAuthorizationFixture(t, callbackURL)
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	fixture.mcpClient = fixture.clientWithRoots(roots)
	clientSecretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(clientSecretFile, []byte("callback-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle, err := mcpbrokerserver.NewProduction(ctx, mcpbrokerserver.ProductionConfig{
		PublicAddress: address, AdminAddress: "127.0.0.1:0",
		TLSConfig:   &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
		WorkloadJWT: mcpbrokerserver.WorkloadJWTConfig{Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s", AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute},
		ToolHive: mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{
			Name: "github", URL: fixture.mcp.URL, Auth: "oauth",
			OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token", ClientID: "callback-client", ClientSecretFile: clientSecretFile, Scopes: []string{"read"}},
			Static: []mcpbroker.StaticTool{{Name: "protected", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
		}}},
		ToolHiveOptions: []mcpbroker.Option{
			mcpbroker.WithOAuthLoopbackForTest(t, roots),
			mcpbroker.WithBrokerHTTPClientForTest(t, fixture.clientWithRoots(roots)),
			mcpbroker.WithOAuthLimits(300*time.Millisecond, time.Second),
		},
		PropagationWait: time.Millisecond, DrainTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	lifecycle.Start()
	t.Cleanup(func() { _ = lifecycle.Close(context.Background()) })
	if !lifecycle.Ready(ctx) {
		t.Fatal("production lifecycle was not admitted")
	}

	credentials := t.TempDir()
	caFile := filepath.Join(credentials, "ca.pem")
	tokenFile := filepath.Join(credentials, "token")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateSource.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(identity.token(t, "mecak8s", "mecak8s")), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteFactory := mcpbrokergrpc.NewRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{Target: address, CAFile: caFile, ServerName: "example.com", TokenFile: tokenFile, Transport: mcpbrokergrpc.DefaultConfig()})
	remote, closeRemote, err := remoteFactory(ctx)
	if err != nil {
		t.Fatalf("NewRemoteFactory: %v", err)
	}
	t.Cleanup(func() { _ = closeRemote() })

	requestAuthorization := func(id, callID string) (brokercontract.Attachment, session.ExternalAuthorization, string, string) {
		attachment, _, err := remote.AttachSession(ctx, session.SessionID(id))
		if err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
		if err := attachment.Commit(ctx); err != nil {
			t.Fatalf("commit %s: %v", id, err)
		}
		requester := attachment.Tools()[0].(tool.AuthorizationRequester)
		authorization, required, err := requester.RequestAuthorization(ctx, session.NewToolCall(session.ToolCallID(callID), "mcp__github__protected", json.RawMessage(`{}`)))
		if err != nil || !required {
			t.Fatalf("request authorization %s = (%+v, %v, %v)", id, authorization, required, err)
		}
		presentation, err := attachment.PresentAuthorization(ctx, authorization)
		if err != nil {
			t.Fatalf("present authorization %s: %v", id, err)
		}
		parsed, err := url.Parse(presentation)
		if err != nil || parsed.Query().Get("state") == "" {
			t.Fatalf("presentation %s = %q, %v", id, presentation, err)
		}
		return attachment, authorization, parsed.Query().Get("state"), presentation
	}

	first, firstAuth, firstState, firstPresentation := requestAuthorization("callback-first", "callback-first-call")
	second, secondAuth, secondState, _ := requestAuthorization("callback-second", "callback-second-call")
	if len(firstState) < 43 || firstState == secondState {
		t.Fatalf("states are not unique opaque 256-bit values: %q / %q", firstState, secondState)
	}
	client := fixture.browserClient(roots)
	reject := func(query url.Values) {
		t.Helper()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL+"?"+query.Encode(), nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || string(body) != "invalid OAuth callback\n" {
			t.Fatalf("callback rejection = (%d, %q)", response.StatusCode, body)
		}
		for _, forbidden := range []string{firstState, secondState, "callback-first", "callback-second", "authorization-code"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("callback rejection disclosed %q", forbidden)
			}
		}
	}
	reject(url.Values{})
	reject(url.Values{"code": {"x"}, "state": {"not-a-state"}})
	reject(url.Values{"code": {"authorization-code"}, "state": {firstState}, "session": {"callback-second"}})
	if fixture.exchanges.Load() != 0 {
		t.Fatalf("invalid callbacks consumed token state: exchanges=%d", fixture.exchanges.Load())
	}
	if status, err := first.AuthorizationStatus(ctx, firstAuth); err != nil || status != session.AuthorizationPending {
		t.Fatalf("first state consumed before valid callback: %s, %v", status, err)
	}

	validRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, firstPresentation, nil)
	validResponse, err := client.Do(validRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = validResponse.Body.Close()
	if validResponse.StatusCode != http.StatusOK || fixture.exchanges.Load() != 1 {
		t.Fatalf("valid callback = status:%d exchanges:%d", validResponse.StatusCode, fixture.exchanges.Load())
	}
	reject(url.Values{"code": {"authorization-code"}, "state": {firstState}})
	if fixture.exchanges.Load() != 1 {
		t.Fatalf("replayed state reached exchange %d times", fixture.exchanges.Load())
	}

	time.Sleep(350 * time.Millisecond)
	reject(url.Values{"code": {"authorization-code"}, "state": {secondState}})
	if fixture.exchanges.Load() != 1 {
		t.Fatalf("expired state reached exchange %d times", fixture.exchanges.Load())
	}
	if status, err := second.AuthorizationStatus(ctx, secondAuth); err != nil || status != session.AuthorizationExpired {
		t.Fatalf("expired state status = %s, %v", status, err)
	}

	lifecycle.BeginDrain()
	late, _ := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL, nil)
	lateResponse, err := client.Do(late)
	if err != nil {
		t.Fatal(err)
	}
	_ = lateResponse.Body.Close()
	if lateResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("callback admitted after drain: %d", lateResponse.StatusCode)
	}
}

// must occur in each named proof's direct body, not in a dead nested helper.
func TestInvariant_singleton_broker_named_proofs_use_production_paths(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "mcp_broker_authorization_vertical_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	proofs := []struct {
		name       string
		lifecycles int
	}{
		{"runSingletonBrokerStage3RemoteVertical", 1},
		{"TestSingletonBrokerRemediation_Scenario5_RestartBoundary", 2},
		{"TestSingletonBrokerRemediation_Scenario5_CallbackCorrelationReplayAndNonDisclosure", 1},
	}
	for _, proof := range proofs {
		fn := namedFunction(file, proof.name)
		if fn == nil {
			t.Fatalf("missing named proof %s", proof.name)
		}
		seen := directSelectors(fn.Body)
		if !seen["mcpbrokergrpc.NewRemoteFactory"] {
			t.Errorf("%s does not construct the production remote factory in its own body", proof.name)
		}
		assertStartedProductionLifecycles(t, proof.name, fn.Body, proof.lifecycles)
		for _, forbidden := range []string{"grpc.NewServer", "mcpbrokergrpc.NewServer", "mcpbrokergrpc.RegisterServer"} {
			if seen[forbidden] {
				t.Errorf("%s hand-builds broker transport through %s", proof.name, forbidden)
			}
		}
	}
	if !directSelectors(namedFunction(file, "runSingletonBrokerStage3RemoteVertical").Body)["app.Build"] {
		t.Error("Stage 3 production vertical bypassed app.Build")
	}
}

func assertStartedProductionLifecycles(t *testing.T, proof string, body *ast.BlockStmt, want int) {
	t.Helper()
	constructors := make(map[string]int)
	starts := make(map[string]int)
	terminated := false
	for index, statement := range body.List {
		if _, ok := statement.(*ast.ReturnStmt); ok {
			terminated = true
			continue
		}
		assignment, ok := statement.(*ast.AssignStmt)
		if ok {
			for rhsIndex, expression := range assignment.Rhs {
				if !isSelectorCall(expression, "mcpbrokerserver", "NewProduction") {
					continue
				}
				if terminated || rhsIndex >= len(assignment.Lhs) {
					t.Errorf("%s has an unreachable or unbound NewProduction call", proof)
					continue
				}
				identifier, ok := assignment.Lhs[rhsIndex].(*ast.Ident)
				if !ok || identifier.Name == "_" {
					t.Errorf("%s discards its NewProduction lifecycle", proof)
					continue
				}
				constructors[identifier.Name] = index
			}
		}
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expression.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Start" {
			continue
		}
		if identifier, ok := selector.X.(*ast.Ident); ok {
			starts[identifier.Name] = index
		}
	}
	if len(constructors) != want {
		t.Errorf("%s production lifecycle constructors = %d, want %d", proof, len(constructors), want)
	}
	for lifecycle, constructedAt := range constructors {
		startedAt, ok := starts[lifecycle]
		if !ok || startedAt <= constructedAt {
			t.Errorf("%s lifecycle %s is constructed but not started on the direct live path", proof, lifecycle)
		}
	}
}

func isSelectorCall(expression ast.Expr, packageName, function string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != function {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == packageName
}

func namedFunction(file *ast.File, name string) *ast.FuncDecl {
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func directSelectors(body *ast.BlockStmt) map[string]bool {
	seen := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		if ifStmt, ok := node.(*ast.IfStmt); ok {
			if literal, ok := ifStmt.Cond.(*ast.Ident); ok && literal.Name == "false" {
				return false
			}
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok {
			seen[pkg.Name+"."+selector.Sel.Name] = true
		}
		return true
	})
	return seen
}

type captureDiagnostics struct {
	mu      sync.Mutex
	records []string
}

func (d *captureDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	d.mu.Lock()
	d.records = append(d.records, message+fmt.Sprint(args...))
	d.mu.Unlock()
}

func (d *captureDiagnostics) With(args ...any) port.Diagnostics {
	return &boundCaptureDiagnostics{parent: d, args: append([]any(nil), args...)}
}

type boundCaptureDiagnostics struct {
	parent *captureDiagnostics
	args   []any
}

func (d *boundCaptureDiagnostics) Log(ctx context.Context, level port.Level, message string, args ...any) {
	d.parent.Log(ctx, level, message, append(append([]any(nil), d.args...), args...)...)
}

func (d *boundCaptureDiagnostics) With(args ...any) port.Diagnostics {
	return &boundCaptureDiagnostics{parent: d.parent, args: append(append([]any(nil), d.args...), args...)}
}

func (d *captureDiagnostics) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.records...)
}

type captureMetrics struct {
	mu          sync.Mutex
	projections []any
}

func (m *captureMetrics) Emit(_ context.Context, event session.Event) {
	m.mu.Lock()
	m.projections = append(m.projections, event)
	m.mu.Unlock()
}

func (m *captureMetrics) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	m.mu.Lock()
	m.projections = append(m.projections, []any{id, call, result, queued, took})
	m.mu.Unlock()
}

func (m *captureMetrics) snapshot() []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]any(nil), m.projections...)
}

type stage3Evidence struct {
	fixture     *brokerAuthorizationFixture
	storeDir    string
	diagnostics *captureDiagnostics
	metrics     *captureMetrics
	projections []any
}

func (e *stage3Evidence) assertSecretAbsent(t *testing.T, secret string) {
	t.Helper()
	projections := append([]any(nil), e.projections...)
	projections = append(projections, e.diagnostics.snapshot(), e.metrics.snapshot())
	encoded, err := json.Marshal(projections)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("client secret escaped into protobuf/session/event, diagnostics, metrics, or rendered configuration projection")
	}
	err = filepath.WalkDir(e.storeDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), secret) {
			return fmt.Errorf("secret found in durable record %s", entry.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.fixture.assertHTTPSecretCustody(t, secret)
}

func runSingletonBrokerStage3RemoteVertical(t *testing.T) *stage3Evidence {
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

	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("vertical-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{
		CallbackURL: callbackURL,
		Routes: []permconfig.MCPServerProfile{{
			Name: "github", URL: fixture.mcp.URL,
			Auth: permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
				Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token"}},
				Client:   permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{ID: "vertical-client", SecretFile: secretFile}},
				Scopes:   []string{"read"}, RequestRefreshToken: true,
			}},
		}},
	})
	brokerHTTPClient := fixture.clientWithRoots(roots)
	diagnostics := &captureDiagnostics{}
	metrics := &captureMetrics{}
	lifecycle, err := mcpbrokerserver.NewProduction(ctx, mcpbrokerserver.ProductionConfig{
		PublicAddress: callbackAddress, AdminAddress: "127.0.0.1:0",
		TLSConfig:       &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
		WorkloadJWT:     mcpbrokerserver.WorkloadJWTConfig{Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s", AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute},
		Diagnostics:     diagnostics,
		ToolHive:        mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{Name: "github", URL: fixture.mcp.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token", ClientID: "vertical-client", ClientSecretFile: secretFile, Scopes: []string{"read"}, RequestRefreshToken: true}, Static: []mcpbroker.StaticTool{{Name: "protected", Description: "read protected data", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}}},
		ToolHiveOptions: []mcpbroker.Option{mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, brokerHTTPClient), mcpbroker.WithOAuthLimits(2*time.Minute, 3*time.Second)},
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
	storeDir := t.TempDir()
	built, err := app.Build(ctx, app.Config{
		Workspace: t.TempDir(), StoreDir: storeDir, MockProvider: provider, NoSoul: true,
		AllowAllTools: true, OwnershipEnforced: true, MCPAuthority: declaration,
		Sink: metrics, ToolCallRecorder: metrics,
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
	wireSession, err := server.NewHarnessServer(built.Service).GetSession(ownerCtx, &mecatlv1.GetSessionRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("project protobuf session: %v", err)
	}
	return &stage3Evidence{fixture: fixture, storeDir: storeDir, projections: []any{declaration, firstEvents, secondEvents, finished, wireSession}, diagnostics: diagnostics, metrics: metrics}
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

func copyStage3Store(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if entry.IsDir() && entry.Name() == ".session-leases" {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
	if err != nil {
		t.Fatalf("copy durable Stage-3 store: %v", err)
	}
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
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("callback %s?%s status = %d body=%q", response.Request.URL.Path, response.Request.URL.RawQuery, response.StatusCode, body)
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

type stage3RoundTripFunc func(*http.Request) (*http.Response, error)

func (f stage3RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
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
	requestMu             sync.Mutex
	tokenRequests         []tokenRequestObservation
	nonTokenRequests      []string
}

type tokenRequestObservation struct {
	basicOK  bool
	password string
	body     string
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
		fixture.recordNonTokenRequest(r.URL.String() + " " + r.Header.Get("Authorization"))
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
		fixture.recordNonTokenRequest(r.URL.String() + " " + r.Header.Get("Authorization"))
		query := url.Values{"code": {"fixture-code"}, "state": {r.URL.Query().Get("state")}}
		callback := r.URL.Query().Get("redirect_uri")
		if callback == "" {
			callback = fixture.callbackURL
		}
		http.Redirect(w, r, callback+"?"+query.Encode(), http.StatusFound)
	})
	oauthMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(rawBody)))
		_, password, basicOK := r.BasicAuth()
		fixture.requestMu.Lock()
		fixture.tokenRequests = append(fixture.tokenRequests, tokenRequestObservation{basicOK: basicOK, password: password, body: string(rawBody)})
		fixture.requestMu.Unlock()
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

func (f *brokerAuthorizationFixture) recordOutboundRequest(request *http.Request) {
	if strings.HasSuffix(request.URL.Path, "/token") {
		return // The endpoint records the exact received Basic auth and form body once.
	}
	f.recordNonTokenRequest(request.URL.String() + " " + request.Header.Get("Authorization"))
}

func (f *brokerAuthorizationFixture) recordNonTokenRequest(value string) {
	f.requestMu.Lock()
	f.nonTokenRequests = append(f.nonTokenRequests, value)
	f.requestMu.Unlock()
}

func (f *brokerAuthorizationFixture) assertHTTPSecretCustody(t *testing.T, secret string) {
	t.Helper()
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	if len(f.tokenRequests) == 0 {
		t.Fatal("no OAuth token request observed")
	}
	for i, request := range f.tokenRequests {
		if err := validateTokenSecretCustody(request, secret); err != nil {
			t.Fatalf("token request %d: %v", i, err)
		}
	}
	for _, value := range f.nonTokenRequests {
		if strings.Contains(value, secret) {
			t.Fatalf("client secret escaped token authentication into an observable HTTP surface: %q", value)
		}
	}
}

func validateTokenSecretCustody(request tokenRequestObservation, secret string) error {
	if !request.basicOK || request.password != secret {
		return fmt.Errorf("Basic password does not exactly match expected credential (present=%t, got_length=%d, want_length=%d)", request.basicOK, len(request.password), len(secret))
	}
	form, err := url.ParseQuery(request.body)
	if err != nil {
		return fmt.Errorf("parse token request: %w", err)
	}
	if form.Get("client_secret") != "" {
		return errors.New("client_secret must be absent or empty")
	}
	form.Del("client_secret")
	if strings.Contains(form.Encode(), secret) {
		return errors.New("client secret escaped its token-endpoint authentication field")
	}
	return nil
}

func TestTokenSecretCustodyValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		request tokenRequestObservation
		wantErr bool
	}{
		{name: "exact Basic and absent form secret", request: tokenRequestObservation{basicOK: true, password: "expected", body: "grant_type=authorization_code"}},
		{name: "exact Basic and empty form secret", request: tokenRequestObservation{basicOK: true, password: "expected", body: "client_secret=&grant_type=refresh_token"}},
		{name: "missing Basic", request: tokenRequestObservation{password: "expected", body: "grant_type=authorization_code"}, wantErr: true},
		{name: "empty Basic password", request: tokenRequestObservation{basicOK: true, body: "grant_type=authorization_code"}, wantErr: true},
		{name: "wrong Basic password", request: tokenRequestObservation{basicOK: true, password: "wrong", body: "grant_type=authorization_code"}, wantErr: true},
		{name: "planted form secret", request: tokenRequestObservation{basicOK: true, password: "expected", body: "client_secret=expected&grant_type=authorization_code"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTokenSecretCustody(test.request, "expected")
			if (err != nil) != test.wantErr {
				t.Fatalf("validateTokenSecretCustody() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func (f *brokerAuthorizationFixture) clientWithRoots(roots *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	f.t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: stage3RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		f.recordOutboundRequest(request)
		return transport.RoundTrip(request)
	}), Timeout: 3 * time.Second}
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
	signedToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": f.server.URL, "sub": subject, "aud": audience,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	signedToken.Header["kid"] = "stage3-workload"
	signed, err := signedToken.SignedString(f.key)
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
