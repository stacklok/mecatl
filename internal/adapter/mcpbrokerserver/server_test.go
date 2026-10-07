package mcpbrokerserver

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"

	oidcadapter "github.com/stacklok/mecatl/authn/oidc"
	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const testAudience = "mecabroker"

type identityFixture struct {
	server    *httptest.Server
	key       *rsa.PrivateKey
	available atomic.Bool
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &identityFixture{key: key}
	f.available.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.server.URL, "jwks_uri": f.server.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		if !f.available.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "broker-key",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}
func (f *identityFixture) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})
}
func (f *identityFixture) token(t *testing.T, issuer, audience string, expiry time.Time, key *rsa.PrivateKey) string {
	return f.tokenWithSubject(t, issuer, audience, "workload-secret-identity", expiry, key)
}
func (f *identityFixture) tokenWithSubject(t *testing.T, issuer, audience, subject string, expiry time.Time, key *rsa.PrivateKey) string {
	t.Helper()
	if key == nil {
		key = f.key
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "broker-key", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "aud": audience, "iat": time.Now().Add(-time.Minute).Unix(), "exp": expiry.Unix()})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func productionOIDC(f *identityFixture, staleness time.Duration) WorkloadJWTConfig {
	return WorkloadJWTConfig{Issuer: f.server.URL, JWKSURI: f.server.URL + "/keys", Audience: testAudience, AllowedSubjects: []string{"workload-secret-identity"}, TrustedCAPEM: f.caPEM(), MaxJWKSStaleness: staleness}
}

func TestSingletonBrokerRemediation_Scenario3_ReadinessDoesNotLaunderStaleKeys(t *testing.T) {
	issuer := newIdentityFixture(t)
	const staleness = 300 * time.Millisecond
	srv, err := newBrokerHost(t.Context(), hostConfig{SessionAPI: &countingService{}, WorkloadJWT: productionOIDC(issuer, staleness), ReadinessTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.close(context.Background()) }()
	token := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	if _, err := srv.verifier.Validate(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !srv.ready(t.Context()) {
		t.Fatal("healthy JWKS dependency did not pass readiness")
	}
	issuer.available.Store(false)
	time.Sleep(150 * time.Millisecond)
	if _, err := srv.verifier.Validate(t.Context(), token); !errors.Is(err, oidcadapter.ErrIdentityUnavailable) {
		t.Fatalf("stale keys accepted: %v", err)
	}
	if srv.ready(t.Context()) {
		t.Fatal("readiness stayed open after JWKS became unavailable")
	}
}

type countingService struct {
	contract.SessionService
	reads atomic.Int32
}

func (s *countingService) OpenSession(context.Context, *contract.SessionRef) (contract.SessionSnapshot, error) {
	s.reads.Add(1)
	cat, err := contract.NewCatalogue(contract.CatalogueRef(strings.Repeat("A", 43)), "", nil)
	return contract.SessionSnapshot{Ref: contract.SessionRef(strings.Repeat("A", 43)), ExpiresAt: time.Now().Add(time.Hour), Catalogue: cat}, err
}

func startAuthenticatedBroker(t *testing.T, service contract.SessionService, oidc WorkloadJWTConfig, diag port.Diagnostics, observe func(string, string)) (brokerv1.SessionServiceClient, *grpc.ClientConn, string) {
	t.Helper()
	srv, err := newBrokerHost(t.Context(), hostConfig{SessionAPI: service, WorkloadJWT: oidc, Diagnostics: diag, Observe: observe})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.close(context.Background()) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer, err := srv.newGRPCServer(&tls.Config{Certificates: []tls.Certificate{fCertificate(t, oidc)}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: rootsFromPEM(t, oidc.TrustedCAPEM), ServerName: "example.com", MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return brokerv1.NewSessionServiceClient(conn), conn, listener.Addr().String()
}
func fCertificate(t *testing.T, oidc WorkloadJWTConfig) tls.Certificate {
	t.Helper()
	block, _ := pem.Decode(oidc.TrustedCAPEM)
	if block == nil {
		t.Fatal("missing test certificate")
	}
	fixtureKeysMu.Lock()
	key := fixtureKeys[string(block.Bytes)]
	fixtureKeysMu.Unlock()
	if key == nil {
		t.Fatal("test certificate key not registered")
	}
	return tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key}
}

var (
	fixtureKeysMu sync.Mutex
	fixtureKeys   = map[string]crypto.PrivateKey{}
)

func registerFixtureKey(f *identityFixture) {
	fixtureKeysMu.Lock()
	fixtureKeys[string(f.server.Certificate().Raw)] = f.server.TLS.Certificates[0].PrivateKey
	fixtureKeysMu.Unlock()
}
func authContext(token string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestSessionRPCAuthenticationPrecedesBrokerState(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	service := &countingService{}
	client, _, _ := startAuthenticatedBroker(t, service, productionOIDC(issuer, time.Minute), port.NopDiagnostics{}, nil)
	calls := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := client.OpenSession(ctx, &brokerv1.OpenSessionRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.InvokeTool(ctx, &brokerv1.InvokeToolRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.CheckAuthorization(ctx, &brokerv1.CheckAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.BeginAuthorization(ctx, &brokerv1.BeginAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.ObserveAuthorization(ctx, &brokerv1.ObserveAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.CancelAuthorization(ctx, &brokerv1.CancelAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.ResumeTool(ctx, &brokerv1.ResumeToolRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.BeginEnrollment(ctx, &brokerv1.BeginEnrollmentRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.ObserveEnrollment(ctx, &brokerv1.ObserveEnrollmentRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.CancelEnrollment(ctx, &brokerv1.CancelEnrollmentRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.DisconnectTools(ctx, &brokerv1.DisconnectToolsRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.DeleteSession(ctx, &brokerv1.DeleteSessionRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.InspectConnectors(ctx, &brokerv1.InspectConnectorsRequest{})
			return err
		},
	}
	valid := authContext(issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil))
	for i, call := range calls {
		if err := call(valid); status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.Unavailable {
			t.Fatalf("authenticated RPC %d: %v", i, err)
		}
	}
	before := service.reads.Load()
	for i, call := range calls {
		if err := call(t.Context()); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("anonymous RPC %d: %v", i, err)
		}
	}
	if service.reads.Load() != before {
		t.Fatal("anonymous calls touched broker state")
	}
}

func TestInitialProductionMCPBroker_Scenario2_RejectsInvalidIdentity(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	service := &countingService{}
	client, _, address := startAuthenticatedBroker(t, service, productionOIDC(issuer, time.Minute), port.NopDiagnostics{}, nil)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"wrong issuer":      issuer.token(t, "https://wrong.example", testAudience, time.Now().Add(time.Minute), nil),
		"wrong audience":    issuer.token(t, issuer.server.URL, "other", time.Now().Add(time.Minute), nil),
		"expired":           issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(-time.Minute), nil),
		"invalid signature": issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), otherKey),
		"malformed":         "not-a-jwt",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.OpenSession(authContext(token), &brokerv1.OpenSessionRequest{}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("invalid identity: %v", err)
			}
		})
	}
	if service.reads.Load() != 0 {
		t.Fatal("invalid identity reached broker state")
	}
	if err := validateTransport("0.0.0.0:8443", nil); err == nil {
		t.Fatal("plaintext public listener accepted")
	}
	var factoryCalls atomic.Int32
	_, err = newBrokerHost(t.Context(), hostConfig{WorkloadJWT: WorkloadJWTConfig{Issuer: "http://issuer.example", Audience: testAudience, TrustedCAPEM: issuer.caPEM(), MaxJWKSStaleness: time.Minute}, Runtime: func(context.Context) (brokerRuntime, error) {
		factoryCalls.Add(1)
		return brokerRuntime{SessionAPI: service}, nil
	}})
	if err == nil || factoryCalls.Load() != 0 {
		t.Fatal("invalid OIDC constructed broker state")
	}
	if _, err := newBrokerHost(t.Context(), hostConfig{SessionAPI: service, WorkloadJWT: productionOIDC(issuer, 0)}); err == nil {
		t.Fatal("unbounded JWKS staleness accepted")
	}
	for _, cfg := range []*tls.Config{
		{RootCAs: x509.NewCertPool(), ServerName: "example.com", MinVersion: tls.VersionTLS13},
		{RootCAs: rootsFromPEM(t, issuer.caPEM()), ServerName: "wrong.example", MinVersion: tls.VersionTLS13},
	} {
		conn, err := tls.Dial("tcp", address, cfg)
		if err == nil {
			_ = conn.Close()
			t.Fatal("invalid server certificate trusted")
		}
	}
}

type diagnosticRecord struct {
	msg  string
	args []any
}
type captureDiagnostics struct {
	mu      sync.Mutex
	records []diagnosticRecord
}

func (d *captureDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, diagnosticRecord{msg, append([]any(nil), args...)})
}
func (d *captureDiagnostics) With(args ...any) port.Diagnostics {
	return &boundDiagnostics{parent: d, args: append([]any(nil), args...)}
}

type boundDiagnostics struct {
	parent *captureDiagnostics
	args   []any
}

func (d *boundDiagnostics) Log(ctx context.Context, l port.Level, msg string, args ...any) {
	d.parent.Log(ctx, l, msg, append(append([]any(nil), d.args...), args...)...)
}
func (d *boundDiagnostics) With(args ...any) port.Diagnostics {
	return &boundDiagnostics{parent: d.parent, args: append(append([]any(nil), d.args...), args...)}
}

func TestSessionTraceOmitsReferencesCredentialsArgumentsAndResults(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	diag := &captureDiagnostics{}
	client, _, _ := startAuthenticatedBroker(t, &countingService{}, productionOIDC(issuer, time.Minute), diag, nil)
	token := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	opened, err := client.OpenSession(authContext(token), &brokerv1.OpenSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.InvokeTool(authContext(token), &brokerv1.InvokeToolRequest{SessionRef: opened.Ref, CatalogueRef: opened.Catalogue.Ref, Call: &brokerv1.Call{Name: "secret-tool", Id: "secret-call", Arguments: []byte("never-log")}})
	diag.mu.Lock()
	encoded := fmt.Sprint(diag.records)
	diag.mu.Unlock()
	for _, forbidden := range []string{token, opened.Ref, "secret-tool", "secret-call", "never-log", issuer.server.URL} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("diagnostics leaked %q", forbidden)
		}
	}
	for _, want := range []string{"broker RPC", "open_session", "invoke_tool", "workload-secret-identity", "outcome"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("diagnostics lack %q", want)
		}
	}
}

func TestInvariant_initial_broker_callback_cannot_supply_authority(t *testing.T) {
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) })
	bundle := mcpbroker.HandlerBundle{Authorization: handler, Token: handler, UpstreamCallback: handler, Discovery: handler, JWKS: handler, ProtectedResource: handler, VMCP: handler, Callback: handler}
	issuer := newIdentityFixture(t)
	srv, err := newBrokerHost(t.Context(), hostConfig{SessionAPI: &countingService{}, WorkloadJWT: productionOIDC(issuer, time.Minute), Handlers: bundle, CallbackPath: "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.close(context.Background()) })
	for _, path := range []string{"/v1/mcp/broker/oauth/authorize", "/v1/mcp/broker/oauth/token", "/v1/mcp/broker/oauth/callback", "/v1/mcp/broker/.well-known/openid-configuration", "/v1/mcp/broker/.well-known/jwks.json", "/v1/mcp/broker/.well-known/oauth-protected-resource", "/v1/mcp/broker/mcp", "/complete?state=opaque&code=code"} {
		w := httptest.NewRecorder()
		srv.httpHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("route %s: %d", path, w.Code)
		}
	}
	if calls.Load() != 8 {
		t.Fatal("fixed routes not mounted")
	}
	catalogue, err := mcpbroker.Compile(mcpauthority.BrokerConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := mcpbroker.New(catalogue, func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	w := httptest.NewRecorder()
	runtime.CallbackHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/complete?state=opaque&code=code&session_id=attacker&owner=attacker&backend=evil&route=evil&principal=admin", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("authority-bearing callback: %d", w.Code)
	}
	wire := strings.ToLower(protodesc.ToFileDescriptorProto(brokerv1.File_mecatl_broker_v1_session_proto).String())
	for _, forbidden := range []string{"principal", "callback_state", "backend", "route", "custody", "broker_incarnation", "handle"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("wire contains internal selector %q", forbidden)
		}
	}
}

func TestInitialProductionMCPBroker_Scenario2_JWKSFailureIsBounded(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	client, _, _ := startAuthenticatedBroker(t, &countingService{}, productionOIDC(issuer, 20*time.Millisecond), port.NopDiagnostics{}, nil)
	token := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	if _, err := client.OpenSession(authContext(token), &brokerv1.OpenSessionRequest{}); err != nil {
		t.Fatal(err)
	}
	issuer.available.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := client.OpenSession(authContext(token), &brokerv1.OpenSessionRequest{})
		if status.Code(err) == codes.Unavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stale keys authoritative: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := client.OpenSession(t.Context(), &brokerv1.OpenSessionRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous outage: %v", err)
	}
	if _, err := newBrokerHost(t.Context(), hostConfig{SessionAPI: &countingService{}, WorkloadJWT: productionOIDC(issuer, maxJWKSStaleness+time.Second)}); err == nil {
		t.Fatal("excessive staleness accepted")
	}
}
func rootsFromPEM(t *testing.T, value []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(value) {
		t.Fatal("append certificate")
	}
	return pool
}
