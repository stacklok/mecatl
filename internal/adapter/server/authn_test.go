package server_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// dialGRPCSecure stands up an in-memory gRPC server with the given Authenticator
// installed as interceptors, returning a client and cleanup. The returned client
// sends no credentials; tests attach metadata per-call.
func dialGRPCSecure(t *testing.T, svc *server.Service, auth *server.Authenticator, streamInterceptors ...grpc.StreamServerInterceptor) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	streamInterceptors = append([]grpc.StreamServerInterceptor{auth.StreamInterceptor()}, streamInterceptors...)
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(streamInterceptors...),
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

func TestAuthenticationRejectionDiagnosticsAreCategorizedAndSafe(t *testing.T) {
	t.Parallel()

	const sensitive = "Bearer eyJ.secret.token issuer=https://private.example subject=alice kid=private-key"
	for _, tc := range []struct {
		name          string
		inputCategory string
		wantCategory  string
	}{
		{name: "wrong audience", inputCategory: "wrong_audience", wantCategory: "wrong_audience"},
		{name: "wrong issuer", inputCategory: "wrong_issuer", wantCategory: "wrong_issuer"},
		{name: "unknown external category", inputCategory: "issuer=https://private.example", wantCategory: "invalid_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diag := &authRecordingDiagnostics{}
			auth := server.NewAuthenticator(server.SecurityConfig{
				Validator:   diagnosticValidator{category: tc.inputCategory, err: sensitive},
				Diagnostics: diag,
			})
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", sensitive))
			_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
				t.Fatal("handler called after rejected identity")
				return nil, nil
			})
			if got := status.Code(err); got != codes.Unauthenticated {
				t.Fatalf("gRPC code = %v, want Unauthenticated", got)
			}
			if got := status.Convert(err).Message(); got != "missing or invalid bearer token" {
				t.Fatalf("gRPC message = %q, want generic result", got)
			}
			if len(diag.records) != 1 {
				t.Fatalf("diagnostic records = %d, want 1", len(diag.records))
			}
			record := diag.records[0]
			assertAuthRecord(t, record, port.LevelWarn, "rejected", tc.wantCategory, "grpc", "Unauthenticated")
			if strings.Contains(record.render(), sensitive) || strings.Contains(record.render(), "private.example") || strings.Contains(record.render(), "alice") || strings.Contains(record.render(), "private-key") {
				t.Fatalf("diagnostic leaked sensitive authentication data: %s", record.render())
			}
		})
	}
}

func TestHTTPAuthenticationRejectionDiagnosticPreservesGenericClientResult(t *testing.T) {
	t.Parallel()

	const sensitive = "Bearer eyJ.secret.token issuer=https://private.example subject=alice kid=private-key"
	diag := &authRecordingDiagnostics{}
	auth := server.NewAuthenticator(server.SecurityConfig{
		Validator:   diagnosticValidator{category: "wrong_issuer", err: sensitive},
		Diagnostics: diag,
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", sensitive)
	response := httptest.NewRecorder()
	auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called after rejected identity")
	})).ServeHTTP(response, req)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP status = %d, want 401", response.Code)
	}
	if got := response.Body.String(); !strings.Contains(got, "missing or invalid bearer token") {
		t.Fatalf("HTTP response = %q, want generic result", got)
	}
	if len(diag.records) != 1 {
		t.Fatalf("diagnostic records = %d, want 1", len(diag.records))
	}
	record := diag.records[0]
	assertAuthRecord(t, record, port.LevelWarn, "rejected", "wrong_issuer", "http", "401")
	if strings.Contains(record.render(), sensitive) || strings.Contains(record.render(), "private.example") || strings.Contains(record.render(), "alice") || strings.Contains(record.render(), "private-key") {
		t.Fatalf("diagnostic leaked sensitive authentication data: %s", record.render())
	}
}

func TestHTTPMalformedAndStaticBearerRejectionsAreObservedSafely(t *testing.T) {
	t.Parallel()

	const sensitive = "Bearer secret-token issuer=https://private.example subject=alice"
	for _, tc := range []struct {
		name     string
		config   server.SecurityConfig
		header   string
		category string
	}{
		{name: "malformed identity bearer", config: server.SecurityConfig{Validator: diagnosticValidator{category: "malformed", err: sensitive}}, header: sensitive, category: "malformed"},
		{name: "static bearer", config: server.SecurityConfig{AuthToken: "secret-token"}, header: "Bearer wrong-token", category: "invalid_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diag := &authRecordingDiagnostics{}
			tc.config.Diagnostics = diag
			auth := server.NewAuthenticator(tc.config)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.header)
			response := httptest.NewRecorder()
			auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler called after rejected authentication")
			})).ServeHTTP(response, req)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("HTTP status = %d, want 401", response.Code)
			}
			if got := response.Body.String(); !strings.Contains(got, "missing or invalid bearer token") || strings.Contains(got, sensitive) || strings.Contains(got, "wrong-token") {
				t.Fatalf("HTTP response = %q, want generic rejection", got)
			}
			if len(diag.records) != 1 {
				t.Fatalf("diagnostic records = %d, want 1", len(diag.records))
			}
			record := diag.records[0]
			assertAuthRecord(t, record, port.LevelWarn, "rejected", tc.category, "http", "401")
			for _, value := range []string{"secret-token", "wrong-token", "private.example", "alice"} {
				if strings.Contains(record.render(), value) {
					t.Fatalf("diagnostic leaked %q: %s", value, record.render())
				}
			}
		})
	}
}

func TestAuthenticationDiagnosticsCoverEnabledSuccessAndAvailability(t *testing.T) {
	t.Parallel()

	principal := &session.Principal{Issuer: "https://private.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for _, tc := range []struct {
		name       string
		config     server.SecurityConfig
		wantStatus int
		outcome    string
		category   string
		status     string
		body       string
		level      port.Level
	}{
		{name: "static bearer accepted", config: server.SecurityConfig{AuthToken: "secret"}, wantStatus: http.StatusNoContent, outcome: "accepted", category: "static_bearer", status: "200", level: port.LevelInfo},
		{name: "identity accepted", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{principal: principal}}, wantStatus: http.StatusNoContent, outcome: "accepted", category: "validated_identity", status: "200", level: port.LevelInfo},
		{name: "identity unavailable", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{err: unavailableDiagnosticError{}}}, wantStatus: http.StatusServiceUnavailable, outcome: "unavailable", category: "jwks_stale", status: "503", body: "identity provider unavailable", level: port.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diag := &authRecordingDiagnostics{}
			tc.config.Diagnostics = diag
			auth := server.NewAuthenticator(tc.config)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, req)

			if response.Code != tc.wantStatus {
				t.Fatalf("HTTP status = %d, want %d", response.Code, tc.wantStatus)
			}
			if tc.body != "" && !strings.Contains(response.Body.String(), tc.body) {
				t.Fatalf("HTTP response = %q, want generic %q", response.Body.String(), tc.body)
			}
			if strings.Contains(response.Body.String(), "sensitive IdP failure") {
				t.Fatalf("HTTP response leaked validator error: %q", response.Body.String())
			}
			if len(diag.records) != 1 {
				t.Fatalf("diagnostic records = %d, want 1", len(diag.records))
			}
			record := diag.records[0]
			assertAuthRecord(t, record, tc.level, tc.outcome, tc.category, "http", tc.status)
			for _, sensitive := range []string{"secret", "private.example", "alice"} {
				if strings.Contains(record.render(), sensitive) {
					t.Fatalf("diagnostic leaked %q: %s", sensitive, record.render())
				}
			}
		})
	}

	diag := &authRecordingDiagnostics{}
	auth := server.NewAuthenticator(server.SecurityConfig{Diagnostics: diag})
	auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if len(diag.records) != 0 {
		t.Fatalf("disabled authentication emitted diagnostics: %#v", diag.records)
	}
}

func TestGRPCAuthenticationDiagnosticsCoverSuccessAndUnavailable(t *testing.T) {
	t.Parallel()

	principal := &session.Principal{Issuer: "https://private.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for _, tc := range []struct {
		name        string
		config      server.SecurityConfig
		bearer      string
		wantCode    codes.Code
		wantMessage string
		outcome     string
		category    string
		statusText  string
		level       port.Level
	}{
		{name: "static bearer accepted", config: server.SecurityConfig{AuthToken: "secret"}, bearer: "secret", wantCode: codes.OK, outcome: "accepted", category: "static_bearer", statusText: "OK", level: port.LevelInfo},
		{name: "identity accepted", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{principal: principal}}, bearer: "sensitive-token", wantCode: codes.OK, outcome: "accepted", category: "validated_identity", statusText: "OK", level: port.LevelInfo},
		{name: "identity unavailable", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{err: unavailableDiagnosticError{}}}, bearer: "sensitive-token", wantCode: codes.Unavailable, wantMessage: "identity provider unavailable", outcome: "unavailable", category: "jwks_stale", statusText: "Unavailable", level: port.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diag := &authRecordingDiagnostics{}
			tc.config.Diagnostics = diag
			auth := server.NewAuthenticator(tc.config)
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tc.bearer))
			_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return nil, nil })
			if status.Code(err) != tc.wantCode {
				t.Fatalf("gRPC code = %v, want %v", status.Code(err), tc.wantCode)
			}
			if tc.wantMessage != "" && status.Convert(err).Message() != tc.wantMessage {
				t.Fatalf("gRPC message = %q, want %q", status.Convert(err).Message(), tc.wantMessage)
			}
			if len(diag.records) != 1 {
				t.Fatalf("diagnostic records = %d, want 1", len(diag.records))
			}
			record := diag.records[0]
			assertAuthRecord(t, record, tc.level, tc.outcome, tc.category, "grpc", tc.statusText)
			for _, sensitive := range []string{"secret", "sensitive-token", "private.example", "alice", "sensitive IdP failure"} {
				if strings.Contains(record.render(), sensitive) {
					t.Fatalf("diagnostic leaked %q: %s", sensitive, record.render())
				}
			}
		})
	}
}

func TestAcceptedAuthenticationDiagnosticsAreBoundedPerCategoryAndTransport(t *testing.T) {
	principal := &session.Principal{Issuer: "https://private.example", Subject: "alice", GrantType: session.GrantTypeUser}
	for _, tc := range []struct {
		name      string
		config    server.SecurityConfig
		transport string
		token     string
		category  string
		status    string
	}{
		{name: "static grpc", config: server.SecurityConfig{AuthToken: "secret"}, transport: "grpc", token: "secret", category: "static_bearer", status: "OK"},
		{name: "static http", config: server.SecurityConfig{AuthToken: "secret"}, transport: "http", token: "secret", category: "static_bearer", status: "200"},
		{name: "identity grpc", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{principal: principal}}, transport: "grpc", token: "sensitive-token", category: "validated_identity", status: "OK"},
		{name: "identity http", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{principal: principal}}, transport: "http", token: "sensitive-token", category: "validated_identity", status: "200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diag := &authRecordingDiagnostics{}
			tc.config.Diagnostics = diag
			auth := server.NewAuthenticator(tc.config)

			var wg sync.WaitGroup
			for range 32 {
				wg.Go(func() {
					switch tc.transport {
					case "grpc":
						ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tc.token))
						_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return nil, nil })
						if err != nil {
							t.Errorf("accepted gRPC request failed: %v", err)
						}
					case "http":
						req := httptest.NewRequest(http.MethodGet, "/", nil)
						req.Header.Set("Authorization", "Bearer "+tc.token)
						auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(httptest.NewRecorder(), req)
					}
				})
			}
			wg.Wait()

			records := diag.snapshot()
			if len(records) != 1 {
				t.Fatalf("accepted authentication diagnostics = %d, want 1", len(records))
			}
			record := records[0]
			assertAuthRecord(t, record, port.LevelInfo, "accepted", tc.category, tc.transport, tc.status)
		})
	}
}

func TestRejectedAndUnavailableAuthenticationDiagnosticsRemainPerRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config server.SecurityConfig
		header string
		status int
	}{
		{name: "rejected", config: server.SecurityConfig{AuthToken: "secret"}, header: "Bearer wrong", status: http.StatusUnauthorized},
		{name: "malformed", config: server.SecurityConfig{Validator: diagnosticValidator{category: "malformed", err: "invalid"}}, header: "Bearer malformed", status: http.StatusUnauthorized},
		{name: "unavailable", config: server.SecurityConfig{Validator: fixedDiagnosticValidator{err: unavailableDiagnosticError{}}}, header: "Bearer unavailable", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diag := &authRecordingDiagnostics{}
			tc.config.Diagnostics = diag
			auth := server.NewAuthenticator(tc.config)
			h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			for range 2 {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", tc.header)
				response := httptest.NewRecorder()
				h.ServeHTTP(response, req)
				if response.Code != tc.status {
					t.Fatalf("HTTP status = %d, want %d", response.Code, tc.status)
				}
			}
			if records := diag.snapshot(); len(records) != 2 {
				t.Fatalf("authentication diagnostics = %d, want 2", len(records))
			}
		})
	}
}

type fixedDiagnosticValidator struct {
	principal *session.Principal
	err       error
}

func (v fixedDiagnosticValidator) Validate(context.Context, string) (*session.Principal, error) {
	return v.principal, v.err
}

type unavailableDiagnosticError struct{}

func (unavailableDiagnosticError) Error() string                           { return "sensitive IdP failure" }
func (unavailableDiagnosticError) Unwrap() error                           { return server.ErrIdentityUnavailable }
func (unavailableDiagnosticError) AuthenticationRejectionCategory() string { return "jwks_stale" }

type diagnosticValidator struct {
	category string
	err      string
}

func (v diagnosticValidator) Validate(context.Context, string) (*session.Principal, error) {
	return nil, diagnosticRejection(v)
}

type diagnosticRejection struct {
	category string
	err      string
}

func (e diagnosticRejection) Error() string                           { return e.err }
func (diagnosticRejection) Unwrap() error                             { return server.ErrInvalidToken }
func (e diagnosticRejection) AuthenticationRejectionCategory() string { return e.category }

type diagnosticRecord struct {
	level   port.Level
	message string
	args    map[string]string
}

func (r diagnosticRecord) render() string {
	return r.message + " " + r.args["outcome"] + " " + r.args["category"] + " " + r.args["transport"] + " " + r.args["status"]
}

func assertAuthRecord(t *testing.T, record diagnosticRecord, level port.Level, outcome, category, transport, statusText string) {
	t.Helper()
	if record.level != level || record.message != "authentication" || record.args["outcome"] != outcome || record.args["category"] != category || record.args["transport"] != transport || record.args["status"] != statusText || len(record.args) != 4 {
		t.Fatalf("unexpected authentication diagnostic: %#v", record)
	}
}

type authRecordingDiagnostics struct {
	mu      sync.Mutex
	records []diagnosticRecord
}

func (d *authRecordingDiagnostics) Log(_ context.Context, level port.Level, msg string, args ...any) {
	record := diagnosticRecord{level: level, message: msg, args: make(map[string]string, len(args)/2)}
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		record.args[key], _ = args[i+1].(string)
	}
	d.mu.Lock()
	d.records = append(d.records, record)
	d.mu.Unlock()
}

func (d *authRecordingDiagnostics) snapshot() []diagnosticRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]diagnosticRecord(nil), d.records...)
}

func (d *authRecordingDiagnostics) With(...any) port.Diagnostics { return d }

func TestServerInfoRequiresConfiguredAuthentication(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	if _, err := client.GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated GetServerInfo code = %v, want Unauthenticated", status.Code(err))
	}
	info, err := client.GetServerInfo(bearerCtx(context.Background(), "secret"), &mecatlv1.GetServerInfoRequest{ProviderId: "test-provider"})
	if err != nil || info.GetBuildId() != "test-build" || info.GetServerImplementation() != "unknown" || info.GetLlmProviderDisplayEndpoint() != "https://provider.example:8443/v1" {
		t.Fatalf("authenticated GetServerInfo = %#v, %v", info, err)
	}

	h := auth.Middleware(server.NewHTTPHandler(svc))
	unauthenticated := httptest.NewRecorder()
	h.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/v1/info", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /v1/info = %d, want 401", unauthenticated.Code)
	}
	authenticated := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(authenticated, req)
	if authenticated.Code != http.StatusOK || !strings.Contains(authenticated.Body.String(), "test-build") || !strings.Contains(authenticated.Body.String(), `"server_implementation":"unknown"`) {
		t.Fatalf("authenticated GET /v1/info = %d %q", authenticated.Code, authenticated.Body.String())
	}
}

func TestGRPCAuthBearer(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No token -> Unauthenticated.
	_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no-token code = %v, want Unauthenticated", status.Code(err))
	}

	// Wrong token -> Unauthenticated.
	_, err = client.CreateSession(bearerCtx(ctx, "nope"), &mecatlv1.CreateSessionRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong-token code = %v, want Unauthenticated", status.Code(err))
	}

	// Correct token -> OK.
	if _, err := client.CreateSession(bearerCtx(ctx, "secret"), &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("correct-token CreateSession: %v", err)
	}
}

// With no token configured, requests pass without credentials (dev mode).
func TestGRPCAuthDisabledAllows(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	diag := &authRecordingDiagnostics{}
	auth := server.NewAuthenticator(server.SecurityConfig{Diagnostics: diag})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("auth-disabled CreateSession: %v", err)
	}
	if len(diag.records) != 0 {
		t.Fatalf("auth-disabled unary emitted diagnostics: %#v", diag.records)
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
	diag := &authRecordingDiagnostics{}
	auth := server.NewAuthenticator(server.SecurityConfig{Diagnostics: diag})
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
	if len(diag.records) != 0 {
		t.Fatalf("auth-disabled duplicate unary/stream emitted diagnostics: %#v", diag.records)
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

	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("call 3 code = %v, want ResourceExhausted", status.Code(err))
	}

	// After ~1s a token refills and a call recovers.
	time.Sleep(1100 * time.Millisecond)
	if _, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{}); err != nil {
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

	body := func() *strings.Reader { return strings.NewReader(`{}`) }

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
