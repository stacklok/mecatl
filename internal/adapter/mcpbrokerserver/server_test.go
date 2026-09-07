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

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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
	t.Helper()
	if key == nil {
		key = f.key
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "broker-key", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": issuer, "sub": "workload-secret-identity", "aud": audience, "iat": time.Now().Add(-time.Minute).Unix(), "exp": expiry.Unix()})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func productionOIDC(f *identityFixture, staleness time.Duration) OIDCConfig {
	return OIDCConfig{Issuer: f.server.URL, JWKSURI: f.server.URL + "/keys", Audience: testAudience, TrustedCAPEM: f.caPEM(), MaxJWKSStaleness: staleness}
}

type countingService struct{ reads atomic.Int32 }

func (s *countingService) AttachSession(context.Context, session.SessionID) (contract.Attachment, contract.AttachOutcome, error) {
	s.reads.Add(1)
	return &emptyAttachment{}, contract.AttachCreated, nil
}
func (s *countingService) DeleteSession(context.Context, session.SessionID) (contract.DeleteOutcome, error) {
	s.reads.Add(1)
	return contract.DeleteNotFound, nil
}

type emptyAttachment struct{}

func (*emptyAttachment) Binding() session.ExternalBinding { return "binding" }
func (*emptyAttachment) Commit(context.Context) error     { return nil }
func (*emptyAttachment) Abort(context.Context) error      { return nil }
func (*emptyAttachment) Close(context.Context) (contract.CloseOutcome, error) {
	return contract.CloseClosed, nil
}
func (*emptyAttachment) Tools() []tool.Tool { return nil }
func (*emptyAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "", contract.ErrAuthorizationNotFound
}
func (*emptyAttachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return "", contract.ErrAuthorizationNotFound
}
func (*emptyAttachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (contract.CancelOutcome, error) {
	return "", contract.ErrAuthorizationNotFound
}

func startAuthenticatedBroker(t *testing.T, service contract.Service, oidc OIDCConfig, diag port.Diagnostics, observe func(string, string)) (brokerv1.BrokerServiceClient, *grpc.ClientConn, string) {
	t.Helper()
	srv, err := New(t.Context(), Config{Service: service, OIDC: oidc, Diagnostics: diag, Observe: observe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer, err := srv.NewGRPCServer(&tls.Config{Certificates: []tls.Certificate{fCertificate(t, oidc)}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatalf("NewGRPCServer: %v", err)
	}
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(oidc.TrustedCAPEM)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return brokerv1.NewBrokerServiceClient(conn), conn, listener.Addr().String()
}

func fCertificate(t *testing.T, oidc OIDCConfig) tls.Certificate {
	t.Helper()
	// Tests use the identity fixture's httptest certificate as the broker leaf.
	block, _ := pem.Decode(oidc.TrustedCAPEM)
	if block == nil {
		t.Fatal("missing test certificate")
	}
	// Locate the matching fixture key through the registry populated by newIdentityFixture.
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

func TestSingletonBrokerRemediation_Scenario3_PublicRPCAuthenticationPrecedesBrokerState(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	service := &countingService{}
	client, _, _ := startAuthenticatedBroker(t, service, productionOIDC(issuer, time.Minute), port.NopDiagnostics{}, nil)
	token := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)

	calls := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := client.Attach(ctx, &brokerv1.AttachRequest{SessionId: "session"})
			return err
		},
		func(ctx context.Context) error { _, err := client.Commit(ctx, &brokerv1.CommitRequest{}); return err },
		func(ctx context.Context) error { _, err := client.Abort(ctx, &brokerv1.AbortRequest{}); return err },
		func(ctx context.Context) error { _, err := client.Close(ctx, &brokerv1.CloseRequest{}); return err },
		func(ctx context.Context) error {
			_, err := client.Delete(ctx, &brokerv1.DeleteRequest{SessionId: "missing"})
			return err
		},
		func(ctx context.Context) error { _, err := client.Execute(ctx, &brokerv1.ExecuteRequest{}); return err },
		func(ctx context.Context) error {
			_, err := client.RequestAuthorization(ctx, &brokerv1.RequestAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.AbortAuthorization(ctx, &brokerv1.AbortAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.PresentAuthorization(ctx, &brokerv1.PresentAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.AuthorizationStatus(ctx, &brokerv1.AuthorizationStatusRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.CancelAuthorization(ctx, &brokerv1.CancelAuthorizationRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.BeginWorkspaceEnrollment(ctx, &brokerv1.BeginWorkspaceEnrollmentRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.ObserveWorkspaceEnrollment(ctx, &brokerv1.ObserveWorkspaceEnrollmentRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := client.CancelWorkspaceEnrollment(ctx, &brokerv1.CancelWorkspaceEnrollmentRequest{})
			return err
		},
	}
	for i, call := range calls {
		if err := call(authContext(token)); status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.Unavailable {
			t.Fatalf("authenticated RPC %d rejected at authentication: %v", i, err)
		}
	}
	before := service.reads.Load()
	for i, call := range calls {
		if err := call(context.Background()); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("anonymous RPC %d code = %s, want Unauthenticated", i, status.Code(err))
		}
	}
	if got := service.reads.Load(); got != before {
		t.Fatalf("anonymous calls touched broker state: reads %d -> %d", before, got)
	}
}

func TestInitialProductionMCPBroker_Scenario2_RejectsInvalidIdentity(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	service := &countingService{}
	client, _, brokerAddress := startAuthenticatedBroker(t, service, productionOIDC(issuer, time.Minute), port.NopDiagnostics{}, nil)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	for name, token := range map[string]string{
		"wrong issuer":      issuer.token(t, "https://wrong.example", testAudience, time.Now().Add(time.Minute), nil),
		"wrong audience":    issuer.token(t, issuer.server.URL, "other", time.Now().Add(time.Minute), nil),
		"expired":           issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(-time.Minute), nil),
		"invalid signature": issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), otherKey),
		"malformed":         "not-a-jwt",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.Attach(authContext(token), &brokerv1.AttachRequest{SessionId: "secret-session"})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("code = %s, want Unauthenticated: %v", status.Code(err), err)
			}
		})
	}
	if service.reads.Load() != 0 {
		t.Fatal("invalid workload identity reached broker state")
	}
	if err := ValidateTransport("0.0.0.0:8443", nil); err == nil {
		t.Fatal("plaintext non-loopback listener was accepted")
	}
	var factoryCalls atomic.Int32
	badOIDC := Config{OIDC: OIDCConfig{Issuer: "http://issuer.example", Audience: testAudience, TrustedCAPEM: issuer.caPEM(), MaxJWKSStaleness: time.Minute}, Factory: func(context.Context) (contract.Service, mcpbroker.HandlerBundle, string, func() error, error) {
		factoryCalls.Add(1)
		return &countingService{}, mcpbroker.HandlerBundle{}, "", nil, nil
	}}
	if _, err := New(t.Context(), badOIDC); err == nil {
		t.Fatal("production accepted an HTTP issuer")
	}
	if factoryCalls.Load() != 0 {
		t.Fatal("broker state was constructed before identity configuration admission")
	}
	if _, err := New(t.Context(), Config{Service: &countingService{}, OIDC: productionOIDC(issuer, 0)}); err == nil {
		t.Fatal("production accepted unbounded JWKS staleness")
	}
	for _, tlsConfig := range []*tls.Config{
		{RootCAs: x509.NewCertPool(), ServerName: "example.com", MinVersion: tls.VersionTLS13},
		{RootCAs: rootsFromPEM(t, issuer.caPEM()), ServerName: "wrong.example", MinVersion: tls.VersionTLS13},
	} {
		conn, err := tls.Dial("tcp", brokerAddress, tlsConfig)
		if err == nil {
			_ = conn.Close()
			t.Fatal("invalid server certificate was trusted")
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

func TestInvariant_initial_broker_observability_is_bounded_and_secret_free(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	diag := &captureDiagnostics{}
	var labels []string
	client, _, _ := startAuthenticatedBroker(t, &countingService{}, productionOIDC(issuer, time.Minute), diag, func(op, outcome string) { labels = append(labels, op+":"+outcome) })
	credential := "bearer-secret-credential"
	callbackState := "callback-state-secret"
	presentationURL := "https://present.example/secret"
	toolArguments := credential + callbackState + presentationURL
	validToken := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	_, _ = client.Execute(authContext(validToken), &brokerv1.ExecuteRequest{Name: "secret-tool", Args: []byte(toolArguments)})
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+credential))
	_, _ = client.Execute(ctx, &brokerv1.ExecuteRequest{Name: "secret-tool", Args: []byte(toolArguments)})
	encoded := fmt.Sprint(diag.records)
	for _, secret := range []string{credential, callbackState, presentationURL, toolArguments, "workload-secret-identity", issuer.server.URL} {
		if strings.Contains(encoded, secret) || strings.Contains(strings.Join(labels, "|"), secret) {
			t.Fatalf("secret %q reached observability: diagnostics=%s labels=%v", secret, encoded, labels)
		}
	}
	allowedOps := map[string]bool{"execute": true}
	allowedOutcomes := map[string]bool{"unauthenticated": true, "unavailable": true, "allowed": true}
	for _, label := range labels {
		parts := strings.Split(label, ":")
		if len(parts) != 2 || !allowedOps[parts[0]] || !allowedOutcomes[parts[1]] {
			t.Fatalf("unbounded metric label %q", label)
		}
	}
}

func TestInvariant_initial_broker_callback_cannot_supply_authority(t *testing.T) {
	var callbackCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	bundle := mcpbroker.HandlerBundle{Authorization: handler, Token: handler, UpstreamCallback: handler, Discovery: handler, JWKS: handler, ProtectedResource: handler, VMCP: handler, Callback: handler}
	issuer := newIdentityFixture(t)
	srv, err := New(t.Context(), Config{Service: &countingService{}, OIDC: productionOIDC(issuer, time.Minute), Handlers: bundle, CallbackPath: "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	for _, path := range []string{"/v1/mcp/broker/oauth/authorize", "/v1/mcp/broker/oauth/token", "/v1/mcp/broker/oauth/callback", "/v1/mcp/broker/.well-known/openid-configuration", "/v1/mcp/broker/.well-known/jwks.json", "/v1/mcp/broker/.well-known/oauth-protected-resource", "/v1/mcp/broker/mcp", "/complete?state=opaque&code=code"} {
		recorder := httptest.NewRecorder()
		srv.HTTPHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("route %s status=%d", path, recorder.Code)
		}
	}
	if callbackCalls.Load() != 8 {
		t.Fatalf("mounted routes called %d times, want 8", callbackCalls.Load())
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
	response := httptest.NewRecorder()
	runtime.CallbackHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/complete?state=opaque&code=code&session_id=attacker&owner=attacker&backend=evil&route=evil&principal=admin", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("authority-bearing callback status = %d, want 400", response.Code)
	}

	wire := strings.ToLower(protodesc.ToFileDescriptorProto(brokerv1.File_mecatl_broker_v1_broker_proto).String())
	for _, forbidden := range []string{"owner", "principal", "callback_state", "backend", "route"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("client protocol contains authority selector %q", forbidden)
		}
	}
}

func TestInitialProductionMCPBroker_Scenario2_JWKSFailureIsBounded(t *testing.T) {
	issuer := newIdentityFixture(t)
	registerFixtureKey(issuer)
	client, _, _ := startAuthenticatedBroker(t, &countingService{}, productionOIDC(issuer, 20*time.Millisecond), port.NopDiagnostics{}, nil)
	token := issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	if _, err := client.Attach(authContext(token), &brokerv1.AttachRequest{SessionId: "first"}); err != nil {
		t.Fatalf("initial auth: %v", err)
	}
	issuer.available.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := client.Attach(authContext(token), &brokerv1.AttachRequest{SessionId: "later"})
		if status.Code(err) == codes.Unavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stale key remained authoritative: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := client.Attach(context.Background(), &brokerv1.AttachRequest{SessionId: "anonymous"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("outage downgraded to anonymous access: %v", err)
	}
	cfg := productionOIDC(issuer, maxJWKSStaleness+time.Second)
	if _, err := New(t.Context(), Config{Service: &countingService{}, OIDC: cfg}); err == nil {
		t.Fatal("excessive JWKS staleness was accepted")
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
