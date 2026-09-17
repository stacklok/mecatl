package executioncontroller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
	firstCertificate := append([]byte(nil), manager.current.Load().tlsConfig.Certificates[0].Certificate[0]...)
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
	if string(firstCertificate) == string(manager.current.Load().tlsConfig.Certificates[0].Certificate[0]) {
		t.Fatal("TLS identity did not rotate atomically")
	}
	manifest.Generation = 1
	writeManifest(t, manifestPath, manifest)
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("generation rollback accepted")
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
