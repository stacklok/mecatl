package server

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
)

// Native donor fixtures remain broker-owned; no host attachment RPC is composed.
type continuityFixture struct {
	t                *testing.T
	issuerA, issuerB *multiUpstreamOIDC
	mcpA, mcpB       *multiUpstreamMCP
	redis            *miniredis.Miniredis
	keyFile          string
	roots            *x509.CertPool
	gateway          *httptest.Server
	profiles         []mcpbroker.ToolHiveProfile
	workload         *session.Principal
	owner            context.Context
	mu               sync.Mutex
	route            http.Handler
	process          *mcpbroker.Process
}

func newContinuityFixture(t *testing.T) *continuityFixture {
	t.Helper()
	f := &continuityFixture{t: t, issuerA: newMultiUpstreamOIDC(t, "backend-a"), issuerB: newMultiUpstreamOIDC(t, "backend-b"), mcpA: newMultiUpstreamMCP(t, "backend-a"), mcpB: newMultiUpstreamMCP(t, "backend-b"), redis: miniredis.RunT(t), workload: &session.Principal{Issuer: "https://kubernetes.default.svc", Subject: "system:serviceaccount:agents:mecak8s"}, owner: session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser})}
	f.keyFile = filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(f.keyFile, []byte("01234567890123456789012345678901"), 0600); err != nil {
		t.Fatal(err)
	}
	f.gateway = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		route := f.route
		f.mu.Unlock()
		route.ServeHTTP(w, r)
	}))
	f.gateway.StartTLS()
	t.Cleanup(f.gateway.Close)
	f.roots = x509.NewCertPool()
	f.roots.AddCert(f.gateway.Certificate())
	f.profiles = []mcpbroker.ToolHiveProfile{
		{Name: "backend-a", URL: f.mcpA.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerA.server.URL, ClientID: f.issuerA.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
		{Name: "backend-b", URL: f.mcpB.server.URL, Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{Issuer: f.issuerB.server.URL, ClientID: f.issuerB.clientID, Scopes: []string{"openid"}, RequestRefreshToken: true}},
	}
	f.startBroker()
	return f
}

func (f *continuityFixture) startBroker() *mcpbroker.Process {
	f.t.Helper()
	protected := &mcpbroker.ProtectedStorageConfig{Redis: mcpbroker.ProtectedRedisConfig{Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		return redis.NewClient(&redis.Options{Addr: f.redis.Addr()}), nil
	}, ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second}, Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: "current", Keys: []mcpbroker.ProtectedEncryptionKey{{ID: "current", File: f.keyFile}}}}
	process, err := mcpbroker.NewToolHiveProcess(f.t.Context(), mcpbroker.ToolHiveConfig{CallbackURL: f.gateway.URL + "/callback", Profiles: f.profiles, ProtectedStorage: protected}, mcpbroker.WithOAuthLoopbackForTest(f.t, f.roots), mcpbroker.WithBrokerHTTPClientForTest(f.t, f.gateway.Client()))
	if err != nil {
		f.t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.route, f.process = mux, process
	f.mu.Unlock()
	f.t.Cleanup(func() { _ = process.Close() })
	return process
}
func (f *continuityFixture) replaceBroker() {
	f.mu.Lock()
	old := f.process
	f.mu.Unlock()
	_ = old.Close()
	f.startBroker()
}
