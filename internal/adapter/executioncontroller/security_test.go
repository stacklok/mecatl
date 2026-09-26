package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type durableClaimBackend struct {
	*fakeBackend
	mu                         sync.Mutex
	acquired, renewed          map[string]executionenv.RunClaim
	acquireCalls, renewalCalls int
}

func newDurableClaimBackend() *durableClaimBackend {
	return &durableClaimBackend{fakeBackend: newFakeBackend(), acquired: make(map[string]executionenv.RunClaim), renewed: make(map[string]executionenv.RunClaim)}
}

func (b *durableClaimBackend) AcquireRun(_ context.Context, ref executionenv.EnvironmentRef, _ string, _ string, binding, run, operation string, ttl time.Duration) (executionenv.RunClaim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acquireCalls++
	if claim, ok := b.acquired[operation]; ok {
		return claim, nil
	}
	claim := executionenv.RunClaim{Environment: ref, BindingID: binding, RunID: run, ClaimID: "claim-acquire", Epoch: 2, GrantGeneration: 1, ExpiresAt: time.Now().Add(ttl)}
	b.acquired[operation] = claim
	return claim, nil
}

func (b *durableClaimBackend) RenewRun(_ context.Context, _ executionenv.EnvironmentRef, _ string, _ string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renewalCalls++
	if claim, ok := b.renewed[req.OperationID]; ok {
		return claim, nil
	}
	claim := executionenv.RunClaim{Environment: req.Environment, BindingID: req.BindingID, RunID: req.RunID, ClaimID: req.ClaimID, Epoch: req.Epoch, GrantGeneration: req.GrantGeneration + 1, ExpiresAt: time.Now().Add(req.TTL)}
	b.renewed[req.OperationID] = claim
	return claim, nil
}

func (b *durableClaimBackend) claim(operation string, renew bool) (executionenv.RunClaim, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if renew {
		claim, ok := b.renewed[operation]
		return claim, b.renewalCalls, ok
	}
	claim, ok := b.acquired[operation]
	return claim, b.acquireCalls, ok
}

func TestSecurityManagerReloadRollbackAndRecovery(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "..data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	grantPub, grantPriv, _ := ed25519.GenerateKey(rand.Reader)
	writePKCS8(t, filepath.Join(data, "grant.pem"), grantPriv)
	caPEM, serverCert, serverKey := syntheticPKI(t, now)
	if err := os.WriteFile(filepath.Join(data, "server.crt"), serverCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "server.key"), serverKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "clients.pem"), caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"grant.pem", "server.crt", "server.key", "clients.pem"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(grantPub)
	manifest := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true, Administrator: true}}}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !manager.Ready() {
		t.Fatal("valid snapshot is not ready")
	}
	manifest.Clients[0].Administrator = false
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("same-generation client policy drift was accepted")
	}
	manifest.Clients[0].Administrator = true
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatalf("restore same generation: %v", err)
	}
	manifest.Generation = 0
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("invalid candidate retained authorization readiness")
	}
	manifest.Generation = 2
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil || !manager.Ready() {
		t.Fatalf("recovery reload: ready=%v err=%v", manager.Ready(), err)
	}
	firstCertificate := append([]byte(nil), manager.state.Load().snapshot.tlsConfig.Certificates[0].Certificate[0]...)
	cm, err := kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	originalState := cm.Data[securityStateDataKey]
	cm.Data[securityStateDataKey] = `{"generation":2,"fingerprints":{"k1:1":"forged"}}`
	if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.material(t.Context()); err == nil || manager.Ready() {
		t.Fatal("authoritative fingerprint drift retained authorization")
	}
	cm, err = kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm.Data[securityStateDataKey] = originalState
	if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatalf("restore authority: %v", err)
	}
	newCAPEM, newServerCert, newServerKey := syntheticPKI(t, now)
	for name, contents := range map[string][]byte{"server.crt": newServerCert, "server.key": newServerKey, "clients.pem": newCAPEM} {
		if err := os.WriteFile(filepath.Join(data, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Generation = 3
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil || !manager.Ready() {
		t.Fatalf("TLS/CA correction did not recover: ready=%v err=%v", manager.Ready(), err)
	}
	if string(firstCertificate) == string(manager.state.Load().snapshot.tlsConfig.Certificates[0].Certificate[0]) {
		t.Fatal("TLS identity did not rotate atomically")
	}
	manifest.Generation = 4
	writeManifest(t, manifestPath, manifest)
	peer := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	peer.now = manager.now
	if err := peer.Reload(t.Context()); err != nil {
		t.Fatalf("peer authority advance: %v", err)
	}
	if manager.CheckReady(t.Context()) || manager.Ready() {
		t.Fatal("readiness retained a snapshot superseded by a peer")
	}
	manifest.Generation = 1
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("generation rollback accepted")
	}
}

func TestSecurityManagerStaleAuthorityFailureCannotPoisonNewerState(t *testing.T) {
	now := time.Now().UTC()
	window := keyValidity{activateAt: now.Add(-time.Minute), verifyUntil: now.Add(time.Hour)}
	oldSnapshot := &securitySnapshot{generation: 1, digest: "old", activeWindow: window, validUntil: now.Add(time.Hour), fingerprints: map[string]string{"k1:1": "old"}}
	newSnapshot := &securitySnapshot{generation: 2, digest: "new", activeWindow: window, validUntil: now.Add(time.Hour), fingerprints: map[string]string{"k1:1": "old", "k1:2": "new"}}
	raw, err := json.Marshal(securityLedger{Generation: newSnapshot.generation, Digest: newSnapshot.digest, Fingerprints: newSnapshot.fingerprints})
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{securityStateDataKey: string(raw)}})
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce sync.Once
	kube.PrependReactor("get", "configmaps", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return false, nil, nil
	})
	manager := NewSecurityManager("", "", "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	oldState := &securityState{snapshot: oldSnapshot}
	manager.state.Store(oldState)
	done := make(chan error, 1)
	go func() {
		_, authErr := manager.authoritative(t.Context())
		done <- authErr
	}()
	<-started
	newState := &securityState{snapshot: newSnapshot}
	manager.state.Store(newState)
	close(release)
	if err := <-done; !errors.Is(err, errAuthorityUnavailable) {
		t.Fatalf("stale authority check error=%v", err)
	}
	if manager.state.Load() != newState || !manager.Ready() {
		t.Fatal("stale authority check invalidated the newer published state")
	}

	manager.invalidateObservedUnlessReplaced(t.Context(), oldState)
	if manager.state.Load() != newState || !manager.Ready() {
		t.Fatal("stale reload failure invalidated the newer authoritative state")
	}
}

func TestSecurityLedgerRejectsKeyVersionRollbackAcrossRestart(t *testing.T) {
	ledger := securityLedger{Generation: 2, Fingerprints: map[string]string{"key:2": "old"}}
	candidate := &securitySnapshot{generation: 3, fingerprints: map[string]string{"key:1": "new"}}
	if _, err := advanceSecurityLedger(&ledger, candidate); err == nil {
		t.Fatal("key version rollback was accepted")
	}
	candidate.fingerprints = map[string]string{"key:3": "new"}
	changed, err := advanceSecurityLedger(&ledger, candidate)
	if err != nil || !changed || candidate.fingerprints["key:2"] != "old" {
		t.Fatalf("monotonic rotation failed: changed=%v err=%v fingerprints=%v", changed, err, candidate.fingerprints)
	}
}

func TestSecurityManagerGuardsEveryRPCOnExistingConnection(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	caPEM, serverPEM, serverKeyPEM, clientCert := rpcSecurityPKI(t, now, "spiffe://example/client")
	grantPub, grantPriv, _ := ed25519.GenerateKey(rand.Reader)
	writePKCS8(t, filepath.Join(dir, "grant.pem"), grantPriv)
	for name, contents := range map[string][]byte{"server.crt": serverPEM, "server.key": serverKeyPEM, "clients.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(grantPub)
	manifest := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", testProfiles(), nil)
	limiter, err := NewRPCLimiter(manager, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())), grpc.UnaryInterceptor(limiter.UnaryInterceptor))
	executionv1.RegisterExecutionProviderServiceServer(server, NewHandler(HandlerConfig{Security: manager}, store))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "provider.test", RootCAs: roots, Certificates: []tls.Certificate{clientCert}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := executionv1.NewExecutionProviderServiceClient(conn)
	call := func() error {
		_, err := client.ValidateProfile(t.Context(), &executionv1.ValidateProfileRequest{Profile: "go"})
		return err
	}
	if err := call(); err != nil {
		t.Fatalf("initial RPC: %v", err)
	}
	cm, err := kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	authoritativeState := cm.Data[securityStateDataKey]
	var drifted securityLedger
	if err := executionenv.DecodeStrict([]byte(authoritativeState), &drifted); err != nil {
		t.Fatal(err)
	}
	drifted.Digest = strings.Repeat("0", 64)
	raw, _ := json.Marshal(drifted)
	cm.Data[securityStateDataKey] = string(raw)
	if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := call(); status.Code(err) != codes.Unavailable || manager.Ready() {
		t.Fatalf("old snapshot survived durable authority drift: ready=%v err=%v", manager.Ready(), err)
	} else if details := status.Convert(err).Details(); len(details) != 1 || details[0].(*executionv1.ErrorDetail).Code != string(executionenv.CodeNotReady) || !details[0].(*executionv1.ErrorDetail).Retryable {
		t.Fatalf("authority lag was not a structured retryable not_ready: %v", details)
	}
	cm, _ = kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
	cm.Data[securityStateDataKey] = authoritativeState
	if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	newCAPEM, newServerPEM, newServerKeyPEM, _ := rpcSecurityPKI(t, now, "spiffe://example/other")
	for name, contents := range map[string][]byte{"server.crt": newServerPEM, "server.key": newServerKeyPEM, "clients.pem": newCAPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Generation = 2
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := call(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("old connection survived CA rotation: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(t.Context()); err == nil {
		t.Fatal("invalid manifest reloaded")
	}
	if err := call(); status.Code(err) != codes.Unavailable {
		t.Fatalf("RPC remained authorized during invalid manifest: %v", err)
	}
	for name, contents := range map[string][]byte{"server.crt": serverPEM, "server.key": serverKeyPEM, "clients.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest.Generation = 3
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil || call() != nil {
		t.Fatalf("valid generation did not recover existing connection: %v", err)
	}
}

func TestRunClaimSigningAuthorityOutageIsRetryableAfterDurableMutation(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	caPEM, serverPEM, serverKeyPEM, clientCert := rpcSecurityPKI(t, now, "spiffe://example/client")
	grantPub, grantPriv, _ := ed25519.GenerateKey(rand.Reader)
	writePKCS8(t, filepath.Join(dir, "grant.pem"), grantPriv)
	for name, contents := range map[string][]byte{"server.crt": serverPEM, "server.key": serverKeyPEM, "clients.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(grantPub)
	manifest := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	var reads, failAt atomic.Int64
	kube.PrependReactor("get", "configmaps", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		read := reads.Add(1)
		if target := failAt.Load(); target != 0 && read == target {
			return true, nil, errors.New("injected authority read outage")
		}
		return false, nil, nil
	})
	armSigningOutage := func() {
		reads.Store(0)
		failAt.Store(2) // Authorization reads first; response signing reads second.
	}
	recoverAuthority := func() {
		failAt.Store(0)
		if err := manager.Reload(t.Context()); err != nil {
			t.Fatalf("reload authority: %v", err)
		}
	}
	assertNotReady := func(err error) {
		t.Helper()
		st := status.Convert(err)
		details := st.Details()
		if st.Code() != codes.Unavailable || len(details) != 1 {
			t.Fatalf("code=%v details=%v", st.Code(), details)
		}
		detail, ok := details[0].(*executionv1.ErrorDetail)
		if !ok || detail.Code != string(executionenv.CodeNotReady) || !detail.Retryable {
			t.Fatalf("detail=%v", details)
		}
	}
	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx := grpcpeer.NewContext(t.Context(), &grpcpeer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}}})
	backend := newDurableClaimBackend()
	h := NewHandler(HandlerConfig{Security: manager}, backend)
	owner := &executionv1.Owner{Issuer: "issuer", Subject: "alice"}
	ref := &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}
	acquire := &executionv1.AcquireRunRequest{Environment: ref, Owner: owner, BindingId: "binding", RunId: "run", OperationId: "acquire-op", TtlMillis: time.Minute.Milliseconds()}

	armSigningOutage()
	_, err = h.AcquireRun(ctx, acquire)
	assertNotReady(err)
	storedAcquire, calls, ok := backend.claim(acquire.OperationId, false)
	if !ok || calls != 1 {
		t.Fatalf("acquire mutation missing or retried automatically: stored=%t calls=%d", ok, calls)
	}
	recoverAuthority()
	acquired, err := h.AcquireRun(ctx, acquire)
	if err != nil {
		t.Fatalf("explicit acquire retry: %v", err)
	}
	if callsClaim, retryCalls, ok := backend.claim(acquire.OperationId, false); !ok || retryCalls != 2 || callsClaim.ClaimID != storedAcquire.ClaimID || acquired.ClaimId != storedAcquire.ClaimID {
		t.Fatalf("acquire retry did not converge: response=%q stored=%+v calls=%d", acquired.GetClaimId(), callsClaim, retryCalls)
	}

	renew := &executionv1.RenewRunRequest{Environment: acquired.Environment, Owner: owner, BindingId: acquired.BindingId, RunId: acquired.RunId, ClaimId: acquired.ClaimId, Epoch: acquired.Epoch, GrantGeneration: acquired.GrantGeneration, OperationId: "renew-op", TtlMillis: time.Minute.Milliseconds()}
	armSigningOutage()
	_, err = h.RenewRun(ctx, renew)
	assertNotReady(err)
	storedRenew, calls, ok := backend.claim(renew.OperationId, true)
	if !ok || calls != 1 {
		t.Fatalf("renew mutation missing or retried automatically: stored=%t calls=%d", ok, calls)
	}
	recoverAuthority()
	renewed, err := h.RenewRun(ctx, renew)
	if err != nil {
		t.Fatalf("explicit renew retry: %v", err)
	}
	if callsClaim, retryCalls, ok := backend.claim(renew.OperationId, true); !ok || retryCalls != 2 || callsClaim.ClaimID != storedRenew.ClaimID || callsClaim.GrantGeneration != storedRenew.GrantGeneration || renewed.ClaimId != storedRenew.ClaimID || renewed.GrantGeneration != storedRenew.GrantGeneration {
		t.Fatalf("renew retry did not converge: response=%q/%d stored=%+v calls=%d", renewed.GetClaimId(), renewed.GetGrantGeneration(), callsClaim, retryCalls)
	}
}

func TestSecurityManagerAuthorizesPresentedIntermediateAndRevokesRootOnExistingConnection(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	caPEM, serverPEM, serverKeyPEM, clientCert := rpcSecurityIntermediatePKI(t, now, "spiffe://example/client")
	grantPub, grantPriv, _ := ed25519.GenerateKey(rand.Reader)
	writePKCS8(t, filepath.Join(dir, "grant.pem"), grantPriv)
	for name, contents := range map[string][]byte{"server.crt": serverPEM, "server.key": serverKeyPEM, "clients.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(grantPub)
	manifest := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), "ns", testProfiles(), nil)
	limiter, err := NewRPCLimiter(manager, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())), grpc.UnaryInterceptor(limiter.UnaryInterceptor))
	executionv1.RegisterExecutionProviderServiceServer(server, NewHandler(HandlerConfig{Security: manager}, store))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "provider.test", RootCAs: roots, Certificates: []tls.Certificate{clientCert}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := executionv1.NewExecutionProviderServiceClient(conn)
	call := func() error {
		_, err := client.ValidateProfile(t.Context(), &executionv1.ValidateProfileRequest{Profile: "go"})
		return err
	}
	if err := call(); err != nil {
		t.Fatalf("intermediate-signed client RPC: %v", err)
	}
	removedRoot, _, _, _ := rpcSecurityPKI(t, now, "spiffe://example/other")
	if err := os.WriteFile(filepath.Join(dir, "clients.pem"), removedRoot, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Generation = 2
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := call(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("existing connection survived removal of intermediate issuer root: %v", err)
	}
}

func rpcSecurityIntermediatePKI(t *testing.T, now time.Time, clientURI string) ([]byte, []byte, []byte, tls.Certificate) {
	t.Helper()
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	root := &x509.Certificate{SerialNumber: big.NewInt(201), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, rootPub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediatePub, intermediateKey, _ := ed25519.GenerateKey(rand.Reader)
	intermediate := &x509.Certificate{SerialNumber: big.NewInt(202), Subject: pkix.Name{CommonName: "intermediate"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediate, root, intermediatePub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, parent *x509.Certificate, parentKey ed25519.PrivateKey, uriText string, usages []x509.ExtKeyUsage, dns []string) ([]byte, ed25519.PrivateKey) {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, DNSNames: dns}
		if uriText != "" {
			u, _ := url.Parse(uriText)
			tmpl.URIs = []*url.URL{u}
		}
		der, issueErr := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return der, key
	}
	serverDER, serverKey := issue(203, root, rootKey, "spiffe://example/provider", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"provider.test"})
	clientDER, clientKey := issue(204, intermediate, intermediateKey, clientURI, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	encodeKey := func(key ed25519.PrivateKey) []byte {
		raw, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), encodeKey(serverKey), tls.Certificate{Certificate: [][]byte{clientDER, intermediateDER}, PrivateKey: clientKey}
}

func rpcSecurityPKI(t *testing.T, now time.Time, clientURI string) ([]byte, []byte, []byte, tls.Certificate) {
	t.Helper()
	caPub, caKey, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(101), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, uriText string, usages []x509.ExtKeyUsage, dns []string) ([]byte, ed25519.PrivateKey) {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, DNSNames: dns}
		if uriText != "" {
			u, _ := url.Parse(uriText)
			tmpl.URIs = []*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return der, key
	}
	serverDER, serverKey := issue(102, "spiffe://example/provider", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"provider.test"})
	clientDER, clientKey := issue(103, clientURI, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	encodeKey := func(key ed25519.PrivateKey) []byte {
		raw, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), encodeKey(serverKey), tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey}
}

func TestAuthorityDigestCoversSecurityMeaningfulState(t *testing.T) {
	base := securityManifest{Version: 1, Generation: 7, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "key.pem", PublicSHA256: "fingerprint", ActivateAt: time.Unix(1, 0).UTC(), VerifyUntil: time.Unix(100, 0).UTC(), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "ca.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client"}}}
	digest := func(m securityManifest, ca, server string) string {
		t.Helper()
		ttl, err := time.ParseDuration(m.GrantTTLText)
		if err != nil {
			t.Fatal(err)
		}
		skew, err := time.ParseDuration(m.ClockSkewText)
		if err != nil {
			t.Fatal(err)
		}
		got, err := authorityDigest(m, ttl, skew, map[string]string{"k1:1": "fingerprint"}, []string{ca}, server)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	original := digest(base, "ca-1", "server-1")
	mutations := map[string]func(*securityManifest){
		"capability":      func(m *securityManifest) { m.Clients[0].Administrator = true },
		"key revoke":      func(m *securityManifest) { m.Keys[0].State = "revoked" },
		"active key":      func(m *securityManifest) { m.ActiveKeyID = "k2" },
		"activation time": func(m *securityManifest) { m.Keys[0].ActivateAt = m.Keys[0].ActivateAt.Add(time.Second) },
		"grant ttl":       func(m *securityManifest) { m.GrantTTLText = "2m" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Keys = slices.Clone(base.Keys)
			candidate.Clients = slices.Clone(base.Clients)
			mutate(&candidate)
			if digest(candidate, "ca-1", "server-1") == original {
				t.Fatal("mutation did not change authority digest")
			}
		})
	}
	if digest(base, "ca-2", "server-1") == original || digest(base, "ca-1", "server-2") == original {
		t.Fatal("CA or server identity swap did not change authority digest")
	}
}

func TestSecurityManagerSigningWindowIsCheckedOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	grantPub, grantPriv, _ := ed25519.GenerateKey(rand.Reader)
	writePKCS8(t, filepath.Join(dir, "grant.pem"), grantPriv)
	caPEM, serverCert, serverKey := syntheticPKI(t, now)
	for name, contents := range map[string][]byte{"server.crt": serverCert, "server.key": serverKey, "clients.pem": caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(grantPub)
	manifest := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(10 * time.Second), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	clock := now
	manager.now = func() time.Time { return clock }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(HandlerConfig{Security: manager}, &fakeBackend{})
	claim := executionenv.RunClaim{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 1, GrantGeneration: 1}
	if _, expiry, err := h.signClaim(t.Context(), claim, "spiffe://example/client", "owner"); err != nil || expiry.After(manifest.Keys[0].VerifyUntil) {
		t.Fatalf("initial sign expiry=%v err=%v", expiry, err)
	}
	clock = manifest.Keys[0].VerifyUntil
	if manager.Ready() {
		t.Fatal("expired active key remained ready between reload ticks")
	}
	if _, _, err := h.signClaim(t.Context(), claim, "spiffe://example/client", "owner"); err == nil {
		t.Fatal("expired active key signed between reload ticks")
	}
	manifest.Generation = 2
	manifest.Keys[0].VerifyUntil = clock.Add(time.Hour)
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil || !manager.Ready() {
		t.Fatalf("corrected generation did not recover: ready=%v err=%v", manager.Ready(), err)
	}
}

func TestSecurityManagerRejectsEscapingProjectedSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("../../outside", filepath.Join(dir, "grant.pem")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})
	if _, err := readRootFile(root, "grant.pem"); err == nil {
		t.Fatal("escaping symlink was read")
	}
}

func writeManifest(t *testing.T, path string, manifest securityManifest) {
	t.Helper()
	b, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func writePKCS8(t *testing.T, path string, key ed25519.PrivateKey) {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), 0o600); err != nil {
		t.Fatal(err)
	}
}
func syntheticPKI(t *testing.T, now time.Time) ([]byte, []byte, []byte) {
	t.Helper()
	caPub, caKey, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverPub, serverKey, _ := ed25519.GenerateKey(rand.Reader)
	uri, _ := url.Parse("spiffe://example/provider")
	server := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "provider"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{uri}}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, serverPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
