package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
)

type fakeBackend struct {
	ensureCalls, created int
	allocations          map[string]Allocation
}

func newFakeBackend() *fakeBackend { return &fakeBackend{allocations: map[string]Allocation{}} }
func (*fakeBackend) ValidateProfile(context.Context, string) (Profile, error) {
	return Profile{Name: "go", Digest: "sha256:test", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute}, nil
}
func (f *fakeBackend) Ensure(_ context.Context, client, owner, binding, _, fp string) (Allocation, error) {
	f.ensureCalls++
	if a, ok := f.allocations[fp]; ok {
		return a, nil
	}
	f.created++
	a := Allocation{Environment: executionenv.EnvironmentRef{ID: "env-1", Revision: "rev-1"}, Epoch: 1, OwnerHash: owner, BindingID: binding, Client: client, Ready: true}
	f.allocations[fp] = a
	return a, nil
}
func (*fakeBackend) Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error) {
	panic("unused")
}
func (*fakeBackend) ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error {
	return nil
}
func (*fakeBackend) Retire(context.Context, executionenv.EnvironmentRef, string) error { return nil }
func (*fakeBackend) File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error) {
	return executionenv.FileResponse{}, nil
}
func (*fakeBackend) StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	return executionenv.CommandStartResponse{}, nil
}
func (*fakeBackend) CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
}
func (*fakeBackend) CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
}

func TestHandlerBlocksAllRequestsUntilStartupReady(t *testing.T) {
	backend := newFakeBackend()
	h := NewHandler(HandlerConfig{Ready: func() bool { return false }}, backend)
	r := httptest.NewRequest("POST", executionenv.BasePath+"/environments/ensure", strings.NewReader(`{"binding_id":"session-1","profile":"go","owner":{"issuer":"issuer","subject":"subject"}}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if backend.ensureCalls != 0 {
		t.Fatalf("backend calls before startup readiness = %d", backend.ensureCalls)
	}
}

func TestHandlerRequiresAllowlistedCanonicalURISAN(t *testing.T) {
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {MayAttestOwner: true}}}, nil)
	r := httptest.NewRequest("POST", executionenv.BasePath+"/profiles/validate", strings.NewReader(`{"profile":"go"}`))
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{{Scheme: "spiffe", Host: "cluster", Path: "/ns/other"}}}}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandlerValidateIsReadOnlyAndEnsureIdempotent(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	backend := newFakeBackend()
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {MayAttestOwner: true}}, Signer: GrantSigner{KeyID: "k1", PrivateKey: priv, Issuer: "provider", Audience: "execution", Lifetime: time.Minute}, Verifier: executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}, Issuer: "provider", Audience: "execution"}}, backend)
	call := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", executionenv.BasePath+path, strings.NewReader(body))
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{{Scheme: "spiffe", Host: "cluster", Path: "/ns/mecak8s"}}}}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/profiles/validate", `{"profile":"go"}`); w.Code != 200 {
		t.Fatalf("validate %d %s", w.Code, w.Body.String())
	}
	if backend.ensureCalls != 0 {
		t.Fatal("validation allocated")
	}
	body := `{"binding_id":"session-1","profile":"go","owner":{"issuer":"https://issuer","subject":"alice"}}`
	w1 := call("/environments/ensure", body)
	if w1.Code != 200 {
		t.Fatalf("ensure %d %s", w1.Code, w1.Body.String())
	}
	w2 := call("/environments/ensure", body)
	if w2.Code != 200 {
		t.Fatalf("ensure2 %d %s", w2.Code, w2.Body.String())
	}
	if backend.created != 1 {
		t.Fatalf("created=%d", backend.created)
	}
	var first, second executionenv.EnsureEnvironmentResponse
	if err := json.Unmarshal(w1.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if first.Environment != second.Environment || first.Epoch != second.Epoch {
		t.Fatalf("allocation identity changed")
	}
}

func TestCanonicalClientIdentityRejectsCNAndAmbiguousURIs(t *testing.T) {
	if _, err := canonicalClientIdentity(&x509.Certificate{Subject: pkix.Name{CommonName: "trusted"}}); err == nil {
		t.Fatal("CN accepted")
	}
	u1, _ := url.Parse("spiffe://b/x")
	u2, _ := url.Parse("spiffe://a/x")
	got, err := canonicalClientIdentity(&x509.Certificate{URIs: []*url.URL{u1, u2}})
	if err != nil || got != "spiffe://a/x" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestMTLSFixtureActuallyVerifiesClientCertificate(t *testing.T) {
	clientCert, pool, err := testCertificate()
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {}}}, newFakeBackend())
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()
	if _, err := srv.Client().Post(srv.URL+executionenv.BasePath+"/profiles/validate", "application/json", strings.NewReader(`{"profile":"go"}`)); err == nil {
		t.Fatal("server accepted connection without client certificate")
	}
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{clientCert}
	client := &http.Client{Transport: transport}
	resp, err := client.Post(srv.URL+executionenv.BasePath+"/profiles/validate", "application/json", strings.NewReader(`{"profile":"go"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func testCertificate() (tls.Certificate, *x509.CertPool, error) {
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caPriv)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	clientPub, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	identity, _ := url.Parse("spiffe://cluster/ns/mecak8s")
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ignored-cn"}, URIs: []*url.URL{identity}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, clientPub, caPriv)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(clientPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	parsedCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool.AddCert(parsedCA)
	return cert, pool, err
}
