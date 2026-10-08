//nolint:revive // Test doubles mirror the private protocol's complete method set.
package executionclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
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
	claim                               executionenv.RunClaim
	ensureCalls, attachCalls, fileCalls int
	expireNextRead                      bool
}

const codingRevision = "v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (*integrationBackend) ValidateTemplate(context.Context, string, string) (executioncontroller.Profile, error) {
	return executioncontroller.Profile{Name: "coding", Digest: "sha256:test"}, nil
}
func (*integrationBackend) CatalogTemplates() []*executionv1.TemplateCatalogItem {
	return []*executionv1.TemplateCatalogItem{{Template: &executionv1.TemplateSelector{Id: "coding", Revision: codingRevision}}}
}
func (b *integrationBackend) EnsurePendingOwnedRevision(ctx context.Context, client, owner string, _ executionenv.Owner, binding, id, revision, fp, operationID string) (executioncontroller.Allocation, error) {
	if id != "coding" || revision != codingRevision {
		return executioncontroller.Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "execution template not found"}
	}
	return b.EnsurePending(ctx, client, owner, binding, id, fp, operationID)
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
		return executionenv.FileResponse{}, &executionenv.Error{Code: executionenv.CodeUnauthenticated, Message: "lease renewal required", Retryable: true}
	}
	if q.Operation == executionenv.OpFileResolveAuthority {
		return executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"}, nil
	}
	return executionenv.FileResponse{Data: []byte("from-grpc"), Version: string([]byte{0xff, 0, 1})}, nil
}
func (*integrationBackend) StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	return executionenv.CommandStartResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Result: executionenv.CommandStatusResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Stdout: []byte("ok\n\xff"), Stderr: []byte("err\xe2")}}, nil
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
	if ref != b.allocation.Environment || client != b.allocation.Client || owner != b.allocation.OwnerHash || binding != b.allocation.BindingID {
		return executionenv.RunClaim{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "binding unavailable"}
	}
	b.claim = executionenv.RunClaim{Environment: ref, BindingID: binding, RunID: run, ClaimID: "claim", Epoch: b.allocation.Epoch + 1, GrantGeneration: 1, ExpiresAt: time.Now().Add(ttl)}
	return b.claim, nil
}
func (b *integrationBackend) ValidateRunClaim(_ context.Context, client, owner string, rc executionenv.RequestContext) error {
	if client != b.allocation.Client || owner != b.allocation.OwnerHash || rc.Environment != b.claim.Environment || rc.BindingID != b.claim.BindingID || rc.RunID != b.claim.RunID || rc.ClaimID != b.claim.ClaimID || rc.Epoch != b.claim.Epoch || rc.GrantGeneration != b.claim.GrantGeneration || !time.Now().Before(b.claim.ExpiresAt) {
		return &executionenv.Error{Code: executionenv.CodePermissionDenied, Message: "run unavailable"}
	}
	return nil
}
func (b *integrationBackend) RenewRun(_ context.Context, _ executionenv.EnvironmentRef, _ string, _ string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	b.claim = executionenv.RunClaim{Environment: req.Environment, BindingID: req.BindingID, RunID: req.RunID, ClaimID: req.ClaimID, Epoch: req.Epoch, GrantGeneration: 1, ExpiresAt: time.Now().Add(req.TTL)}
	return b.claim, nil
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
		if serverName == "localhost" {
			tmpl.DNSNames = []string{"localhost"}
		} else {
			tmpl.DNSNames = []string{"example.test"}
		}
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
	h := executioncontroller.NewHandler(executioncontroller.HandlerConfig{Clients: map[string]executioncontroller.ClientPolicy{"spiffe://example.test/mecak8s": {MayAttestOwner: true, ExecutionTemplates: []string{"coding"}}}, Ready: ready}, backend)
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

func TestReloadTLSClosesStaleConnectionOnIncompleteProjectionAndRecovers(t *testing.T) {
	dir := t.TempDir()
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	clientCert, _, clientKey := certificate(t, ca, caKey, "client", true)
	files := TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key")}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(files.CA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))
	write(files.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Certificate[0]}))
	keyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	write(files.Key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	c, err := NewWithTLSFiles("localhost:8443", files)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	original := c.conn.Load()
	if err := c.ReloadTLS(); err != nil || c.conn.Load() != original {
		t.Fatalf("unchanged projection replaced transport: %v", err)
	}
	write(files.Key, []byte("incomplete"))
	if err := c.ReloadTLS(); err == nil || c.conn.Load() != nil {
		t.Fatal("partial certificate/key projection retained live transport")
	}
	if err := c.Invoke(t.Context(), "/test", nil, nil); status.Code(err) != codes.Unavailable {
		t.Fatalf("stale transport remained callable: %v", err)
	}
	write(files.Key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err := c.ReloadTLS(); err != nil || c.conn.Load() == nil || c.conn.Load() == original {
		t.Fatalf("valid projection did not restore fresh transport: %v", err)
	}
	newCert, _, _ := certificate(t, ca, caKey, "client", true)
	write(files.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newCert.Certificate[0]}))
	if err := c.ReloadTLS(); err == nil || c.conn.Load() != nil {
		t.Fatal("mismatched renewed leaf and key retained live transport")
	}
}

func TestTemplateProviderRequiresExactRevision(t *testing.T) {
	for _, revision := range []string{"", "v1-" + strings.Repeat("z", 64), "v1-" + strings.Repeat("A", 64), "v1-" + strings.Repeat("a", 63)} {
		if _, err := NewTemplateProvider(&Client{}, "coding", revision); err == nil {
			t.Fatalf("malformed revision %q accepted", revision)
		}
	}
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
	provider, _ := NewTemplateProvider(client, "coding", codingRevision)
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
	if err != nil || result.Stdout != "ok\n�" || result.Stderr != "err�" {
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
	ensured, err := client.EnsureTemplate(context.Background(), "binding", "coding", codingRevision, owner, "create")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := client.AcquireRun(context.Background(), executionenv.RunClaimRequest{Environment: ensured.Environment, Owner: owner, BindingID: "binding", RunID: "run", OperationID: "acquire", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: "binding", RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}
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
	_, err = client.ValidateTemplate(context.Background(), "coding", codingRevision)
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
	if _, err := executionv1.NewExecutionProviderServiceClient(conn).ValidateTemplate(ctx, &executionv1.ValidateTemplateRequest{Template: &executionv1.TemplateSelector{Id: "coding", Revision: codingRevision}}); err == nil {
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
