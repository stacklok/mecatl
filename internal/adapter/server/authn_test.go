package server_test

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
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// dialGRPCSecure stands up an in-memory gRPC server with the given Authenticator
// installed as interceptors, returning a client and cleanup. The returned client
// sends no credentials; tests attach metadata per-call.
func dialGRPCSecure(t *testing.T, svc *server.Service, auth *server.Authenticator) (mecatlv1.HarnessServiceClient, func()) {
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

// bearerCtx returns a context carrying an Authorization: Bearer <tok> metadata.
func bearerCtx(ctx context.Context, tok string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
}

// --- gRPC auth ---------------------------------------------------------------

func TestGRPCAuthBearer(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No token -> Unauthenticated.
	_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no-token code = %v, want Unauthenticated", status.Code(err))
	}

	// Wrong token -> Unauthenticated.
	_, err = client.CreateSession(bearerCtx(ctx, "nope"), &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong-token code = %v, want Unauthenticated", status.Code(err))
	}

	// Correct token -> OK.
	if _, err := client.CreateSession(bearerCtx(ctx, "secret"), &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("correct-token CreateSession: %v", err)
	}
}

// With no token configured, requests pass without credentials (dev mode).
func TestGRPCAuthDisabledAllows(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("auth-disabled CreateSession: %v", err)
	}
}

func TestGRPCAuthRejectsDuplicateAuthorizationMetadata(t *testing.T) {
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "conflicting", values: []string{"Bearer secret", "Bearer wrong"}},
		{name: "both valid", values: []string{"Bearer secret", "Bearer secret"}},
	} {
		t.Run(tc.name+" unary", func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", tc.values[0], "authorization", tc.values[1]))
			called := false
			_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
				called = true
				return nil, nil
			})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
			}
			if called {
				t.Fatal("handler was called for duplicate authorization metadata")
			}
		})
		t.Run(tc.name+" stream", func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				"authorization", tc.values[0], "authorization", tc.values[1]))
			called := false
			err := auth.StreamInterceptor()(nil, authTestStream{ctx: ctx}, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error {
				called = true
				return nil
			})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
			}
			if called {
				t.Fatal("handler was called for duplicate authorization metadata")
			}
		})
	}
}

func TestGRPCAuthRejectsDuplicateAuthorizationMetadataWithoutAuthConfig(t *testing.T) {
	auth := server.NewAuthenticator(server.SecurityConfig{})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer one", "authorization", "Bearer two"))

	called := false
	_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unary code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatal("unary handler was called for duplicate authorization metadata without auth")
	}

	called = false
	err = auth.StreamInterceptor()(nil, authTestStream{ctx: ctx}, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error {
		called = true
		return nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatal("stream handler was called for duplicate authorization metadata without auth")
	}
}

type authTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s authTestStream) Context() context.Context { return s.ctx }

// --- gRPC rate limit ---------------------------------------------------------

func TestGRPCRateLimit(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	// 1 rps, burst 2: the first two calls pass, the third is rejected.
	auth := server.NewAuthenticator(server.SecurityConfig{RateLimit: 1, RateBurst: 2})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("call 3 code = %v, want ResourceExhausted", status.Code(err))
	}

	// After ~1s a token refills and a call recovers.
	time.Sleep(1100 * time.Millisecond)
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("recovery call: %v", err)
	}
}

// --- HTTP auth ---------------------------------------------------------------

// secureHTTP mounts the API behind the auth middleware and the health endpoints
// outside it, mirroring serve() in cmd/mecated.
func secureHTTP(svc *server.Service, auth *server.Authenticator) http.Handler {
	mux := http.NewServeMux()
	server.NewHealthHandler(nil).RegisterHealth(mux)
	mux.Handle("/", auth.Middleware(server.NewHTTPHandler(svc)))
	return mux
}

func TestHTTPAuthBearer(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	srv := httptest.NewServer(secureHTTP(svc, auth))
	defer srv.Close()

	body := func() *strings.Reader { return strings.NewReader(`{"workspace":"/ws"}`) }

	// No token -> 401.
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", body())
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}

	// Wrong token -> 401.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions", body())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer nope")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post wrong: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", resp.StatusCode)
	}

	// Correct token -> 201.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions", body())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post correct: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("correct-token status = %d, want 201", resp.StatusCode)
	}
}

// TestListModelsRequiresAuth pins the disclosure guarantee that ListModels is
// behind auth on BOTH surfaces — it would otherwise leak the available-provider
// set (CWE-200) to an unauthenticated caller. The gRPC UnaryInterceptor wraps ALL
// handlers method-agnostically and the HTTP auth.Middleware wraps the whole mux, so
// this is structurally covered; the explicit test guards against a future handler
// that bypasses the interceptor.
func TestListModelsRequiresAuth(t *testing.T) {
	// gRPC: no credential ⇒ Unauthenticated; correct token ⇒ OK.
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.ListModels(ctx, &mecatlv1.ListModelsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("gRPC ListModels no-token code = %v, want Unauthenticated", status.Code(err))
	}
	if _, err := client.ListModels(bearerCtx(ctx, "secret"), &mecatlv1.ListModelsRequest{}); err != nil {
		t.Fatalf("gRPC ListModels correct-token: %v", err)
	}

	// HTTP: GET /v1/models with no token ⇒ 401; with the token ⇒ 200.
	srv := httptest.NewServer(secureHTTP(svc, auth))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HTTP ListModels no-token status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models with token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP ListModels correct-token status = %d, want 200", resp.StatusCode)
	}
}

// Health endpoints must bypass auth even when a token is configured.
func TestHTTPHealthBypassesAuth(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret", RateLimit: 1, RateBurst: 1})
	srv := httptest.NewServer(secureHTTP(svc, auth))
	defer srv.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (no auth required)", path, resp.StatusCode)
		}
	}
}

// --- HTTP rate limit ---------------------------------------------------------

func TestHTTPRateLimit(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{RateLimit: 1, RateBurst: 1})
	srv := httptest.NewServer(secureHTTP(svc, auth))
	defer srv.Close()

	// First GET passes; the immediate second is throttled.
	resp, err := http.Get(srv.URL + "/v1/sessions/none")
	if err != nil {
		t.Fatalf("get 1: %v", err)
	}
	resp.Body.Close()
	// 404 (unknown session) is fine — it still consumed a token.

	resp, err = http.Get(srv.URL + "/v1/sessions/none")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("get 2 status = %d, want 429", resp.StatusCode)
	}
}
