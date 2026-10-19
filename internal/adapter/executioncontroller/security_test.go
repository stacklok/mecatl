package executioncontroller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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

func TestSecurityPolicyAuthorityRevokesExistingClient(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ca, server, key, client := rpcSecurityPKI(t, now, "spiffe://example/client")
	writeRotationMaterial(t, dir, map[string][]byte{"server.crt": server, "server.key": key, "clients.pem": ca})
	manifest := securityManifest{Version: 1, Generation: 1, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}}
	path := filepath.Join(dir, "manifest.json")
	writeManifest(t, path, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}})
	manager := NewSecurityManager(path, dir, "ns", "authority", kube)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(client.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if id, policy, err := manager.authorize(t.Context(), []*x509.Certificate{leaf}); err != nil || id != manifest.Clients[0].URI || !policy.MayAttestOwner {
		t.Fatalf("authorized client denied: %q %+v %v", id, policy, err)
	}
	manifest.Generation++
	manifest.Clients[0].MayAttestOwner = false
	manifest.Clients[0].ExecutionTemplates = nil
	writeManifest(t, path, manifest)
	other := NewSecurityManager(path, dir, "ns", "authority", kube)
	if err := other.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.authorize(t.Context(), []*x509.Certificate{leaf}); err == nil {
		t.Fatal("superseded client policy accepted before local projection reload")
	}
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, policy, err := manager.authorize(t.Context(), []*x509.Certificate{leaf}); err != nil || policy.MayAttestOwner {
		t.Fatalf("new policy not applied: %+v %v", policy, err)
	}
	manifest.Generation--
	writeManifest(t, path, manifest)
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("policy generation rollback accepted")
	}
}

func TestSecurityManifestRejectsOldSigningFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"generation":1,"issuer":"old","tls":{},"clients":[{"uri":"spiffe://example/client"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewSecurityManager(path, dir, "ns", "authority", nil)
	if _, err := manager.loadManifest(); err == nil {
		t.Fatal("old signing-key configuration was accepted")
	}
}

func TestAuthorityDigestIgnoresTLSButBindsClientPermissions(t *testing.T) {
	manifest := securityManifest{Version: 1, Generation: 1, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	before, err := authorityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.TLS.CertificateFile = "rotated.crt"
	after, err := authorityDigest(manifest)
	if err != nil || before != after {
		t.Fatal("TLS identity pinned to client authorization policy")
	}
	manifest.Clients[0].MayAttestOwner = false
	after, err = authorityDigest(manifest)
	if err != nil || before == after {
		t.Fatal("client permission not bound to policy")
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

func syntheticPKI(t *testing.T, now time.Time) ([]byte, []byte, []byte) {
	ca, cert, key, _ := rpcSecurityPKI(t, now, "spiffe://example/client")
	return ca, cert, key
}

func rpcSecurityPKI(t *testing.T, now time.Time, clientURI string) ([]byte, []byte, []byte, tls.Certificate) {
	t.Helper()
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, uriText string, usage x509.ExtKeyUsage, dns string) ([]byte, ed25519.PrivateKey) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if uriText != "" {
			uri, parseErr := url.Parse(uriText)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			tmpl.URIs = []*url.URL{uri}
		}
		if dns != "" {
			tmpl.DNSNames = []string{dns}
		}
		der, issueErr := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return der, key
	}
	serverDER, serverKey := issue(102, "spiffe://example/provider", x509.ExtKeyUsageServerAuth, "provider.test")
	clientDER, clientKey := issue(103, clientURI, x509.ExtKeyUsageClientAuth, "")
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}), tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey}
}
