package executioncontroller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
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
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())))
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
	if err := call(); status.Code(err) != codes.Unauthenticated || manager.Ready() {
		t.Fatalf("old snapshot survived durable authority drift: ready=%v err=%v", manager.Ready(), err)
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
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())))
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
