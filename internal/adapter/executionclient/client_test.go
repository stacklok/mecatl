//nolint:revive // Test doubles mirror the private protocol's complete method set.
package executionclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type integrationBackend struct {
	allocation                          executioncontroller.Allocation
	ensureCalls, attachCalls, fileCalls int
	expireNextRead                      bool
}

func (*integrationBackend) ValidateProfile(context.Context, string) (executioncontroller.Profile, error) {
	return executioncontroller.Profile{Name: "coding", Digest: "sha256:test"}, nil
}
func (b *integrationBackend) Ensure(_ context.Context, client, owner, binding, _, _ string) (executioncontroller.Allocation, error) {
	b.ensureCalls++
	b.allocation = executioncontroller.Allocation{Environment: executionenv.EnvironmentRef{ID: "env-real", Revision: "rev-1"}, Epoch: 1, OwnerHash: owner, BindingID: binding, Client: client, Ready: true}
	return b.allocation, nil
}
func (b *integrationBackend) Attach(_ context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) (executioncontroller.Allocation, error) {
	b.attachCalls++
	if ref != b.allocation.Environment || client != b.allocation.Client || owner != b.allocation.OwnerHash || binding != b.allocation.BindingID {
		return executioncontroller.Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "not found"}
	}
	return b.allocation, nil
}
func (*integrationBackend) ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error {
	return nil
}
func (*integrationBackend) Retire(context.Context, executionenv.EnvironmentRef, string) error {
	return nil
}
func (b *integrationBackend) File(_ context.Context, _, _ string, q executionenv.FileRequest) (executionenv.FileResponse, error) {
	b.fileCalls++
	if b.expireNextRead && q.Operation == executionenv.OpFileRead {
		b.expireNextRead = false
		return executionenv.FileResponse{}, &executionenv.Error{Code: executionenv.CodeUnauthenticated, Message: "expired grant", Retryable: true}
	}
	if q.Operation == executionenv.OpFileResolveAuthority {
		return executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"}, nil
	}
	return executionenv.FileResponse{Data: []byte("from-grpc"), Version: string([]byte{0xff, 0, 1})}, nil
}
func (*integrationBackend) StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	return executionenv.CommandStartResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Result: executionenv.CommandStatusResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Stdout: []byte("ok\n")}}, nil
}
func (*integrationBackend) CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, &executionenv.Error{Code: executionenv.CodeInternal, Message: "unimplemented"}
}
func (*integrationBackend) CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, &executionenv.Error{Code: executionenv.CodeInternal, Message: "unimplemented"}
}

func (b *integrationBackend) EnsurePending(ctx context.Context, client, owner, binding, profile, fp, _ string) (executioncontroller.Allocation, error) {
	return b.Ensure(ctx, client, owner, binding, profile, fp)
}
func (b *integrationBackend) AcquireRun(_ context.Context, ref executionenv.EnvironmentRef, client, owner, binding, run, _ string, ttl time.Duration) (executionenv.RunClaim, error) {
	return executionenv.RunClaim{Environment: ref, BindingID: binding, RunID: run, ClaimID: "claim", Epoch: b.allocation.Epoch + 1, GrantGeneration: 1, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (b *integrationBackend) RenewRun(_ context.Context, _ executionenv.EnvironmentRef, _ string, _ string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	return executionenv.RunClaim{Environment: req.Environment, BindingID: req.BindingID, RunID: req.RunID, ClaimID: req.ClaimID, Epoch: req.Epoch, GrantGeneration: 1, ExpiresAt: time.Now().Add(req.TTL)}, nil
}
func (*integrationBackend) ReleaseRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) error {
	return nil
}
func (*integrationBackend) CommitReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*integrationBackend) AbortReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*integrationBackend) ReserveSuccessor(context.Context, executionenv.EnvironmentRef, string, string, string, string, string) error {
	return nil
}
func (*integrationBackend) PrepareReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*integrationBackend) ConfirmReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*integrationBackend) CancelReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*integrationBackend) ListReferenceIntents(context.Context, string, string, int) ([]executionenv.ReferenceIntent, error) {
	return nil, nil
}
func (*integrationBackend) FindReferenceIntent(context.Context, executionenv.EnvironmentRef, string, string, string) (executionenv.ReferenceIntent, error) {
	return executionenv.ReferenceIntent{}, nil
}

func certificate(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, serverName string, client bool) (tls.Certificate, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: serverName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: parent == nil, BasicConstraintsValid: true}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		u, _ := url.Parse("spiffe://example.test/mecak8s")
		tmpl.URIs = []*url.URL{u}
	} else if parent != nil {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{"example.test"}
	}
	issuer, signer := tmpl, key
	if parent != nil {
		issuer, signer = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, parsed, key
}

type grpcFixture struct {
	endpoint  string
	clientTLS *tls.Config
	stop      func()
}

func startFixture(t *testing.T, backend executioncontroller.Backend, ready func() bool) grpcFixture {
	t.Helper()
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	serverCert, _, _ := certificate(t, ca, caKey, "example.test", false)
	clientCert, _, _ := certificate(t, ca, caKey, "client", true)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	h := executioncontroller.NewHandler(executioncontroller.HandlerConfig{Clients: map[string]executioncontroller.ClientPolicy{"spiffe://example.test/mecak8s": {MayAttestOwner: true}}, Signer: executioncontroller.GrantSigner{KeyID: "k1", PrivateKey: private, Issuer: "provider", Audience: "executor", Lifetime: time.Minute}, Verifier: executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": private.Public().(ed25519.PublicKey)}, Issuer: "provider", Audience: "executor", MaxLifetime: 2 * time.Minute}, Ready: ready}, backend)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(executioncontroller.TLSConfig(serverCert, pool))), grpc.MaxRecvMsgSize(executionenv.MaxMessageBytes), grpc.MaxSendMsgSize(executionenv.MaxMessageBytes))
	executionv1.RegisterExecutionProviderServiceServer(s, h)
	go func() { _ = s.Serve(ln) }()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return grpcFixture{endpoint: ln.Addr().String(), clientTLS: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{clientCert}, ServerName: "example.test"}, stop: func() { s.Stop(); _ = ln.Close() }}
}

func TestProviderThroughRealGRPCSignedHandlerRefreshesAndReattachesExactly(t *testing.T) {
	backend := &integrationBackend{}
	fx := startFixture(t, backend, nil)
	defer fx.stop()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider, _ := NewProvider(client, "coding")
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: principal, BindingID: "session-real"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Commit != nil {
		if err := binding.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	reattached, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Principal: principal, BindingID: "session-real"})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.AcquireRun(context.Background(), server.ExecutionRunRequest{Ref: reattached.Ref, Principal: principal, BindingID: "session-real", RunID: "run-real"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release(context.Background())
	runEnv := handle.Environment()
	resolver := runEnv.Workspace().(tool.AuthorityResourceResolver)
	target, root, err := resolver.AuthorityResourcePath("main.go")
	if err != nil || target != "/workspace/main.go" || root != "/workspace" {
		t.Fatalf("authority=(%q,%q) err=%v", target, root, err)
	}
	data, version, err := runEnv.Workspace().ReadVersion(context.Background(), "main.go")
	if err != nil || string(data) != "from-grpc" {
		t.Fatalf("read=%q err=%v", data, err)
	}
	encoded, err := tool.EncodeFileVersion(version)
	if err != nil || encoded != string([]byte{0xff, 0, 1}) {
		t.Fatalf("opaque version=%q err=%v", encoded, err)
	}
	result, err := runEnv.CommandRunner().Run(context.Background(), "go test ./...")
	if err != nil || result.Stdout != "ok\n" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if backend.ensureCalls != 1 || backend.attachCalls < 2 || backend.fileCalls != 2 {
		t.Fatalf("ensure=%d attach=%d file=%d", backend.ensureCalls, backend.attachCalls, backend.fileCalls)
	}
}

func TestCommandStatusIsUnimplementedOnlyAfterAuthorization(t *testing.T) {
	backend := &integrationBackend{}
	fx := startFixture(t, backend, nil)
	defer fx.stop()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	ensured, err := client.Ensure(context.Background(), "binding", "coding", owner, "create")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := client.AcquireRun(context.Background(), executionenv.RunClaimRequest{Environment: ensured.Environment, Owner: owner, BindingID: "binding", RunID: "run", OperationID: "acquire", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: "binding", RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Grant: claim.Grant}
	_, err = client.rpc.CommandStatus(context.Background(), &executionv1.CommandQueryRequest{Context: contextToProto(rc), CommandId: "c1"})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("authorized status code=%v", status.Code(err))
	}
	rc.Owner.Subject = "mallory"
	_, err = client.rpc.CommandStatus(context.Background(), &executionv1.CommandQueryRequest{Context: contextToProto(rc), CommandId: "c1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong-owner status code=%v", status.Code(err))
	}
}

func TestGRPCReadinessAndMTLSAreMandatory(t *testing.T) {
	fx := startFixture(t, &integrationBackend{}, func() bool { return false })
	defer fx.stop()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.ValidateProfile(context.Background(), "coding")
	var remote *executionenv.Error
	if !errors.As(err, &remote) || remote.Code != executionenv.CodeNotReady {
		t.Fatalf("error=%v", err)
	}
	noCert := fx.clientTLS.Clone()
	noCert.Certificates = nil
	conn, err := grpc.NewClient(fx.endpoint, grpc.WithTransportCredentials(credentials.NewTLS(noCert)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := executionv1.NewExecutionProviderServiceClient(conn).ValidateProfile(ctx, &executionv1.ValidateProfileRequest{Profile: "coding"}); err == nil {
		t.Fatal("server accepted client without certificate")
	}
}

func TestDecodeErrorRejectsUntrustedOrContradictoryMetadata(t *testing.T) {
	const marker = "/var/run/secrets/provider-key"
	typed := func(code executionenv.ErrorCode, retry bool, grpcCode codes.Code) error {
		st, err := status.New(grpcCode, marker).WithDetails(&executionv1.ErrorDetail{Code: string(code), Retryable: retry})
		if err != nil {
			t.Fatal(err)
		}
		return st.Err()
	}
	multiple, err := status.New(codes.NotFound, marker).WithDetails(
		&executionv1.ErrorDetail{Code: string(executionenv.CodeNotFound)},
		&executionv1.ErrorDetail{Code: string(executionenv.CodePermissionDenied)},
	)
	if err != nil {
		t.Fatal(err)
	}
	cases := []error{
		errors.New(marker),
		status.Error(codes.Internal, marker),
		typed("", false, codes.Internal),
		typed(executionenv.ErrorCode("future"), false, codes.Internal),
		typed(executionenv.CodeNotFound, false, codes.PermissionDenied),
		typed(executionenv.CodeNotFound, true, codes.NotFound),
		multiple.Err(),
	}
	for i, input := range cases {
		got := decodeError(context.Background(), input)
		var remote *executionenv.Error
		if !errors.As(got, &remote) || remote.Code != executionenv.CodeInternal || remote.Retryable || strings.Contains(got.Error(), marker) {
			t.Fatalf("case %d escaped or misclassified: %v", i, got)
		}
	}
}

func TestDecodeErrorAcceptsClosedTypedErrorAndLocalCancellation(t *testing.T) {
	st, err := status.New(codes.Unauthenticated, "peer text").WithDetails(&executionv1.ErrorDetail{Code: string(executionenv.CodeUnauthenticated), Retryable: true})
	if err != nil {
		t.Fatal(err)
	}
	var remote *executionenv.Error
	if got := decodeError(context.Background(), st.Err()); !errors.As(got, &remote) || remote.Code != executionenv.CodeUnauthenticated || !remote.Retryable {
		t.Fatalf("typed error=%v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := decodeError(ctx, status.Error(codes.Canceled, "peer text")); !errors.Is(got, context.Canceled) {
		t.Fatalf("local cancellation=%v", got)
	}
}

func TestCommandResponsesRejectUnknownStates(t *testing.T) {
	if _, err := commandStartFromProto(&executionv1.CommandStartResponse{State: executionv1.CommandState_COMMAND_STATE_UNSPECIFIED, Result: &executionv1.CommandStatusResponse{State: executionv1.CommandState_COMMAND_STATE_SUCCEEDED}}); err == nil {
		t.Fatal("accepted unspecified command-start state")
	}
	if _, err := commandStatusFromProto(&executionv1.CommandStatusResponse{State: executionv1.CommandState(99), TerminalReceipt: "clean"}); err == nil {
		t.Fatal("accepted unknown command-status state")
	}
}

func TestEndpointRejectsHTTPAndAlternateResolvers(t *testing.T) {
	cfg := &tls.Config{RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{Certificate: [][]byte{{1}}}}}
	for _, endpoint := range []string{"https://provider:8443", "dns:///provider:8443", "provider"} {
		if c, err := New(endpoint, cfg); err == nil {
			c.Close()
			t.Fatalf("accepted %q", endpoint)
		}
	}
}
