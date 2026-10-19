//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	corev1 "k8s.io/api/core/v1"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
)

type renewalTestIdentity struct {
	pair            tls.Certificate
	certPEM, keyPEM []byte
}

func renewalTestIssuer(t *testing.T) ([]byte, func(int64, time.Time) renewalTestIdentity) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), func(serial int64, expires time.Time) renewalTestIdentity {
		t.Helper()
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "fixture"}, URIs: []*url.URL{{Scheme: "spiffe", Host: "mecatl.test", Path: "/client/fixture"}}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		identity := renewalTestIdentity{certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})}
		identity.pair, err = tls.X509KeyPair(identity.certPEM, identity.keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return identity
	}
}

func TestFinalMockTLSSelectsCurrentIssuedIdentityAfterInitialExpiry(t *testing.T) {
	ca, issue := renewalTestIssuer(t)
	initial := issue(2, time.Now().Add(-time.Minute))
	current := issue(3, time.Now().Add(time.Hour))
	state := t.TempDir()
	dir := filepath.Join(state, "pki")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"ca.crt": ca, "mecak8s.crt": initial.certPEM, "mecak8s.key": initial.keyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var reads atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/execution-qualification/secrets/execution-client-tls" {
			t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: current.certPEM, corev1.TLSPrivateKeyKey: current.keyPEM}}); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: kind-owned\ncontexts:\n- name: kind-owned\n  context: {cluster: owned, user: fixture}\nclusters:\n- name: owned\n  cluster: {server: %s}\nusers:\n- name: fixture\n  user: {}\n", api.URL)
	if err := os.WriteFile(filepath.Join(state, "kubeconfig"), []byte(kubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MECATL_KUBE_CONTEXT", "kind-owned")
	t.Setenv("MECATL_EXECUTION_LIVE_CLUSTER", "owned")
	cfg := loadTLS(t, dir, "mecak8s", "localhost")
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.SerialNumber.String() != "3" || !time.Now().Before(leaf.NotAfter) || reads.Load() != 1 {
		t.Fatal("final mock selected the expired initial disk snapshot instead of current issued identity")
	}
}

type renewalCatalogServer struct {
	executionv1.UnimplementedExecutionProviderServiceServer
	started, release chan struct{}
	clientSerial     atomic.Value
}

func (s *renewalCatalogServer) ListExecutionTemplates(ctx context.Context, _ *executionv1.ListExecutionTemplatesRequest) (*executionv1.ListExecutionTemplatesResponse, error) {
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("catalog RPC did not provide a peer")
	}
	info, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("catalog RPC did not authenticate the client")
	}
	s.clientSerial.Store(info.State.PeerCertificates[0].SerialNumber.String())
	return &executionv1.ListExecutionTemplatesResponse{}, nil
}

func (s *renewalCatalogServer) StartCommand(ctx context.Context, _ *executionv1.CommandStartRequest) (*executionv1.CommandStartResponse, error) {
	close(s.started)
	select {
	case <-s.release:
		return &executionv1.CommandStartResponse{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSameClientReloadProofRejectsNoOpAndWrongClient(t *testing.T) {
	ca, issue := renewalTestIssuer(t)
	firstClient, nextClient := issue(2, time.Now().Add(time.Hour)), issue(3, time.Now().Add(time.Hour))
	firstServer, nextServer := issue(4, time.Now().Add(time.Hour)), issue(5, time.Now().Add(time.Hour))
	var serverPair atomic.Pointer[tls.Certificate]
	serverPair.Store(&firstServer.pair)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid test CA")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	svc := &renewalCatalogServer{started: make(chan struct{}), release: make(chan struct{})}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return serverPair.Load(), nil }})))
	executionv1.RegisterExecutionProviderServiceServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	dir := t.TempDir()
	files := executionclient.TLSFiles{CA: filepath.Join(dir, "ca.crt"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key"), ServerName: "localhost"}
	publish := func(identity renewalTestIdentity) {
		t.Helper()
		for path, data := range map[string][]byte{files.CA: ca, files.Cert: identity.certPEM, files.Key: identity.keyPEM} {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	publish(firstClient)
	client, err := executionclient.NewWithTLSFiles(listener.Addr().String(), files)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wrongClient, err := executionclient.NewWithTLSFiles(listener.Addr().String(), files)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongClient.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if serial, err := inflightCatalogSerial(ctx, client); err != nil || serial != "4" {
		t.Fatalf("baseline RPC: serial=%s err=%v", serial, err)
	}
	if serial := svc.clientSerial.Load(); serial != "2" {
		t.Fatalf("baseline RPC authenticated client serial=%v, want 2", serial)
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := executionv1.NewExecutionProviderServiceClient(client).StartCommand(ctx, &executionv1.CommandStartRequest{})
		result <- err
		close(done)
	}()
	select {
	case <-svc.started:
	case <-ctx.Done():
		t.Fatal("command did not start")
	}
	publish(nextClient)
	serverPair.Store(&nextServer.pair)
	// The projection and an independent renewed handshake alone cannot prove
	// that the active command's client published its new transport.
	if err := inflightVerifyReload(ctx, client, files, "5", "3"); err == nil {
		t.Fatal("accepted no-op reload")
	}
	if serial := svc.clientSerial.Load(); serial != "2" {
		t.Fatalf("no-op reload RPC authenticated client serial=%v, want 2", serial)
	}
	if err := wrongClient.ReloadTLS(); err != nil {
		t.Fatal(err)
	}
	if serial, err := inflightCatalogSerial(ctx, wrongClient); err != nil || serial != "5" {
		t.Fatalf("wrong-client RPC: serial=%s err=%v", serial, err)
	}
	if serial := svc.clientSerial.Load(); serial != "3" {
		t.Fatalf("wrong-client RPC authenticated client serial=%v, want 3", serial)
	}
	if err := inflightVerifyReload(ctx, client, files, "5", "3"); err == nil {
		t.Fatal("accepted reload of a different client")
	}
	if serial := svc.clientSerial.Load(); serial != "2" {
		t.Fatalf("original-client RPC after wrong-client reload authenticated client serial=%v, want 2", serial)
	}
	if err := client.ReloadTLS(); err != nil {
		t.Fatal(err)
	}
	if err := inflightVerifyReload(ctx, client, files, "5", "2"); err == nil {
		t.Fatal("accepted mismatched published client serial")
	}
	if err := inflightVerifyReload(ctx, client, files, "5", "3"); err != nil {
		t.Fatal(err)
	}
	if serial := svc.clientSerial.Load(); serial != "3" {
		t.Fatalf("same-client reload RPC authenticated client serial=%v, want 3", serial)
	}
	if err := inflightRunning(ctx, done); err != nil {
		t.Fatal(err)
	}
	close(svc.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("active command lost its terminal reply")
	}
	// A failed projection reload must not leave the old catalog transport usable.
	if err := os.WriteFile(files.Key, []byte("invalid test key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.ReloadTLS(); err == nil {
		t.Fatal("invalid projection reloaded")
	}
	if _, err := inflightCatalogSerial(ctx, client); err == nil {
		t.Fatal("catalog RPC bypassed fail-closed reload")
	}
}
