package app

import (
	"bytes"
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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/adapters/jsonlstore"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Adapted from f6e2d2327's production vertical: only the canonical SessionAPI
// crosses the host boundary; native OAuth, ToolHive and encrypted custody remain real.
func TestADR_0312_SingletonBrokerConfidentialClientCustody(t *testing.T) {
	evidence := runSingletonBrokerStage3RemoteVertical(t, "")
	evidence.assertSecretAbsent(t, "vertical-secret")
	evidence.assertSecretAbsent(t, "raw-private-canary")
}

func TestBrokerPathRetirement_FollowupProtectedAppBuild(t *testing.T) {
	for _, failure := range []string{"projection", "lost-response"} {
		t.Run(failure, func(t *testing.T) {
			evidence := runSingletonBrokerStage3RemoteVertical(t, failure)
			evidence.assertSecretAbsent(t, "raw-private-canary")
		})
	}
}

func runSingletonBrokerStage3RemoteVertical(t *testing.T, failure string) *stage3Evidence {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	identity := newStage3WorkloadIssuer(t)
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certificateSource.Close)
	address := reserveStage3Address(t)
	callbackURL := "https://" + address + "/oauth/callback"
	fixture := newBrokerAuthorizationFixture(t, callbackURL)
	fixture.loseQueryResponse = failure == "lost-response"
	roots := x509.NewCertPool()
	roots.AddCert(certificateSource.Certificate())
	credentialDir := t.TempDir()
	secretFile := filepath.Join(credentialDir, "client-secret")
	keyFile := filepath.Join(credentialDir, "kek")
	caFile := filepath.Join(credentialDir, "ca.pem")
	tokenFile := filepath.Join(credentialDir, "workload-token")
	for path, contents := range map[string][]byte{
		secretFile: []byte("vertical-secret\n"), keyFile: bytes.Repeat([]byte{0x42}, 32),
		caFile:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateSource.Certificate().Raw}),
		tokenFile: []byte(identity.token(t, "mecak8s", "mecak8s") + "\n"),
	} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := miniredis.RunT(t)
	diagnostics := &captureDiagnostics{}
	metrics := &captureMetrics{}
	cfg := mcpbrokerserver.ProductionConfig{
		PublicAddress: address, AdminAddress: "127.0.0.1:0",
		TLSConfig:   &tls.Config{Certificates: certificateSource.TLS.Certificates, MinVersion: tls.VersionTLS12},
		SessionAPI:  &mcpbrokerserver.SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-app-vertical"},
		WorkloadJWT: mcpbrokerserver.WorkloadJWTConfig{Issuer: identity.server.URL, JWKSURI: identity.server.URL + "/keys", Audience: "mecak8s", AllowedSubjects: []string{"mecak8s"}, TrustedCAPEM: identity.caPEM(), MaxJWKSStaleness: time.Minute},
		Diagnostics: diagnostics,
		ToolHive: mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: []mcpbroker.ToolHiveProfile{{Name: "github", URL: fixture.mcp.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: fixture.oauth.URL + "/authorize", TokenEndpoint: fixture.oauth.URL + "/token", ClientID: "vertical-client", ClientSecretFile: secretFile, Scopes: []string{"read"}, RequestRefreshToken: true}, Static: []mcpbroker.StaticTool{{Name: "protected", Description: "read protected data", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}}},
			ProtectedStorage: &mcpbroker.ProtectedStorageConfig{
				Redis: mcpbroker.ProtectedRedisConfig{Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
					return redis.NewClient(&redis.Options{Addr: db.Addr()}), nil
				}, ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second},
				Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: "active", Keys: []mcpbroker.ProtectedEncryptionKey{{ID: "active", File: keyFile}}},
			},
		},
		ToolHiveOptions: []mcpbroker.Option{mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, fixture.clientWithRoots(roots))},
		PropagationWait: time.Millisecond, DrainTimeout: time.Second,
	}
	lifecycle, err := mcpbrokerserver.NewProduction(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lifecycle.Close(context.Background()) })
	lifecycle.Start()
	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{})
	queryArgs := json.RawMessage(`{"server":"github","tool":"protected","args":{"request":"exact-query"},"jq_filter":".keep"}`)
	if failure == "projection" {
		queryArgs = json.RawMessage(`{"server":"github","tool":"protected","args":{"request":"exact-query"},"jq_filter":"error(.raw)"}`)
	}
	var modelMu sync.Mutex
	var modelRequests []json.RawMessage
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Error(err)
			return
		}
		modelMu.Lock()
		modelRequests = append(modelRequests, encoded)
		modelMu.Unlock()
	})},
		mockllm.ToolCallTurn(session.NewToolCall("denied", "Write", json.RawMessage(`{"path":"denied.txt","content":"must not be written"}`)), session.NewToolCall("direct", "mcp__github__protected", json.RawMessage(`{"request":"first"}`))), mockllm.TextTurn("direct complete"),
		mockllm.ToolCallTurn(session.NewToolCall("query", "CallMcpWithQuery", queryArgs)), mockllm.TextTurn("query complete"),
		mockllm.ToolCallTurn(session.NewToolCall("query", "CallMcpWithQuery", queryArgs)), mockllm.TextTurn("retry refused"),
	)
	storeDir := t.TempDir()
	workspace := t.TempDir()
	settings := writeOperatorSettingsFile(t, "permissions:\n  deny: [Write]\n  ask: [CallMcpWithQuery]\n")
	built, err := buildIsolated(t, ctx, Config{Workspace: workspace, StoreDir: storeDir, UserModelDir: t.TempDir(), NoSoul: true, NoUserModel: true, PermissionConfigs: []string{settings},
		MockProvider: provider, AllowAllTools: true, OwnershipEnforced: true, MCPAuthority: declaration, Diagnostics: diagnostics, Sink: metrics, ToolCallRecorder: metrics,
		SessionBrokerFactory: mcpbrokergrpc.NewSessionRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{Target: lifecycle.PublicAddress(), CAFile: caFile, ServerName: "example.com", TokenFile: tokenFile, Transport: mcpbrokergrpc.DefaultConfig()}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	owner := &session.Principal{Issuer: "https://identity.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ownerCtx := session.WithPrincipal(ctx, owner)
	intruderCtx := session.WithPrincipal(ctx, &session.Principal{Issuer: owner.Issuer, Subject: "mallory", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ownerCtx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	before, ok := sess.BrokerAccess()
	if !ok || len(before.BrokerTools) != 0 {
		t.Fatal("fresh SessionAPI session published tools before enrollment")
	}
	if _, err := built.Service.ConnectWorkspaceServices(intruderCtx, sess.ID); err == nil {
		t.Fatal("non-owner enrolled")
	}
	begun, err := built.Service.ConnectWorkspaceServices(ownerCtx, sess.ID)
	if err != nil || begun.URL == "" {
		t.Fatalf("enrollment: %+v %v", begun, err)
	}
	visitStage3URL(t, ownerCtx, fixture.clientWithRoots(roots), begun.URL)
	connected, err := built.Service.ConnectWorkspaceServices(ownerCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := built.Service.GetSession(ownerCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	adopted, ok := saved.BrokerAccess()
	if !ok || adopted.Session != before.Session || adopted.Catalogue == before.Catalogue || !saved.Authority.CapabilitySet.AllowsTool("mcp__github__protected") || !saved.Authority.CapabilitySet.AllowsTool("Read") {
		t.Fatal("enrollment did not durably adopt catalogue and preserve independent authority")
	}
	if fixture.backendCalls.Load() != 0 {
		t.Fatal("enrollment executed tool")
	}
	firstEvents, firstRun := runAndDrain(ownerCtx, t, built.Service, sess.ID, "read protected data")
	if firstRun.Outcome() != agent.RunOutcomeCompleted {
		t.Fatalf("direct outcome: %v", firstRun.Outcome())
	}
	assertToolResult(ownerCtx, t, built.Service, sess.ID, "denied", true, "permission denied:")
	if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configured denial allowed a write")
	}
	assertToolResult(ownerCtx, t, built.Service, sess.ID, "direct", false, "protected result")
	if fixture.backendCalls.Load() != 1 {
		t.Fatal("direct tool did not execute once")
	}
	fixture.rejectRefresh.Store(true)
	parkedEvents, parkedRun := runAndDrain(ownerCtx, t, built.Service, sess.ID, "read after credential regression with projection")
	if parkedRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("regression outcome=%v", parkedRun.Outcome())
	}
	if fixture.invalidGrantResponses.Load() == 0 || fixture.backendCalls.Load() != 1 {
		t.Fatal("refresh failure did not revoke before query dispatch")
	}
	pending := requiredAuthorization(t, parkedEvents, "query")
	parked, err := built.Service.GetSession(ownerCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	durable, ok := parked.PendingAuthorization()
	if !ok || durable.Call.ID != "query" || durable.Call.Name != "CallMcpWithQuery" || !bytes.Equal(durable.Call.Args, queryArgs) || durable.Authorization.ID != pending.AuthorizationID {
		t.Fatal("durable exact outer query was not parked")
	}
	control := server.MCPAuthorizationControl{SessionID: sess.ID, AuthorizationID: pending.AuthorizationID}
	if _, err := built.Service.MCPAuthorizationPresentation(intruderCtx, sess.ID, control); err == nil {
		t.Fatal("non-owner obtained authorization presentation")
	}
	presentation, err := built.Service.MCPAuthorizationPresentation(ownerCtx, sess.ID, control)
	if err != nil {
		t.Fatal(err)
	}
	fixture.requestMu.Lock()
	fixture.beforeQuery = func() error {
		durable, err := built.Service.GetSession(ownerCtx, sess.ID)
		if err != nil {
			return err
		}
		access, ok := durable.BrokerAccess()
		exact := session.NewToolCall("query", "CallMcpWithQuery", queryArgs)
		if !ok || access.Session != adopted.Session || access.Catalogue == adopted.Catalogue || access.Withdrawn || access.Current == nil || access.Current.CallID != exact.ID || access.Current.Digest != session.BrokerCallDigest(exact) {
			return errors.New("native query reached upstream before exact durable catalogue adoption and dispatch fence")
		}
		return nil
	}
	fixture.requestMu.Unlock()
	fixture.rejectRefresh.Store(false)
	visitStage3URL(t, ownerCtx, fixture.clientWithRoots(roots), presentation)
	continuation, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, control)
	if err != nil || continuation.Status != session.AuthorizationGranted || continuation.Run == nil {
		t.Fatalf("recheck: %+v %v", continuation, err)
	}
	resumedEvents := drainEvents(ownerCtx, t, continuation.Run)
	built.Service.FinishRun(sess.ID, continuation.Run)
	asked := false
	for _, event := range append(append([]session.Event(nil), parkedEvents...), resumedEvents...) {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			asked = true
		}
	}
	if !asked {
		t.Fatal("configured query Ask was bypassed by enrollment or exact resume")
	}
	if failure == "" {
		assertToolResult(ownerCtx, t, built.Service, sess.ID, "query", false, `"projected"`)
	} else {
		assertToolResult(ownerCtx, t, built.Service, sess.ID, "query", true, "")
		uncertain, err := built.Service.GetSession(ownerCtx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		access, ok := uncertain.BrokerAccess()
		if !ok || access.Current == nil || !access.Current.Attempt.Valid() {
			t.Fatalf("synthetic paired result released unknown fence: %+v", access)
		}
		runAndDrain(ownerCtx, t, built.Service, sess.ID, "retry identical query")
		if fixture.backendCalls.Load() != 2 {
			t.Fatal("same provider ID/arguments executed new occurrence after uncertain effect")
		}
		store, err := jsonlstore.New(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := store.Load(ownerCtx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		restoredAccess, ok := restored.BrokerAccess()
		if !ok || restoredAccess.Current == nil || restoredAccess.Current.Attempt != access.Current.Attempt {
			t.Fatalf("restore lost unknown fence: %+v", restoredAccess)
		}
		if _, err := restored.PrepareBrokerInvocation(restoredAccess.Session, restoredAccess.Catalogue, session.NewToolCall("query", "CallMcpWithQuery", queryArgs), time.Now()); err == nil {
			t.Fatal("restore rearmed unknown effect")
		}
	}
	if fixture.backendCalls.Load() != 2 {
		t.Fatal("exact query resume did not execute once")
	}
	if again, err := built.Service.RecheckMCPAuthorization(ownerCtx, sess.ID, control); err == nil && again.Run != nil {
		t.Fatal("settled authorization resumed twice")
	}
	if fixture.backendCalls.Load() != 2 {
		t.Fatal("recheck redispatched query")
	}
	fixture.requestMu.Lock()
	exact := fixture.lastArguments["request"] == "exact-query"
	fixture.requestMu.Unlock()
	if !exact {
		t.Fatal("resume replaced original target arguments")
	}
	finished, err := built.Service.GetSession(ownerCtx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ValidateToolPairing(finished.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
	wire, err := server.NewHarnessServer(built.Service).GetSession(ownerCtx, &mecatlv1.GetSessionRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatal(err)
	}
	modelMu.Lock()
	defer modelMu.Unlock()
	if failure == "" && (len(modelRequests) != 4 || !bytes.Contains(modelRequests[len(modelRequests)-1], []byte("projected"))) {
		t.Fatal("projected result did not reach the real model request")
	}
	if fixture.exchanges.Load() != 2 {
		t.Fatal("initial enrollment and reauthorization did not both exchange credentials")
	}
	if len(db.Keys()) == 0 || strings.Contains(db.Dump(), "refresh-initial-") || strings.Contains(db.Dump(), "fresh-") {
		t.Fatal("native storage did not retain encrypted credentials")
	}
	permissionConfigInput, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	return &stage3Evidence{fixture: fixture, storeDir: storeDir, diagnostics: diagnostics, metrics: metrics, projections: []any{string(permissionConfigInput), declaration, cfg.ToolHive.Profiles, db.Dump(), connected, begun, firstEvents, parkedEvents, resumedEvents, finished, wire, modelRequests}}
}

func runAndDrain(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, prompt string) ([]session.Event, *agent.Run) {
	t.Helper()
	run, err := svc.StartInteractiveRunContent(ctx, id, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(ctx, t, run)
	svc.FinishRun(id, run)
	return events, run
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
			if event.Type == session.EvPermissionAsk && event.Ask != nil {
				run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
			}
		case <-ctx.Done():
			run.Cancel()
			t.Fatal(ctx.Err())
		}
	}
}
func requiredAuthorization(t *testing.T, events []session.Event, call session.ToolCallID) session.AuthorizationPayload {
	t.Helper()
	for _, event := range events {
		if event.Type == session.EvAuthorizationRequired && event.Authorization != nil && event.Authorization.Call == call {
			return *event.Authorization
		}
	}
	t.Fatalf("no authorization.required for %s", call)
	return session.AuthorizationPayload{}
}
func assertToolResult(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, call session.ToolCallID, wantError bool, contains string) {
	t.Helper()
	loaded, err := svc.GetSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range loaded.Conversation.Messages {
		if message.ToolResult != nil && message.ToolResult.CallID == call {
			count++
			if message.ToolResult.IsError != wantError || !strings.Contains(message.ToolResult.Content, contains) {
				t.Fatalf("unexpected result for %s: %+v", call, message.ToolResult)
			}
		}
	}
	if count != 1 {
		t.Fatalf("result count for %s=%d", call, count)
	}
}
func visitStage3URL(t *testing.T, ctx context.Context, client *http.Client, address string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("browser status=%d", response.StatusCode)
	}
}

type captureDiagnostics struct {
	mu      sync.Mutex
	records []string
}

func (d *captureDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, message+fmt.Sprint(args...))
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
	defer m.mu.Unlock()
	m.projections = append(m.projections, event)
}
func (m *captureMetrics) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projections = append(m.projections, []any{id, call, result, queued, took})
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

func secretInProjection(encoded []byte, secret string) bool {
	if bytes.Contains(encoded, []byte(secret)) {
		return true
	}
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch value := value.(type) {
		case string:
			if strings.Contains(value, secret) {
				return true
			}
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				decoded, err := encoding.DecodeString(value)
				if err == nil && bytes.Contains(decoded, []byte(secret)) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if contains(child) {
					return true
				}
			}
		case map[string]any:
			for _, child := range value {
				if contains(child) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}

func (e *stage3Evidence) secretAbsent(secret string) error {
	projections := append([]any(nil), e.projections...)
	projections = append(projections, e.diagnostics.snapshot(), e.metrics.snapshot())
	encoded, err := json.Marshal(projections)
	if err != nil {
		return err
	}
	if secretInProjection(encoded, secret) {
		return errors.New("secret escaped public/session/event/configuration/diagnostic/metric projections")
	}
	if err := filepath.WalkDir(e.storeDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if secretInProjection(contents, secret) {
			return errors.New("secret escaped durable snapshot")
		}
		return nil
	}); err != nil {
		return err
	}
	return e.fixture.httpSecretCustody(secret)
}
func (e *stage3Evidence) assertSecretAbsent(t *testing.T, secret string) {
	t.Helper()
	if err := e.secretAbsent(secret); err != nil {
		t.Fatal(err)
	}
}

func TestSessionBrokerConfidentialCustodyOracle(t *testing.T) {
	const secret = "planted-fixture-secret"
	for _, surface := range []string{"public-proto", "public-proto-bytes", "events", "event-bytes", "model-output", "config", "logs", "metrics", "snapshot", "snapshot-bytes", "non-token-http"} {
		t.Run(surface, func(t *testing.T) {
			e := &stage3Evidence{fixture: &brokerAuthorizationFixture{clientSecret: secret, tokenRequests: []tokenRequestObservation{{basicOK: true, password: secret, body: "grant_type=authorization_code"}}}, storeDir: t.TempDir(), diagnostics: &captureDiagnostics{}, metrics: &captureMetrics{}}
			e.assertSecretAbsent(t, secret)
			switch surface {
			case "public-proto":
				e.projections = []any{&mecatlv1.GetSessionRequest{SessionId: secret}}
			case "public-proto-bytes":
				e.projections = []any{&mecatlv1.ContentBlock{Data: []byte(secret)}}
			case "event-bytes":
				e.projections = []any{session.Event{Type: session.EvToolResult, ToolResult: &session.ToolResult{CallID: "planted", Parts: []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte(secret)}}}}}
			case "model-output":
				e.projections = []any{[]json.RawMessage{json.RawMessage(`{"content":"` + secret + `"}`)}}
			case "events":
				e.projections = []any{session.NewToolCall("planted", secret, []byte(`{}`))}
			case "config":
				e.projections = []any{map[string]string{"rendered": secret}}
			case "logs":
				e.diagnostics.Log(t.Context(), port.LevelInfo, secret)
			case "metrics":
				e.metrics.projections = []any{secret}
			case "snapshot":
				if err := os.WriteFile(filepath.Join(e.storeDir, "snapshot"), []byte(secret), 0o600); err != nil {
					t.Fatal(err)
				}
			case "snapshot-bytes":
				encoded, err := json.Marshal(session.Conversation{Messages: []session.Message{session.NewToolMessage(session.ToolResult{CallID: "planted", Parts: []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte(secret)}}})}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.storeDir, "snapshot.json"), encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			case "non-token-http":
				e.fixture.recordNonTokenRequest(secret)
			}
			if err := e.secretAbsent(secret); err == nil {
				t.Fatal("positive plant escaped custody oracle")
			}
		})
	}
	for _, request := range []tokenRequestObservation{
		{password: secret, body: "grant_type=authorization_code"},
		{basicOK: true, password: "wrong", body: "grant_type=authorization_code"},
		{basicOK: true, password: secret, body: "client_secret=" + secret},
		{basicOK: true, password: secret, body: "code=" + secret},
	} {
		if validateTokenSecretCustody(request, secret) == nil {
			t.Fatal("negative token oracle accepted invalid custody")
		}
	}
	if err := validateTokenSecretCustody(tokenRequestObservation{basicOK: true, password: secret, body: "grant_type=authorization_code"}, secret); err != nil {
		t.Fatal(err)
	}
}

type stage3RoundTripFunc func(*http.Request) (*http.Response, error)

func (f stage3RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokerAuthorizationFixture struct {
	t                                                         *testing.T
	callbackURL                                               string
	clientSecret                                              string
	oauth, mcp                                                *httptest.Server
	backendCalls, exchanges, refreshes, invalidGrantResponses atomic.Int32
	rejectRefresh                                             atomic.Bool
	requestMu                                                 sync.Mutex
	tokenRequests                                             []tokenRequestObservation
	nonTokenRequests                                          []string
	lastArguments                                             map[string]any
	beforeQuery                                               func() error
	loseQueryResponse                                         bool
}
type tokenRequestObservation struct {
	basicOK        bool
	password, body string
}

func newBrokerAuthorizationFixture(t *testing.T, callbackURL string) *brokerAuthorizationFixture {
	t.Helper()
	fixture := &brokerAuthorizationFixture{t: t, callbackURL: callbackURL, clientSecret: "vertical-secret"}
	protected := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected-fixture", Version: "1"}, nil)
	protected.AddTool(&mcpsdk.Tool{Name: "protected", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		fixture.backendCalls.Add(1)
		fixture.requestMu.Lock()
		fixture.lastArguments = args
		check := fixture.beforeQuery
		fixture.requestMu.Unlock()
		if args["request"] == "exact-query" && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		result := &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "protected result"}}}
		if args["request"] == "exact-query" {
			result.StructuredContent = map[string]any{"keep": "projected", "raw": "raw-private-canary"}
		}
		return result, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return protected }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fixture.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed, err := httputil.DumpRequest(r, true)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fixture.recordNonTokenRequest(string(observed))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fresh-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if fixture.loseQueryResponse && bytes.Contains(observed, []byte("exact-query")) {
			handler.ServeHTTP(httptest.NewRecorder(), r)
			http.Error(w, "response lost after execution", http.StatusBadGateway)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.mcp.Close)
	mux := http.NewServeMux()
	fixture.oauth = httptest.NewUnstartedServer(mux)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		fixture.recordNonTokenRequest(r.URL.String() + " " + r.Header.Get("Authorization"))
		query := url.Values{"code": {"fixture-code"}, "state": {r.URL.Query().Get("state")}}
		callback := r.URL.Query().Get("redirect_uri")
		if callback == "" {
			callback = fixture.callbackURL
		}
		http.Redirect(w, r, callback+"?"+query.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		_, password, basicOK := r.BasicAuth()
		fixture.requestMu.Lock()
		fixture.tokenRequests = append(fixture.tokenRequests, tokenRequestObservation{basicOK: basicOK, password: password, body: string(body)})
		fixture.requestMu.Unlock()
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			n := fixture.refreshes.Add(1)
			if fixture.rejectRefresh.Load() {
				fixture.invalidGrantResponses.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"access_token":"fresh-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":-1}`, n, n)
			return
		}
		n := fixture.exchanges.Add(1)
		_, _ = fmt.Fprintf(w, `{"access_token":"stale","refresh_token":"refresh-initial-%d","token_type":"Bearer","expires_in":-1}`, n)
	})
	fixture.oauth.Start()
	t.Cleanup(fixture.oauth.Close)
	return fixture
}
func (f *brokerAuthorizationFixture) recordNonTokenRequest(value string) {
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	f.nonTokenRequests = append(f.nonTokenRequests, value)
}
func (f *brokerAuthorizationFixture) httpSecretCustody(secret string) error {
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	if len(f.tokenRequests) == 0 {
		return errors.New("no OAuth token request observed")
	}
	for _, request := range f.tokenRequests {
		if err := validateTokenSecretCustody(request, f.clientSecret); err != nil {
			return err
		}
	}
	for _, value := range f.nonTokenRequests {
		if strings.Contains(value, secret) {
			return errors.New("secret escaped into non-token HTTP")
		}
	}
	return nil
}
func validateTokenSecretCustody(request tokenRequestObservation, secret string) error {
	if !request.basicOK || request.password != secret {
		return errors.New("token Basic authentication does not exactly match fixture credential")
	}
	form, err := url.ParseQuery(request.body)
	if err != nil {
		return err
	}
	if form.Get("client_secret") != "" {
		return errors.New("client_secret must be absent or empty")
	}
	form.Del("client_secret")
	if strings.Contains(form.Encode(), secret) {
		return errors.New("secret escaped token authentication field")
	}
	return nil
}
func (f *brokerAuthorizationFixture) clientWithRoots(roots *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	f.t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: stage3RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		ip := net.ParseIP(request.URL.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("offline fixture rejected non-loopback HTTP")
		}
		if !strings.HasSuffix(request.URL.Path, "/token") {
			observed, err := httputil.DumpRequest(request, true)
			if err != nil {
				return nil, err
			}
			f.recordNonTokenRequest(string(observed))
		}
		return transport.RoundTrip(request)
	}), Timeout: 5 * time.Second}
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
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "stage3-workload", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})}}})
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}
func (f *stage3WorkloadIssuer) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})
}
func (f *stage3WorkloadIssuer) token(t *testing.T, audience, subject string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": f.server.URL, "sub": subject, "aud": audience, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix()})
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
