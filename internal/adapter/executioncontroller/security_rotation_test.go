package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// These are synthetic fixture names, including the former mutable Secret keys.
func rotationSecurityFixture(t *testing.T) (*SecurityManager, securityManifest, context.Context, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().UTC()
	oldCA, oldCert, oldKey := syntheticPKI(t, now)
	newCA, newCert, newKey, client := rpcSecurityPKI(t, now, "spiffe://example/client")
	writeRotationMaterial(t, dir, map[string][]byte{"tls.crt": oldCert, "tls.key": oldKey, "clients.pem": append(oldCA, newCA...)})
	manifest := securityManifest{Version: 1, Generation: 2, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k2", GrantTTLText: "1m", ClockSkewText: "5s", TLS: securityTLSManifest{CertificateFile: "tls.crt", PrivateKeyFile: "tls.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	for i, id := range []string{"k1", "k2"} {
		pub, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		name := "grant-" + id + ".pem"
		writePKCS8(t, filepath.Join(dir, name), private)
		fp := sha256.Sum256(pub)
		manifest.Keys = append(manifest.Keys, securityKeyManifest{ID: id, Version: uint64(i + 1), File: name, PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"})
	}
	path := filepath.Join(dir, "manifest.json")
	writeManifest(t, path, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{}})
	manager := NewSecurityManager(path, dir, "ns", "authority", kube)
	manager.now = func() time.Time { return now }
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(client.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}}})
	return manager, manifest, ctx, map[string][]byte{"tls.crt": newCert, "tls.key": newKey, "clients.pem": newCA}
}

func writeRotationMaterial(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSecurityRotationPinnedReplicaReadConvergesBeforeDispatch(t *testing.T) {
	manager, manifest, ctx, final := rotationSecurityFixture(t)
	backend := &countedFileBackend{fakeBackend: newFakeBackend()}
	h := NewHandler(HandlerConfig{Security: manager}, backend)
	owner := &executionv1.Owner{Issuer: "issuer", Subject: "alice"}
	claim, err := h.AcquireRun(ctx, &executionv1.AcquireRunRequest{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: owner, BindingId: "binding", RunId: "run", OperationId: "acquire", TtlMillis: time.Minute.Milliseconds()})
	if err != nil {
		t.Fatal("bridge k2 acquire failed")
	}
	request := &executionv1.FileRequest{Context: &executionv1.RequestContext{Environment: claim.Environment, Owner: owner, BindingId: claim.BindingId, RunId: claim.RunId, ClaimId: claim.ClaimId, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Grant: claim.Grant}, Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "sentinel"}
	if _, err := h.Files(ctx, request); err != nil || backend.calls != 1 {
		t.Fatal("bridge claim positive control failed")
	}
	manifest.Generation = 3
	manifest.Keys[0].State = "revoked"
	manifest.TLS = securityTLSManifest{CertificateFile: "provider-new.crt", PrivateKeyFile: "provider-new.key", ClientCAFile: "final-clients.pem"}
	writeRotationMaterial(t, manager.keyDirectory, map[string][]byte{"provider-new.crt": final["tls.crt"], "provider-new.key": final["tls.key"], "final-clients.pem": final["clients.pem"]})
	writeManifest(t, manager.manifestPath, manifest)
	other := NewSecurityManager(manager.manifestPath, manager.keyDirectory, "ns", "authority", manager.kube)
	other.now = manager.now
	if err := other.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	backend.calls = 0
	_, err = h.Files(ctx, request)
	st := status.Convert(err)
	details := st.Details()
	if st.Code() != codes.Unavailable || len(details) != 1 || backend.calls != 0 {
		t.Fatalf("lagging replica: code=%s dispatches=%d", st.Code(), backend.calls)
	}
	detail, ok := details[0].(*executionv1.ErrorDetail)
	if !ok || detail.Code != string(executionenv.CodeNotReady) || !detail.Retryable {
		t.Fatal("lag was not structured retryable not_ready")
	}
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Files(ctx, request); err != nil || backend.calls != 1 {
		t.Fatalf("same current claim after reload: code=%s dispatches=%d", status.Code(err), backend.calls)
	}
}

func TestSecurityRotationMutableNamesPoisonSameGeneration(t *testing.T) {
	manager, manifest, _, final := rotationSecurityFixture(t)
	manifest.Generation = 3
	manifest.Keys[0].State = "revoked"
	// ConfigMap projection arrives before the Secret: all old names still exist.
	writeManifest(t, manager.manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatalf("mixed bundle did not reproduce publication: %v", err)
	}
	mixed := manager.state.Load().snapshot.digest
	writeRotationMaterial(t, manager.keyDirectory, final)
	complete, err := manager.load()
	if err != nil || complete.digest == mixed {
		t.Fatal("complete bundle did not differ from published mixed bundle")
	}
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("same-generation material drift was not rejected")
	}
	if err := manager.Reload(t.Context()); err == nil {
		t.Fatal("retry unexpectedly repaired the poisoned ledger")
	}
	// Recovery requires a forward generation, not a retry or ledger reset.
	manifest.Generation++
	writeManifest(t, manager.manifestPath, manifest)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityRotationImmutableNamesHandleProjectionSkew(t *testing.T) {
	for _, manifestFirst := range []bool{false, true} {
		name := "material-first"
		if manifestFirst {
			name = "manifest-first"
		}
		t.Run(name, func(t *testing.T) {
			manager, manifest, _, final := rotationSecurityFixture(t)
			before := manager.state.Load().snapshot.digest
			manifest.Generation = 3
			manifest.Keys[0].State = "revoked"
			manifest.TLS = securityTLSManifest{CertificateFile: "provider-new.crt", PrivateKeyFile: "provider-new.key", ClientCAFile: "final-clients.pem"}
			if manifestFirst {
				writeManifest(t, manager.manifestPath, manifest)
				if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
					t.Fatal("missing immutable material did not fail closed")
				}
			}
			writeRotationMaterial(t, manager.keyDirectory, map[string][]byte{"provider-new.crt": final["tls.crt"], "provider-new.key": final["tls.key"], "final-clients.pem": final["clients.pem"]})
			if !manifestFirst {
				if err := manager.Reload(t.Context()); err != nil || manager.state.Load().snapshot.digest != before {
					t.Fatal("staging new names changed the old manifest's authority")
				}
				writeManifest(t, manager.manifestPath, manifest)
			}
			if err := manager.Reload(t.Context()); err != nil || !manager.Ready() {
				t.Fatalf("complete immutable bundle did not converge: %v", err)
			}
			if err := manager.Reload(t.Context()); err != nil {
				t.Fatal("same-generation immutable reload drifted")
			}
		})
	}
}
