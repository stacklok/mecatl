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
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type integrationBackend struct {
	mu                                    sync.Mutex
	allocation                            executioncontroller.Allocation
	claim                                 executionenv.RunClaim
	ensureCalls, attachCalls, renewCalls  int
	fileRoutes, commandRoutes, staleRoute int
}

const codingRevision = "v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (*integrationBackend) ValidateTemplate(context.Context, string, string) (executioncontroller.Profile, error) {
	return executioncontroller.Profile{Name: "coding", Digest: "sha256:test"}, nil
}
func (*integrationBackend) CatalogTemplates() []*executionv1.TemplateCatalogItem {
	return []*executionv1.TemplateCatalogItem{{Template: &executionv1.TemplateSelector{Id: "coding", Revision: codingRevision}}}
}
func (b *integrationBackend) EnsurePendingOwnedRevision(
	ctx context.Context,
	client string,
	owner string,
	_ executionenv.Owner,
	binding string,
	id string,
	revision string,
	fp string,
	operationID string,
	_ []string,
) (executioncontroller.Allocation, error) {
	if id != "coding" || revision != codingRevision {
		return executioncontroller.Allocation{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "execution template not found"}
	}
	return b.EnsurePending(ctx, client, owner, binding, id, fp, operationID)
}
func (b *integrationBackend) Ensure(_ context.Context, client, owner, binding, _, _ string) (executioncontroller.Allocation, error) {
	b.ensureCalls++
	b.allocation = executioncontroller.Allocation{
		Environment:          executionenv.EnvironmentRef{ID: "env-real", Revision: "rev-1"},
		Epoch:                1,
		OwnerHash:            owner,
		BindingID:            binding,
		Client:               client,
		Ready:                true,
		AdmittedCapabilities: executionenv.TemplateCapabilities(),
	}
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

// ExecutorRoute refuses the next staleRoute calls as a stale run claim.
func (b *integrationBackend) ExecutorRoute(
	_ context.Context,
	client string,
	owner string,
	rc executionenv.RequestContext,
	capability string,
) (executioncontroller.ExecutorRoute, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if client != b.allocation.Client || owner != b.allocation.OwnerHash || rc.Environment != b.claim.Environment || rc.BindingID != b.claim.BindingID || rc.RunID != b.claim.RunID || rc.ClaimID != b.claim.ClaimID || rc.Epoch != b.claim.Epoch || rc.GrantGeneration != b.claim.GrantGeneration || !time.Now().Before(b.claim.ExpiresAt) {
		return executioncontroller.ExecutorRoute{}, &executionenv.Error{
			Code:    executionenv.CodePermissionDenied,
			Message: "run unavailable",
		}
	}
	if b.staleRoute > 0 {
		b.staleRoute--
		return executioncontroller.ExecutorRoute{}, &executionenv.Error{
			Code:      executionenv.CodeUnauthenticated,
			Message:   "run claim is not current",
			Retryable: true,
		}
	}
	if capability == executionenv.CapabilityFilesystem {
		b.fileRoutes++
	} else {
		b.commandRoutes++
	}
	return executioncontroller.ExecutorRoute{
		MaxFileBytes:       executionenv.MaxFileBytes,
		MaxCommandBytes:    executionenv.MaxCommandBytes,
		MaxCommandDuration: time.Minute,
	}, nil
}
func (b *integrationBackend) RenewRun(_ context.Context, _ executionenv.EnvironmentRef, _ string, _ string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renewCalls++
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
func (*integrationBackend) ReserveSuccessor(
	context.Context,
	executionenv.EnvironmentRef,
	string,
	string,
	string,
	string,
	string,
) ([]string, error) {
	return executionenv.TemplateCapabilities(), nil
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

// startFixture serves the provider; file and command calls reach executor, if given.
func startFixture(
	t *testing.T,
	backend executioncontroller.Backend,
	ready func() bool,
	executor ...grpc.ClientConnInterface,
) grpcFixture {
	t.Helper()
	var conn grpc.ClientConnInterface
	if len(executor) > 0 {
		conn = executor[0]
	}
	return startProvider(t, backend, ready, true, conn)
}

func startProvider(
	t *testing.T,
	backend executioncontroller.Backend,
	ready func() bool,
	executorServices bool,
	executor grpc.ClientConnInterface,
) grpcFixture {
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
	if executorServices {
		connect := func(executioncontroller.ExecutorPin) (grpc.ClientConnInterface, func(), error) {
			if executor == nil {
				return nil, nil, errors.New("no executor")
			}
			return executor, func() {}, nil
		}
		executioncontroller.RegisterExecutorServices(s, h, connect)
	}
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

type exhaustedStatusBackend struct{ integrationBackend }

func (*exhaustedStatusBackend) EnsurePendingOwnedRevision(
	context.Context,
	string,
	string,
	executionenv.Owner,
	string,
	string,
	string,
	string,
	string,
	[]string,
) (executioncontroller.Allocation, error) {
	return executioncontroller.Allocation{}, &executionenv.Error{Code: executionenv.CodeConflict, Message: "PRIVATE-STATUS-CONFLICT"}
}

func TestEnsureTemplateConflictSurvivesHandlerAndClient(t *testing.T) {
	fx := startFixture(t, &exhaustedStatusBackend{}, nil)
	defer fx.stop()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.EnsureTemplate(t.Context(), "binding", "coding", codingRevision, executionenv.Owner{Issuer: "issuer", Subject: "alice"}, "op")
	var remote *executionenv.Error
	if !errors.As(err, &remote) || remote.Code != executionenv.CodeConflict || remote.Retryable || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("conflict not preserved through gRPC: %v", err)
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
	executor := &recordingExecutor{
		data:    []byte("from-grpc"),
		version: []byte{0xff, 0, 1},
		stdout:  "ok\n\xff",
		stderr:  "err\xe2",
	}
	fx := startFixture(t, backend, nil, executor.conn(t))
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
	if backend.ensureCalls != 1 || backend.attachCalls < 2 || backend.fileRoutes != 2 {
		t.Fatalf(
			"ensure=%d attach=%d file=%d",
			backend.ensureCalls,
			backend.attachCalls,
			backend.fileRoutes,
		)
	}
}

func TestReadOnlyFileCallsRenewAStaleClaimOnce(t *testing.T) {
	backend := &integrationBackend{}
	fx := startFixture(t, backend, nil, (&recordingExecutor{}).conn(t))
	t.Cleanup(fx.stop)
	ws := acquireRemoteRun(t, fx).Workspace()
	backend.mu.Lock()
	backend.staleRoute = 1
	backend.mu.Unlock()
	data, err := ws.Read(t.Context(), "main.go")
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("read after a stale claim = %q, %v", data, err)
	}
	backend.mu.Lock()
	renewed, reads := backend.renewCalls, backend.fileRoutes
	backend.staleRoute = 2
	backend.mu.Unlock()
	if renewed != 1 || reads != 1 {
		t.Fatalf("renewals=%d forwarded reads=%d, want one each", renewed, reads)
	}
	_, err = ws.Read(t.Context(), "main.go")
	if refusal := Refusal(err); refusal == nil || refusal.Code != executionenv.CodeUnauthenticated {
		t.Fatalf("read refused twice = %v, want the stale-claim refusal", err)
	}
	backend.mu.Lock()
	backend.staleRoute = 1
	backend.mu.Unlock()
	if _, err := ws.CreateFile(t.Context(), "new.go", []byte("x")); Refusal(err) == nil {
		t.Fatalf("write after a stale claim = %v, want the refusal unretried", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.renewCalls != 2 || backend.fileRoutes != 1 {
		t.Fatalf(
			"renewals=%d forwarded=%d after the refusals",
			backend.renewCalls,
			backend.fileRoutes,
		)
	}
}

func TestConcurrentFileCallsReachTheExecutorTogether(t *testing.T) {
	const calls = 4
	release := make(chan struct{})
	executor := &recordingExecutor{executed: make(chan executorCall, calls), release: release}
	fx := startFixture(t, &integrationBackend{}, nil, executor.conn(t))
	t.Cleanup(fx.stop)
	ws := acquireRemoteRun(t, fx).Workspace()
	done := make(chan error, calls)
	for range calls {
		go func() {
			_, err := ws.Read(t.Context(), "main.go")
			done <- err
		}()
	}
	for range calls {
		select {
		case <-executor.executed:
		case err := <-done:
			t.Fatalf("read finished while others were held: %v", err)
		}
	}
	close(release)
	for range calls {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentCreatesAndReplacesThroughTheProvider(t *testing.T) {
	fx := startFixture(t, &integrationBackend{}, nil, osfsExecutor(t, t.TempDir()))
	t.Cleanup(fx.stop)
	ws := acquireRemoteRun(t, fx).Workspace()
	if _, err := ws.CreateFile(t.Context(), "shared.txt", []byte("base")); err != nil {
		t.Fatal(err)
	}
	_, base, err := ws.ReadVersion(t.Context(), "shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	var replaced, mismatched atomic.Int32
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("file-%d.txt", i)
			if _, err := ws.CreateFile(t.Context(), path, []byte(path)); err != nil {
				errs <- err
				return
			}
			if data, err := ws.Read(t.Context(), path); err != nil || string(data) != path {
				errs <- fmt.Errorf("read %s = %q, %w", path, data, err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := ws.ReplaceFile(t.Context(), "shared.txt", base, []byte(fmt.Sprint(i)))
			var mismatch *tool.VersionMismatchError
			switch {
			case err == nil:
				replaced.Add(1)
			case errors.As(err, &mismatch):
				mismatched.Add(1)
			default:
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if replaced.Load() != 1 || mismatched.Load() != n-1 {
		t.Fatalf("replaced=%d mismatched=%d, want one winner", replaced.Load(), mismatched.Load())
	}
}

func TestValidatePlacementRequiresExecutorServices(t *testing.T) {
	for name, services := range map[string]bool{"current": true, "older provider": false} {
		t.Run(name, func(t *testing.T) {
			fx := startProvider(t, &integrationBackend{}, nil, services, nil)
			t.Cleanup(fx.stop)
			client, err := New(fx.endpoint, fx.clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			provider, err := NewTemplateProvider(client, "coding", codingRevision)
			if err != nil {
				t.Fatal(err)
			}
			err = provider.ValidatePlacement(t.Context())
			if services && err != nil {
				t.Fatalf("preflight = %v", err)
			}
			if !services && !errors.Is(err, ErrExecutorServicesMissing) {
				t.Fatalf("preflight against an older provider = %v", err)
			}
		})
	}
}

func TestRefusalNeedsNotForwardedAndAConsistentCode(t *testing.T) {
	refusal := func(grpcCode codes.Code, code executionenv.ErrorCode, forwarded bool) error {
		details := []protoadapt.MessageV1{
			&executionv1.ErrorDetail{Code: string(code), Retryable: true},
		}
		if !forwarded {
			details = append(details, &executorv1.ErrorDetail{
				Reason: executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED,
			})
		}
		st, err := status.New(grpcCode, "peer text").WithDetails(details...)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("main.go: %w", st.Err())
	}
	got := Refusal(refusal(codes.Unauthenticated, executionenv.CodeUnauthenticated, false))
	if got == nil || got.Code != executionenv.CodeUnauthenticated || !got.Retryable ||
		strings.Contains(got.Error(), "peer text") {
		t.Fatalf("refusal = %v", got)
	}
	for name, err := range map[string]error{
		"forwarded":    refusal(codes.Unauthenticated, executionenv.CodeUnauthenticated, true),
		"inconsistent": refusal(codes.NotFound, executionenv.CodeUnauthenticated, false),
		"unknown code": refusal(codes.Internal, "future", false),
		"plain":        errors.New("main.go: not found"),
	} {
		if got := Refusal(err); got != nil {
			t.Fatalf("%s decoded as %v", name, got)
		}
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

func TestEndpointRejectsHTTPAndAlternateResolvers(t *testing.T) {
	cfg := &tls.Config{RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{Certificate: [][]byte{{1}}}}}
	for _, endpoint := range []string{"https://provider:8443", "dns:///provider:8443", "provider"} {
		if c, err := New(endpoint, cfg); err == nil {
			c.Close()
			t.Fatalf("accepted %q", endpoint)
		}
	}
}
