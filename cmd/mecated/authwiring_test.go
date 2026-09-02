package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// This file closes the auth WIRING gap. internal/adapter/server/authn_test.go
// already proves the Authenticator unit in isolation (it hand-builds a
// SecurityConfig and serves over bufconn/httptest); what was NEVER exercised is
// the flag → config → SecurityConfig → served-request path the BINARY actually
// runs: parseFlags reading --auth-token / MECATL_AUTH_TOKEN, and serve()
// building server.SecurityConfig{AuthToken: cfg.authToken, ...} from it. These
// tests drive that real wiring and assert accept-with-token / reject-without on
// BOTH surfaces (gRPC codes.Unauthenticated + HTTP 401). They are fully offline
// (mockllm provider, loopback bufconn/httptest only) and the asserted request —
// CreateSession — is rejected PRE-auth, so they consume ZERO provider turns.

type offlinePlacementProvider struct{}

func (offlinePlacementProvider) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "offline", Revision: "v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), nil)}, nil
}

func (offlinePlacementProvider) Reattach(context.Context, server.PlacementReattachRequest) (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "offline", Revision: "v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), nil)}, nil
}

// newOfflineService builds a *server.Service over the offline reference adapters
// (mockllm provider, in-memory store + workspace), the same way the server
// package's own tests do (see budget_test.go). It needs no network or key.
func newOfflineService(t *testing.T) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("ok"))
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		// The canonical floor-scoped allow set (same ruleset production children
		// use); a CreateSession never reaches a tool, but the engine wants a policy.
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:  "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		PlacementProvider:   offlinePlacementProvider{},
		PlacementScope:      "test",
		SharedEngineRoot:    "/ws",
	})
	if err != nil {
		t.Fatalf("new offline service: %v", err)
	}
	return svc
}

// authenticatorFromFlags runs the REAL flag→config→SecurityConfig path: it
// parses argv through parseFlags (so --auth-token / MECATL_AUTH_TOKEN lands in
// cfg.authToken) and constructs the Authenticator from that config EXACTLY as
// serve() does (see cmd/mecated/main.go: server.SecurityConfig{AuthToken:
// cfg.authToken, RateLimit: cfg.rateLimit, RateBurst: cfg.rateBurst}). Mirroring
// serve()'s construction here — rather than calling serve(), which binds real
// listeners and blocks — is the same pattern TestAdminMuxMountsPerfMCP uses for
// the admin mux. It returns the parsed token so a test can assert the flag
// actually threaded through.
func authenticatorFromFlags(t *testing.T, argv []string) (*server.Authenticator, string) {
	t.Helper()
	cfg, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(%v): %v", argv, err)
	}
	auth := server.NewAuthenticator(server.SecurityConfig{
		AuthToken: cfg.authToken,
		RateLimit: cfg.rateLimit,
		RateBurst: cfg.rateBurst,
	})
	return auth, cfg.authToken
}

// dialWiredGRPC stands up an in-memory gRPC server with the given Authenticator
// installed as the unary + stream interceptors — the SAME interceptors serve()
// installs (grpc.UnaryInterceptor(auth.UnaryInterceptor()), ...). The returned
// client sends no credentials; callers attach metadata per-call.
func dialWiredGRPC(t *testing.T, svc *server.Service, auth *server.Authenticator) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryInterceptor()),
		grpc.StreamInterceptor(auth.StreamInterceptor()),
	)
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}

// wiredHTTP mounts the API behind auth.Middleware and the health endpoints
// outside it — the SAME layering serve() builds (httpMux with
// NewHealthHandler(...).RegisterHealth + mux.Handle("/", auth.Middleware(
// NewHTTPHandler(svc)))).
func wiredHTTP(svc *server.Service, auth *server.Authenticator) http.Handler {
	mux := http.NewServeMux()
	server.NewHealthHandler(func() bool { return true }).RegisterHealth(mux)
	mux.Handle("/", auth.Middleware(server.NewHTTPHandler(svc)))
	return mux
}

func grpcBearer(ctx context.Context, tok string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
}

// TestAuthWiringGRPC proves the flag→SecurityConfig→interceptor path on the gRPC
// surface: --auth-token threads into cfg.authToken, the Authenticator built from
// it rejects a tokenless / wrong-token CreateSession with codes.Unauthenticated
// and admits the correct token. CreateSession is gated PRE-auth (no model turn),
// so this consumes no provider calls.
func TestAuthWiringGRPC(t *testing.T) {
	const token = "wire-secret"
	svc := newOfflineService(t)
	auth, parsed := authenticatorFromFlags(t, []string{"--auth-token", token})
	if parsed != token {
		t.Fatalf("parseFlags did not thread --auth-token: cfg.authToken = %q, want %q", parsed, token)
	}
	client, cleanup := dialWiredGRPC(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// reject: no token.
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no-token code = %v, want Unauthenticated", status.Code(err))
	}
	// reject: wrong token.
	if _, err := client.CreateSession(grpcBearer(ctx, "nope"), &mecatlv1.CreateSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong-token code = %v, want Unauthenticated", status.Code(err))
	}
	// accept: correct token.
	if _, err := client.CreateSession(grpcBearer(ctx, token), &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("correct-token CreateSession through wired auth: %v", err)
	}
}

// TestAuthWiringHTTP is TestAuthWiringGRPC's HTTP twin: the same flag-built
// Authenticator, mounted as serve() mounts auth.Middleware, rejects a tokenless /
// wrong-token POST /v1/sessions with 401 and admits the correct token (201).
func TestAuthWiringHTTP(t *testing.T) {
	const token = "wire-secret"
	svc := newOfflineService(t)
	auth, _ := authenticatorFromFlags(t, []string{"--auth-token", token})
	srv := httptest.NewServer(wiredHTTP(svc, auth))
	defer srv.Close()

	body := func() *strings.Reader { return strings.NewReader(`{}`) }

	// reject: no token.
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", body())
	if err != nil {
		t.Fatalf("post no-token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}

	// reject: wrong token.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions", body())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer nope")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post wrong-token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", resp.StatusCode)
	}

	// accept: correct token.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions", body())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post correct-token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("correct-token status = %d, want 201", resp.StatusCode)
	}
}

// TestAuthWiringTokenFromEnv proves the OTHER half of the flag→config wiring:
// when --auth-token is unset, parseFlags adopts MECATL_AUTH_TOKEN (so the secret
// need not appear in the process argv), and the Authenticator built from that
// config enforces it. Asserted on the gRPC surface (the HTTP path shares the same
// cfg.authToken). Offline: CreateSession is pre-auth-gated, zero provider turns.
func TestAuthWiringTokenFromEnv(t *testing.T) {
	const token = "env-secret"
	t.Setenv("MECATL_AUTH_TOKEN", token)

	svc := newOfflineService(t)
	// No --auth-token flag: parseFlags must fall back to the env var.
	auth, parsed := authenticatorFromFlags(t, nil)
	if parsed != token {
		t.Fatalf("parseFlags did not adopt MECATL_AUTH_TOKEN: cfg.authToken = %q, want %q", parsed, token)
	}
	client, cleanup := dialWiredGRPC(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("env-token no-credential code = %v, want Unauthenticated", status.Code(err))
	}
	if _, err := client.CreateSession(grpcBearer(ctx, token), &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("env-token correct-credential CreateSession: %v", err)
	}
}
