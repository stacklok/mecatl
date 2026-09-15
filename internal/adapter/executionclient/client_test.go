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
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type integrationBackend struct {
	allocation  executioncontroller.Allocation
	ensureCalls int
	attachCalls int
	fileCalls   int
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
	if q.Operation == executionenv.OpFileResolveAuthority {
		return executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"}, nil
	}
	return executionenv.FileResponse{Data: []byte("from-real-handler"), Version: "v1"}, nil
}
func (*integrationBackend) StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	return executionenv.CommandStartResponse{}, nil
}
func (*integrationBackend) CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
}
func (*integrationBackend) CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
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
		tmpl.URIs = []*url.URL{{Scheme: "spiffe", Host: "example.test", Path: "/mecak8s"}}
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

func TestProviderThroughRealSignedHandlerRefreshesAndReattachesExactly(t *testing.T) {
	_, ca, caKey := certificate(t, nil, nil, "ca", false)
	serverTLS, _, _ := certificate(t, ca, caKey, "example.test", false)
	clientTLS, _, _ := certificate(t, ca, caKey, "client", true)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	backend := &integrationBackend{}
	handler := executioncontroller.NewHandler(executioncontroller.HandlerConfig{
		Clients:  map[string]executioncontroller.ClientPolicy{"spiffe://example.test/mecak8s": {MayAttestOwner: true}},
		Signer:   executioncontroller.GrantSigner{KeyID: "k1", PrivateKey: private, Issuer: "provider", Audience: "executor", Lifetime: 5 * time.Second},
		Verifier: executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": private.Public().(ed25519.PublicKey)}, Issuer: "provider", Audience: "executor", MaxLifetime: 10 * time.Second},
	}, backend)
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca)
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverTLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
	ts.StartTLS()
	defer ts.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	client, err := New(ts.URL, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{clientTLS}, ServerName: "example.test"})
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
	reattached, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Principal: principal, BindingID: "session-real"})
	if err != nil {
		t.Fatal(err)
	}
	resolver, ok := reattached.Environment.Workspace().(tool.AuthorityResourceResolver)
	if !ok {
		t.Fatal("remote workspace does not implement AuthorityResourceResolver")
	}
	target, workspace, err := resolver.AuthorityResourcePath("main.go")
	if err != nil || target != "/workspace/main.go" || workspace != "/workspace" {
		t.Fatalf("authority resource=(%q,%q) err=%v", target, workspace, err)
	}
	data, err := reattached.Environment.Workspace().Read(context.Background(), "main.go")
	if err != nil || string(data) != "from-real-handler" {
		t.Fatalf("read=%q err=%v", data, err)
	}
	if backend.ensureCalls != 1 || backend.attachCalls < 3 || backend.fileCalls != 2 {
		t.Fatalf("ensure=%d attach=%d file=%d", backend.ensureCalls, backend.attachCalls, backend.fileCalls)
	}
}

func TestProviderMTLSWorkspaceAndForegroundRunner(t *testing.T) {
	caTLS, ca, caKey := certificate(t, nil, nil, "ca", false)
	_ = caTLS
	serverTLS, _, _ := certificate(t, ca, caKey, "example.test", false)
	clientTLS, _, _ := certificate(t, ca, caKey, "client", true)
	clientPool := x509.NewCertPool()
	clientPool.AddCert(ca)
	calls := map[string]int{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("request had no verified client certificate")
		}
		calls[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case executionenv.BasePath + "/profiles/validate":
			json.NewEncoder(w).Encode(executionenv.ValidateProfileResponse{Profile: "coding", Digest: "sha256:test"})
		case executionenv.BasePath + "/environments/ensure":
			json.NewEncoder(w).Encode(executionenv.EnsureEnvironmentResponse{Environment: executionenv.EnvironmentRef{ID: "env-1", Revision: "rev-1"}, Epoch: 2, Ready: true, Grant: "grant-1", GrantExpiresAt: time.Now().Add(time.Minute)})
		case executionenv.BasePath + "/environments/attach":
			json.NewEncoder(w).Encode(executionenv.AttachEnvironmentResponse{Environment: executionenv.EnvironmentRef{ID: "env-1", Revision: "rev-1"}, Epoch: 2, Ready: true, Grant: "grant-2", GrantExpiresAt: time.Now().Add(time.Minute)})
		case executionenv.BasePath + "/files":
			var q executionenv.FileRequest
			json.NewDecoder(r.Body).Decode(&q)
			if q.Context.BindingID != "session-1" || q.Context.Grant != "grant-2" {
				t.Errorf("file context = %+v", q.Context)
			}
			if q.Operation == executionenv.OpFileResolveAuthority {
				if q.Path == "escape" {
					w.WriteHeader(http.StatusForbidden)
					json.NewEncoder(w).Encode(executionenv.ErrorResponse{Error: &executionenv.Error{Code: executionenv.CodePermissionDenied, Message: "path is outside workspace"}})
				} else {
					json.NewEncoder(w).Encode(executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"})
				}
			} else {
				json.NewEncoder(w).Encode(executionenv.FileResponse{Data: []byte("remote"), Version: "v1"})
			}
		case executionenv.BasePath + "/commands/start":
			json.NewEncoder(w).Encode(executionenv.CommandStartResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Result: executionenv.CommandStatusResponse{CommandID: "c1", State: executionenv.CommandSucceeded, Stdout: []byte("ok\n"), Stderr: []byte("warn\n")}})
		default:
			http.NotFound(w, r)
		}
	})
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverTLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientPool}
	ts.StartTLS()
	defer ts.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	client, err := New(ts.URL, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{clientTLS}, ServerName: "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := NewProvider(client, "coding")
	if err := provider.ValidatePlacement(context.Background()); err != nil {
		t.Fatal(err)
	}
	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: principal, Scope: "remote", Operation: server.PlacementOperationCreate, BindingID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	resolver := binding.Environment.Workspace().(tool.AuthorityResourceResolver)
	target, root, err := resolver.AuthorityResourcePath("main.go")
	if err != nil || target != "/workspace/main.go" || root != "/workspace" {
		t.Fatalf("authority resource=(%q,%q) err=%v", target, root, err)
	}
	if target, root, err = resolver.AuthorityResourcePath("escape"); err == nil || target != "" || root != "" {
		t.Fatalf("escaped authority resource=(%q,%q) err=%v, want fail closed", target, root, err)
	}
	data, _, err := binding.Environment.Workspace().ReadVersion(context.Background(), "main.go")
	if err != nil || string(data) != "remote" {
		t.Fatalf("read=%q err=%v", data, err)
	}
	result, err := binding.Environment.CommandRunner().Run(context.Background(), "go test ./...")
	if err != nil || result.Stdout != "ok\n" || result.Stderr != "warn\n" {
		t.Fatalf("command=%+v err=%v", result, err)
	}
	if _, streaming := binding.Environment.CommandRunner().(tool.CommandStreamer); streaming {
		t.Fatal("remote runner unexpectedly advertises streaming")
	}
	if calls[executionenv.BasePath+"/profiles/validate"] != 1 || calls[executionenv.BasePath+"/environments/ensure"] != 1 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestProviderWaitsForAsyncReadinessWithoutReEnsuring(t *testing.T) {
	var ensureCalls, attachCalls int
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ref := executionenv.EnvironmentRef{ID: "env-pending", Revision: "rev-1"}
		switch r.URL.Path {
		case executionenv.BasePath + "/environments/ensure":
			ensureCalls++
			json.NewEncoder(w).Encode(executionenv.EnsureEnvironmentResponse{Environment: ref, Epoch: 1, Ready: false, Grant: "initial", GrantExpiresAt: time.Now().Add(time.Minute)})
		case executionenv.BasePath + "/environments/attach":
			attachCalls++
			json.NewEncoder(w).Encode(executionenv.AttachEnvironmentResponse{Environment: ref, Epoch: 1, Ready: attachCalls > 1, Grant: "fresh", GrantExpiresAt: time.Now().Add(time.Minute)})
		default:
			http.NotFound(w, r)
		}
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	client, err := NewWithHTTPClient(ts.URL, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := NewProvider(client, "coding")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	if _, err := provider.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: principal, BindingID: "session-pending"}); err != nil {
		t.Fatal(err)
	}
	if ensureCalls != 1 || attachCalls != 2 {
		t.Fatalf("ensure=%d attach=%d, want 1/2", ensureCalls, attachCalls)
	}
}

func TestProviderReattachMissingNeverEnsures(t *testing.T) {
	var ensureCalls int
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == executionenv.BasePath+"/environments/ensure" {
			ensureCalls++
		}
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(executionenv.ErrorResponse{Error: &executionenv.Error{Code: executionenv.CodeNotFound, Message: "environment not found"}})
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	client, _ := NewWithHTTPClient(ts.URL, ts.Client())
	provider, _ := NewProvider(client, "coding")
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	_, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: session.EnvironmentRef{Kind: "kubernetes", ID: "deleted", Revision: "rev-1"}, Principal: principal, BindingID: "session-1"})
	if err == nil || ensureCalls != 0 {
		t.Fatalf("err=%v ensure=%d, want unavailable and zero ensure calls", err, ensureCalls)
	}
}

func TestProviderRequiresOwnerAndBinding(t *testing.T) {
	p := &Provider{}
	_, err := p.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement()})
	if err == nil {
		t.Fatal("ownerless/bindingless allocation succeeded")
	}
}
