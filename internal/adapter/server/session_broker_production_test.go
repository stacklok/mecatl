package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type sessionTLSRedis struct {
	redis.UniversalClient
	closes  atomic.Int32
	unready atomic.Bool
}

func (r *sessionTLSRedis) Close() error {
	r.closes.Add(1)
	return r.UniversalClient.Close()
}
func (r *sessionTLSRedis) Ping(ctx context.Context) *redis.StatusCmd {
	if r.unready.Load() {
		cmd := redis.NewStatusCmd(ctx)
		cmd.SetErr(context.DeadlineExceeded)
		return cmd
	}
	return r.UniversalClient.Ping(ctx)
}

// Reuses the donor's OAuth and MCP fixtures, but never substitutes execution or
// authentication. Both RPC and browser callbacks reach the production TLS listener.
func TestSessionBrokerProductionTLSNativeProfiles(t *testing.T) {
	for _, topology := range []string{"anonymous", "protected", "mixed", "mixed-discovery-failure"} {
		t.Run(topology, func(t *testing.T) {
			issuer := newMultiUpstreamOIDC(t, "private")
			private := newMultiUpstreamMCP(t, "private")
			anonymous := newMultiUpstreamMCP(t, "public")
			if topology == "mixed-discovery-failure" {
				anonymous.reject = true
			}
			jwks := httptest.NewTLSServer(http.HandlerFunc(issuer.serveHTTP))
			t.Cleanup(jwks.Close)
			var proxyRoute atomic.Pointer[httputil.ReverseProxy]
			gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if proxy := proxyRoute.Load(); proxy != nil {
					proxy.ServeHTTP(w, r)
					return
				}
				http.Error(w, "not started", http.StatusServiceUnavailable)
			}))
			t.Cleanup(gateway.Close)
			roots := x509.NewCertPool()
			roots.AddCert(gateway.Certificate())
			store := miniredis.RunT(t)
			var clients []*sessionTLSRedis
			r := mcpbroker.ProtectedRedisConfig{
				Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
					client := &sessionTLSRedis{UniversalClient: redis.NewClient(&redis.Options{Addr: store.Addr()})}
					clients = append(clients, client)
					return client, nil
				}, ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second,
			}
			cfg := mcpbrokerserver.ProductionConfig{
				PublicAddress: "127.0.0.1:0", AdminAddress: "127.0.0.1:0", DrainTimeout: time.Second,
				TLSConfig:  &tls.Config{Certificates: gateway.TLS.Certificates, MinVersion: tls.VersionTLS12},
				SessionAPI: &mcpbrokerserver.SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-native"},
				WorkloadJWT: mcpbrokerserver.WorkloadJWTConfig{
					Issuer: jwks.URL, JWKSURI: jwks.URL + "/jwks", Audience: "broker-session-proof", AllowedSubjects: []string{"verified-host"},
					TrustedCAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: jwks.Certificate().Raw}), MaxJWKSStaleness: time.Minute,
				},
				ToolHiveOptions: []mcpbroker.Option{mcpbroker.WithOAuthLoopbackForTest(t, roots), mcpbroker.WithBrokerHTTPClientForTest(t, gateway.Client())},
				ToolHive:        mcpbroker.ToolHiveConfig{CallbackURL: gateway.URL + "/callback"},
			}
			if topology != "anonymous" {
				key := filepath.Join(t.TempDir(), "kek")
				if err := os.WriteFile(key, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg.ToolHive.ProtectedStorage = &mcpbroker.ProtectedStorageConfig{Redis: r, Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: "active", Keys: []mcpbroker.ProtectedEncryptionKey{{ID: "active", File: key}}}}
				cfg.ToolHive.Profiles = append(cfg.ToolHive.Profiles, mcpbroker.ToolHiveProfile{Name: "private", URL: private.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: issuer.server.URL, ClientID: issuer.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}})
			} else {
				cfg.SessionMetadataRedis = &r
			}
			if topology != "protected" {
				cfg.ToolHive.Profiles = append(cfg.ToolHive.Profiles, mcpbroker.ToolHiveProfile{Name: "public", URL: anonymous.server.URL, Auth: "none"})
			}
			lifecycle, err := mcpbrokerserver.NewProduction(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			lifecycle.Start()
			t.Cleanup(func() { _ = lifecycle.Close(context.Background()) })
			target, err := url.Parse("https://" + lifecycle.PublicAddress())
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.Transport = gateway.Client().Transport
			proxyRoute.Store(proxy)
			client, err := mcpbrokergrpc.NewSessionClient(lifecycle.PublicAddress(), 5*time.Second, 5*time.Second, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12})))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if _, err := client.OpenSession(t.Context(), nil); err == nil {
				t.Fatal("unauthenticated request admitted")
			}
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": jwks.URL, "sub": "verified-host", "aud": "broker-session-proof", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()})
			token.Header["kid"] = issuer.name + "-key"
			signed, err := token.SignedString(issuer.key)
			if err != nil {
				t.Fatal(err)
			}
			ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+signed)
			opened, err := client.OpenSession(ctx, nil)
			if err != nil || len(opened.Catalogue.Tools()) != 0 {
				t.Fatalf("open: %+v %v", opened, err)
			}
			if headers, _ := anonymous.snapshot(); len(headers) != 0 {
				t.Fatal("Open performed anonymous discovery")
			}
			begin, err := client.BeginEnrollment(ctx, opened.Ref)
			if err != nil {
				t.Fatal(err)
			}
			cat := begin.Catalogue
			if topology != "anonymous" {
				if begin.Kind != c.EnrollmentStartedKind {
					t.Fatalf("protected begin: %+v", begin)
				}
				browser := gateway.Client()
				browser.Timeout = 8 * time.Second
				response, err := browser.Get(begin.Started.Prompt.URL)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("OAuth callback status=%d", response.StatusCode)
				}
				flow, err := client.ObserveEnrollment(ctx, opened.Ref, begin.Started.Ref)
				if topology == "mixed-discovery-failure" {
					if err == nil && flow.Kind == c.FlowCompleted {
						t.Fatal("partial mixed discovery completed")
					}
					reopened, err := client.OpenSession(ctx, &opened.Ref)
					if err != nil || len(reopened.Catalogue.Tools()) != 0 {
						t.Fatalf("partial mixed publication: %+v %v", reopened, err)
					}
				} else {
					if err != nil || flow.Kind != c.FlowCompleted {
						t.Fatalf("observe: %+v %v", flow, err)
					}
					cat = flow.Catalogue
				}
			}
			if topology != "mixed-discovery-failure" {
				if len(cat.Tools()) != len(cfg.ToolHive.Profiles)+1 {
					t.Fatalf("incomplete catalogue: %v", cat.ToolNames())
				}
				for index, candidate := range cat.Tools() {
					if candidate.Spec().Name == "CallMcpWithQuery" {
						continue // Query arguments and projection have dedicated boundary proofs.
					}
					call := c.Call{ID: session.ToolCallID(cat.ToolNames()[index]), Name: candidate.Spec().Name, Arguments: []byte(`{}`)}
					check, err := client.CheckAuthorization(ctx, opened.Ref, cat.Ref(), &call, "")
					if err != nil || !check.Ready || check.Authorization != "" {
						t.Fatalf("native preflight: %+v %v", check, err)
					}
					_, privateCalls := private.snapshot()
					_, publicCalls := anonymous.snapshot()
					if privateCalls+publicCalls != index {
						t.Fatal("preflight executed a tool")
					}
					out, err := client.InvokeTool(ctx, opened.Ref, cat.Ref(), call)
					if err != nil || out.Kind != c.InvocationCompleted || out.Result.IsError {
						t.Fatalf("native invocation: %+v %v", out, err)
					}
				}
				if topology != "anonymous" {
					headers, calls := private.snapshot()
					if calls != 1 || len(headers) == 0 {
						t.Fatalf("native private calls=%d headers=%d", calls, len(headers))
					}
					for _, header := range headers {
						if header != "Bearer private-token" {
							t.Fatal("native credential injection missing")
						}
					}
				}
			}
			if topology == "protected" {
				t.Run("native-expiry-refresh-success", func(t *testing.T) {
					issuer.mu.Lock()
					issuer.tokenTTL = 1
					issuer.mu.Unlock()
					defer func() { issuer.mu.Lock(); issuer.tokenTTL = 0; issuer.mu.Unlock() }()
					opened, err := client.OpenSession(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					begin, err := client.BeginEnrollment(ctx, opened.Ref)
					if err != nil || begin.Kind != c.EnrollmentStartedKind {
						t.Fatalf("enroll: %+v %v", begin, err)
					}
					response, err := gateway.Client().Get(begin.Started.Prompt.URL)
					if err != nil {
						t.Fatal(err)
					}
					_ = response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Fatalf("enroll callback: %d", response.StatusCode)
					}
					flow, err := client.ObserveEnrollment(ctx, opened.Ref, begin.Started.Ref)
					if err != nil || flow.Kind != c.FlowCompleted {
						t.Fatalf("observe: %+v %v", flow, err)
					}
					initial, refreshes := issuer.counts()
					headers, before := private.snapshot()
					time.Sleep(1100 * time.Millisecond)
					call := c.Call{ID: "refreshed", Name: "mcp__private__whoami", Arguments: []byte(`{}`)}
					check, err := client.CheckAuthorization(ctx, opened.Ref, flow.Catalogue.Ref(), &call, "")
					if err != nil || !check.Ready || check.Authorization != "" {
						t.Fatalf("refreshed preflight: %+v %v", check, err)
					}
					afterInitial, afterRefreshes := issuer.counts()
					if afterInitial != initial || afterRefreshes-refreshes != 1 {
						t.Fatalf("preflight initial/refresh delta=%d/%d", afterInitial-initial, afterRefreshes-refreshes)
					}
					afterHeaders, after := private.snapshot()
					if after != before || len(afterHeaders) != len(headers) {
						t.Fatal("refresh preflight executed upstream MCP I/O")
					}
					reopened, err := client.OpenSession(ctx, &opened.Ref)
					if err != nil || reopened.Catalogue.Ref() != flow.Catalogue.Ref() {
						t.Fatalf("refresh changed catalogue: %+v %v", reopened, err)
					}
					out, err := client.InvokeTool(ctx, opened.Ref, flow.Catalogue.Ref(), call)
					if err != nil || out.Kind != c.InvocationCompleted || out.Result.IsError {
						t.Fatalf("refreshed invocation: %+v %v", out, err)
					}
					afterHeaders, after = private.snapshot()
					if after-before != 1 || len(afterHeaders) == len(headers) {
						t.Fatalf("refreshed invocation calls delta=%d", after-before)
					}
					for _, header := range afterHeaders[len(headers):] {
						if header != "Bearer private-refreshed" {
							t.Fatal("refreshed credential injection missing")
						}
					}
					afterInitial, afterRefreshes = issuer.counts()
					if afterInitial != initial || afterRefreshes-refreshes != 1 {
						t.Fatal("Invoke started another enrollment or refresh")
					}
				})
				for _, mode := range []string{"permission-deny", "lost-reply"} {
					t.Run(mode, func(t *testing.T) {
						var dispatched atomic.Int32
						hostClient, err := mcpbrokergrpc.NewSessionClient(lifecycle.PublicAddress(), 5*time.Second, 5*time.Second,
							grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12})),
							grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
								err := invoke(ctx, method, req, reply, conn, opts...)
								if method == "/mecatl.broker.v1.SessionService/InvokeTool" {
									dispatched.Add(1)
									if err == nil && mode == "lost-reply" {
										return status.Error(codes.Unavailable, "fixture discarded completed reply")
									}
								}
								return err
							}))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = hostClient.Close() })
						store := &sessionBrokerFailStore{Store: memstore.New()}
						provider := mockllm.New(mockllm.ToolCallTurn(
							session.NewToolCall("original", "mcp__private__whoami", []byte("{ }")),
							session.NewToolCall("new-id", "mcp__private__whoami", []byte(`{}`))), mockllm.TextTurn("done"))
						svc := sessionBrokerTestHost(t, hostClient, store, provider, nil)
						if mode == "permission-deny" {
							svc.cfg.SessionEngineWithTools = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
								catalogue := tool.NewCatalog()
								for _, candidate := range tools {
									if err := catalogue.Register(candidate); err != nil {
										return SessionEngineResult{}, err
									}
								}
								policy := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeManaged, Tool: "mcp__private__whoami", Pattern: "*", Effect: governance.Deny}}, nil)
								return SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalogue, Store: store, AuthorityEvaluator: localauthority.New(), Policy: policy}), Close: func() error { return nil }}, nil
							}
						}
						created, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
						if err != nil {
							t.Fatal(err)
						}
						pending, err := svc.ConnectWorkspaceServices(ctx, created.ID)
						if err != nil || pending.URL == "" {
							t.Fatalf("host enrollment: %+v %v", pending, err)
						}
						response, err := gateway.Client().Get(pending.URL)
						if err != nil {
							t.Fatal(err)
						}
						_ = response.Body.Close()
						if response.StatusCode != http.StatusOK {
							t.Fatalf("host callback: %d", response.StatusCode)
						}
						if _, err := svc.ConnectWorkspaceServices(ctx, created.ID); err != nil {
							t.Fatal(err)
						}
						_, before := private.snapshot()
						run, err := svc.StartInteractiveRunContent(ctx, created.ID, "call", nil)
						if err != nil {
							t.Fatal(err)
						}
						results := make(map[session.ToolCallID]session.ToolResult)
						for ev := range run.Events() {
							if ev.ToolResult != nil {
								results[ev.ToolResult.CallID] = *ev.ToolResult
							}
						}
						svc.FinishRun(created.ID, run)
						for _, id := range []session.ToolCallID{"original", "new-id"} {
							result, ok := results[id]
							if !ok || !result.IsError || (mode == "permission-deny" && !strings.Contains(result.Content, "permission denied:")) {
								t.Fatalf("missing expected denial/uncertainty result for %s: %+v", id, result)
							}
						}
						_, after := private.snapshot()
						expected := 0
						if mode == "lost-reply" {
							expected = 1
						}
						if after-before != expected || int(dispatched.Load()) != expected {
							t.Fatalf("dispatch/upstream delta=%d/%d want=%d", dispatched.Load(), after-before, expected)
						}
						saved, err := store.Load(ctx, created.ID)
						if err != nil {
							t.Fatal(err)
						}
						access, _ := saved.BrokerAccess()
						if mode == "lost-reply" && (access.Pending != "original" || len(access.Attempted) != 1) {
							t.Fatalf("uncertainty lost durable fence: %+v", access)
						}
						if mode == "permission-deny" && (access.Pending != "" || len(access.Attempted) != 0) {
							t.Fatalf("denied call acquired dispatch fence: %+v", access)
						}
					})
				}
				for _, mode := range []string{"resume", "save-failure", "permission-deny-resume", "changed-account", "cancel"} {
					t.Run("native-expiry-"+mode, func(t *testing.T) {
						issuer.mu.Lock()
						// Enrollment discovery may refresh the short-lived initial token.
						// Keep that grant short-lived too, before revoking refresh.
						issuer.tokenTTL, issuer.refreshTokenTTL = 1, 1
						issuer.mu.Unlock()
						defer func() {
							issuer.mu.Lock()
							issuer.tokenTTL, issuer.refreshTokenTTL, issuer.revokeRefresh, issuer.subject = 0, 0, false, ""
							issuer.mu.Unlock()
						}()
						store := &sessionBrokerFailStore{Store: memstore.New()}
						exact := []byte("{  }")
						provider := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("parked", "mcp__private__whoami", exact)), mockllm.TextTurn("done"))
						svc := sessionBrokerTestHost(t, client, store, provider, nil)
						created, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
						if err != nil {
							t.Fatal(err)
						}
						pending, err := svc.ConnectWorkspaceServices(ctx, created.ID)
						if err != nil || pending.URL == "" {
							t.Fatalf("enroll: %+v %v", pending, err)
						}
						response, err := gateway.Client().Get(pending.URL)
						if err != nil {
							t.Fatal(err)
						}
						_ = response.Body.Close()
						if response.StatusCode != http.StatusOK {
							t.Fatalf("enroll callback: %d", response.StatusCode)
						}
						if _, err = svc.ConnectWorkspaceServices(ctx, created.ID); err != nil {
							t.Fatal(err)
						}
						beforeSession, _ := store.Load(ctx, created.ID)
						oldAccess, _ := beforeSession.BrokerAccess()
						private.mu.Lock()
						private.onCall = func() {
							durable, err := store.Load(ctx, created.ID)
							if err != nil {
								t.Error(err)
								return
							}
							access, _ := durable.BrokerAccess()
							exactSaved := false
							for _, message := range durable.Conversation.Messages {
								for _, call := range message.ToolCalls {
									if call.ID == "parked" && bytes.Equal(call.Args, exact) {
										exactSaved = true
									}
								}
							}
							if access.Catalogue == oldAccess.Catalogue || access.Pending != "parked" || len(access.Attempted) != 1 || !exactSaved {
								t.Errorf("dispatch before exact durable adoption: %+v", access)
							}
						}
						private.mu.Unlock()
						defer func() { private.mu.Lock(); private.onCall = nil; private.mu.Unlock() }()
						issuer.mu.Lock()
						issuer.revokeRefresh = true
						issuer.mu.Unlock()
						time.Sleep(1100 * time.Millisecond)
						headers, before := private.snapshot()
						run, err := svc.StartInteractiveRunContent(ctx, created.ID, "call", nil)
						if err != nil {
							t.Fatal(err)
						}
						for range run.Events() {
						}
						svc.FinishRun(created.ID, run)
						saved, err := store.Load(ctx, created.ID)
						if err != nil {
							t.Fatal(err)
						}
						parked, ok := saved.PendingAuthorization()
						if !ok {
							t.Fatalf("native preflight did not park: %s", saved.State)
						}
						afterHeaders, after := private.snapshot()
						if after != before || len(afterHeaders) != len(headers) {
							t.Fatal("preflight executed upstream MCP I/O")
						}
						control := MCPAuthorizationControl{SessionID: created.ID, AuthorizationID: parked.Authorization.ID}
						prompt, err := svc.MCPAuthorizationPresentation(ctx, created.ID, control)
						if err != nil || prompt == "" {
							t.Fatalf("native browser presentation: %v", err)
						}
						if mode == "cancel" {
							cancelled, err := svc.CancelMCPAuthorization(ctx, created.ID, control)
							if err != nil {
								t.Fatal(err)
							}
							if cancelled.Run != nil {
								for range cancelled.Run.Events() {
								}
								svc.FinishRun(created.ID, cancelled.Run)
							}
							response, err := gateway.Client().Get(prompt)
							if err != nil {
								t.Fatal(err)
							}
							_ = response.Body.Close()
							if response.StatusCode == http.StatusOK {
								t.Fatal("cancelled flow accepted callback")
							}
							out, err := client.ResumeTool(ctx, oldAccess.Session, c.AuthorizationRef(parked.Authorization.ID), oldAccess.Catalogue)
							if err != nil || out.Kind != c.InvocationNotDispatched {
								t.Fatalf("cancel resume: %+v %v", out, err)
							}
							_, after = private.snapshot()
							if after != before {
								t.Fatal("cancel dispatched")
							}
							return
						}
						issuer.mu.Lock()
						issuer.tokenTTL, issuer.revokeRefresh = 0, false
						if mode == "changed-account" {
							issuer.subject = "other-test-user"
						}
						issuer.mu.Unlock()
						response, err = gateway.Client().Get(prompt)
						if err != nil {
							t.Fatal(err)
						}
						_ = response.Body.Close()
						if response.StatusCode != http.StatusOK {
							t.Fatalf("reauth callback: %d", response.StatusCode)
						}
						callBeforeAdoption := c.Call{ID: "parked", Name: "mcp__private__whoami", Arguments: exact}
						check, err := client.CheckAuthorization(ctx, oldAccess.Session, oldAccess.Catalogue, &callBeforeAdoption, "")
						if err != nil || check.Authorization != c.AuthorizationRef(parked.Authorization.ID) || check.Ready {
							t.Fatalf("callback-before-adoption preflight: %+v %v", check, err)
						}
						callBeforeAdoption.ID = "different-call"
						check, err = client.CheckAuthorization(ctx, oldAccess.Session, oldAccess.Catalogue, &callBeforeAdoption, "")
						if err != nil || check.Ready || check.Authorization != "" {
							t.Fatalf("another call replaced new grant: %+v %v", check, err)
						}
						outBeforeAdoption, err := client.ResumeTool(ctx, oldAccess.Session, c.AuthorizationRef(parked.Authorization.ID), oldAccess.Catalogue)
						if err != nil || outBeforeAdoption.Kind != c.InvocationNotDispatched {
							t.Fatalf("Resume before adoption: %+v %v", outBeforeAdoption, err)
						}
						if mode == "save-failure" {
							store.fail.Store(true)
							if _, err := svc.sessionBrokerResumeTools(ctx, saved, parked); err == nil {
								t.Fatal("failed adoption exposed Resume")
							}
							_, after = private.snapshot()
							if after != before {
								t.Fatal("save failure dispatched")
							}
							return
						}
						if mode == "permission-deny-resume" {
							svc.cfg.SessionEngineWithTools = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
								catalogue := tool.NewCatalog()
								for _, candidate := range tools {
									if err := catalogue.Register(candidate); err != nil {
										return SessionEngineResult{}, err
									}
								}
								policy := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeManaged, Tool: "mcp__private__whoami", Pattern: "*", Effect: governance.Deny}}, nil)
								return SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalogue, Store: store, AuthorityEvaluator: localauthority.New(), Policy: policy}), Close: func() error { return nil }}, nil
							}
						}
						if mode == "changed-account" {
							flow, err := client.ObserveAuthorization(ctx, oldAccess.Session, c.AuthorizationRef(parked.Authorization.ID))
							if err != nil || flow.Kind != c.FlowFailed || flow.Reason != c.FailureAuthorityWithdrawn {
								t.Fatalf("changed identity flow: %+v %v", flow, err)
							}
						}
						result, err := svc.RecheckMCPAuthorization(ctx, created.ID, control)
						if err != nil {
							t.Fatal(err)
						}
						if mode == "changed-account" {
							if result.Run != nil {
								for ev := range result.Run.Events() {
									if ev.ToolResult != nil && !ev.ToolResult.IsError {
										t.Fatal("changed account action succeeded")
									}
								}
								svc.FinishRun(created.ID, result.Run)
							}
							out, err := client.ResumeTool(ctx, oldAccess.Session, c.AuthorizationRef(parked.Authorization.ID), oldAccess.Catalogue)
							if err != nil || out.Kind != c.InvocationNotDispatched {
								t.Fatalf("account-change resume: %+v %v", out, err)
							}
							out, err = client.InvokeTool(ctx, oldAccess.Session, oldAccess.Catalogue, c.Call{ID: "parked", Name: "mcp__private__whoami", Arguments: exact})
							if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureAuthorityWithdrawn {
								t.Fatalf("changed-account original call replay: %+v %v", out, err)
							}
							_, after = private.snapshot()
							if after != before {
								t.Fatal("changed account dispatched")
							}
							return
						}
						if result.Run == nil {
							t.Fatal("no native continuation")
						}
						var resumed session.ToolResult
						for ev := range result.Run.Events() {
							if ev.ToolResult != nil && ev.ToolResult.CallID == "parked" {
								resumed = *ev.ToolResult
							}
						}
						svc.FinishRun(created.ID, result.Run)
						_, after = private.snapshot()
						if mode == "permission-deny-resume" {
							if after != before || !resumed.IsError || !strings.Contains(resumed.Content, "permission denied:") {
								t.Fatalf("resume deny: %+v delta=%d", resumed, after-before)
							}
							return
						}
						if resumed.CallID != "parked" || resumed.IsError || after-before != 1 {
							t.Fatalf("native Resume: %+v delta=%d", resumed, after-before)
						}
						durable, _ := store.Load(ctx, created.ID)
						adopted, _ := durable.BrokerAccess()
						if adopted.Catalogue == oldAccess.Catalogue || adopted.Pending != "" {
							t.Fatalf("adoption/fence: %+v", adopted)
						}
						// The production broker receipt proves Resume used retained bytes:
						// the exact call is cached; alternate whitespace with the same ID is refused.
						call := c.Call{ID: "parked", Name: "mcp__private__whoami", Arguments: exact}
						out, err := client.InvokeTool(ctx, adopted.Session, adopted.Catalogue, call)
						if err != nil || out.Kind != c.InvocationCompleted || out.Result.IsError {
							t.Fatalf("exact receipt: %+v %v", out, err)
						}
						call.Arguments = []byte("{}")
						out, err = client.InvokeTool(ctx, adopted.Session, adopted.Catalogue, call)
						if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCallChanged {
							t.Fatalf("retained byte proof: %+v %v", out, err)
						}
						_, after = private.snapshot()
						if after-before != 1 {
							t.Fatal("receipt replay dispatched twice")
						}
					})
				}
				t.Run("remote-revocation-after-ready-conservative", func(t *testing.T) {
					defer func() { private.mu.Lock(); private.revoked = false; private.mu.Unlock() }()
					headers, before := private.snapshot()
					call := c.Call{ID: "revoked", Name: cat.ToolNames()[0], Arguments: []byte("{ }")}
					check, err := client.CheckAuthorization(ctx, opened.Ref, cat.Ref(), &call, "")
					if err != nil || !check.Ready || check.Authorization != "" {
						t.Fatalf("native credential readiness failed: %+v %v", check, err)
					}
					afterHeaders, after := private.snapshot()
					if after != before || len(afterHeaders) != len(headers) {
						t.Fatal("preflight performed upstream I/O")
					}
					private.mu.Lock()
					private.revoked = true
					private.mu.Unlock()
					out, err := client.InvokeTool(ctx, opened.Ref, cat.Ref(), call)
					if err != nil || out.Kind == c.InvocationAuthorizationRequired || out.Kind == c.InvocationNotDispatched || (out.Kind == c.InvocationCompleted && !out.Result.IsError) {
						t.Fatalf("revocation fabricated authorization/success: %+v %v", out, err)
					}
					rejectedHeaders, _ := private.snapshot()
					repeated, err := client.InvokeTool(ctx, opened.Ref, cat.Ref(), call)
					if err != nil || repeated.Kind != out.Kind {
						t.Fatalf("uncertain receipt changed: %+v %v", repeated, err)
					}
					repeatedHeaders, after := private.snapshot()
					if len(repeatedHeaders) != len(rejectedHeaders) {
						t.Fatal("rejected call was blindly replayed")
					}
					if after != before {
						t.Fatal("revoked grant executed upstream tool")
					}
				})
			}
			if len(clients) != 1 || !lifecycle.Ready(ctx) {
				t.Fatal("more than one Redis owner or not ready")
			}
			clients[0].unready.Store(true)
			if lifecycle.Ready(ctx) {
				t.Fatal("metadata failure left listener ready")
			}
			clients[0].unready.Store(false)
			if err := lifecycle.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if clients[0].closes.Load() != 1 {
				t.Fatal("Redis client not closed exactly once")
			}
			if topology == "mixed-discovery-failure" {
				return
			}
			replacement, err := mcpbrokerserver.NewProduction(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			replacement.Start()
			t.Cleanup(func() { _ = replacement.Close(context.Background()) })
			target, err = url.Parse("https://" + replacement.PublicAddress())
			if err != nil {
				t.Fatal(err)
			}
			proxy = httputil.NewSingleHostReverseProxy(target)
			proxy.Transport = gateway.Client().Transport
			proxyRoute.Store(proxy)
			fresh, err := mcpbrokergrpc.NewSessionClient(replacement.PublicAddress(), 5*time.Second, 5*time.Second, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12})))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			recovered, err := fresh.OpenSession(ctx, &opened.Ref)
			if err != nil || recovered.Catalogue.Ref() == cat.Ref() || len(recovered.Catalogue.Tools()) != len(cat.Tools()) {
				t.Fatalf("native replacement: %+v %v", recovered, err)
			}
			for _, name := range recovered.Catalogue.ToolNames() {
				if name == "CallMcpWithQuery" {
					continue
				}
				old := c.Call{ID: session.ToolCallID(name), Name: name, Arguments: []byte(`{}`)}
				out, err := fresh.InvokeTool(ctx, opened.Ref, cat.Ref(), old)
				if err != nil || out.Kind != c.InvocationNotDispatched || out.Reason != c.FailureCatalogueChanged {
					t.Fatalf("replacement old-revision fence: %+v %v", out, err)
				}
				old.ID += "-fresh"
				out, err = fresh.InvokeTool(ctx, opened.Ref, recovered.Catalogue.Ref(), old)
				if err != nil || out.Kind != c.InvocationCompleted || out.Result.IsError {
					t.Fatalf("replacement native invoke: %+v %v", out, err)
				}
			}
			if len(clients) != 2 {
				t.Fatal("replacement created extra Redis clients")
			}
			if err := replacement.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if clients[1].closes.Load() != 1 {
				t.Fatal("replacement Redis client not closed exactly once")
			}
		})
	}
}
