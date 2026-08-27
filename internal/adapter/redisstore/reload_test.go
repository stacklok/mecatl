package redisstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redis"

	"github.com/stacklok/mecatl/engine/port"
)

type capturedDiagnostics struct {
	mu      sync.Mutex
	records []string
}

func (d *capturedDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, msg+" "+strings.TrimSpace(strings.Join(anyStrings(args), " ")))
}
func (d *capturedDiagnostics) With(...any) port.Diagnostics { return d }
func (d *capturedDiagnostics) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.records, "\n")
}
func anyStrings(values []any) []string {
	out := make([]string, len(values))
	for i, value := range values {
		if text, ok := value.(string); ok {
			out[i] = text
		}
	}
	return out
}

func TestPasswordRotationPublishesOnlyVerifiedCandidate(t *testing.T) {
	serverTLS, ca := reloadTLSFixture(t)
	server, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	dir := t.TempDir()
	passwordFile := reloadWrite(t, dir, "password", "old")
	caFile := reloadWrite(t, dir, "ca.pem", ca)
	server.RequireAuth("old")
	diagnostics := &capturedDiagnostics{}
	cfg := Config{Addr: server.Addr(), CAFile: caFile, PasswordFile: passwordFile, Reload: true, Diagnostics: diagnostics}
	cfg.candidateFactory = passwordCandidateFactory("new-secret-value", "old", nil)
	store, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := store.testClient()

	reloadWrite(t, dir, "password", "wrong-secret-value")
	awaitDiagnostic(t, diagnostics, "retrying")
	if store.testClient() != old {
		t.Fatal("invalid credential displaced the last valid client")
	}
	reloadWrite(t, dir, "password", "new-secret-value")
	awaitClientChange(t, store, old)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("rotated client ping: %v", err)
	}
	logs := diagnostics.text()
	for _, secret := range []string{"wrong-secret-value", "new-secret-value", ca} {
		if strings.Contains(logs, secret) {
			t.Fatalf("diagnostics leaked secret material: %q", secret)
		}
	}
}

func TestReloadRetriesUntilRedisAcceptsCredential(t *testing.T) {
	serverTLS, ca := reloadTLSFixture(t)
	server, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	dir := t.TempDir()
	passwordFile := reloadWrite(t, dir, "password", "old")
	server.RequireAuth("old")
	diagnostics := &capturedDiagnostics{}
	var redisReady atomic.Bool
	cfg := Config{Addr: server.Addr(), CAFile: reloadWrite(t, dir, "ca.pem", ca), PasswordFile: passwordFile, Reload: true, Diagnostics: diagnostics}
	cfg.candidateFactory = passwordCandidateFactory("eventual", "old", &redisReady)
	store, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := store.testClient()
	reloadWrite(t, dir, "password", "eventual")
	awaitDiagnostic(t, diagnostics, "retrying")
	redisReady.Store(true)
	awaitClientChange(t, store, old)
}

func TestUntrustedCARotationRetainsActiveClient(t *testing.T) {
	serverTLS, ca := reloadTLSFixture(t)
	server, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	dir := t.TempDir()
	caFile := reloadWrite(t, dir, "ca.pem", ca)
	diagnostics := &capturedDiagnostics{}
	store, err := NewWithConfig(Config{Addr: server.Addr(), CAFile: caFile, Reload: true, Diagnostics: diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := store.testClient()
	_, untrusted := reloadTLSFixture(t)
	reloadWrite(t, dir, "ca.pem", untrusted)
	awaitDiagnostic(t, diagnostics, "retrying")
	if store.testClient() != old {
		t.Fatal("untrusted CA displaced active client")
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("last valid client stopped working: %v", err)
	}
}

func TestProjectedPasswordSymlinkSwapReloads(t *testing.T) {
	serverTLS, ca := reloadTLSFixture(t)
	server, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	root := t.TempDir()
	projectReloadSecret(t, root, "..v1", "old")
	if err := os.Symlink("..v1", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "password"), filepath.Join(root, "password")); err != nil {
		t.Fatal(err)
	}
	server.RequireAuth("old")
	cfg := Config{Addr: server.Addr(), CAFile: reloadWrite(t, root, "ca.pem", ca), PasswordFile: filepath.Join(root, "password"), Reload: true}
	cfg.candidateFactory = passwordCandidateFactory("new", "old", nil)
	store, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := store.testClient()
	projectReloadSecret(t, root, "..v2", "new")
	if err := os.Symlink("..v2", filepath.Join(root, "..data.next")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "..data.next"), filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	awaitClientChange(t, store, old)
}

type closeTrackingClient struct {
	redis.UniversalClient
	closes atomic.Int32
}

func (c *closeTrackingClient) Close() error { c.closes.Add(1); return c.UniversalClient.Close() }

func TestRejectedCandidateIsClosed(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	var tracked *closeTrackingClient
	cfg := Config{Addr: server.Addr(), AllowPlaintext: true, candidateFactory: func(ctx context.Context, cfg *tcredis.Config) (redis.UniversalClient, error) {
		client, err := tcredis.NewClient(ctx, cfg)
		if err != nil {
			return nil, err
		}
		tracked = &closeTrackingClient{UniversalClient: client}
		return tracked, nil
	}}
	if err := reloadCandidate(context.Background(), store, cfg); !errors.Is(err, errStoreClosed) {
		t.Fatalf("reload after close: %v", err)
	}
	if tracked == nil || tracked.closes.Load() != 1 {
		t.Fatal("rejected candidate was not closed")
	}
}

func TestReloadEventsStaySingleFlightAndShutdownCancelsCandidate(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	_, ca := reloadTLSFixture(t)
	dir := t.TempDir()
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 1)
	cfg := Config{
		Addr:   "127.0.0.1:1",
		CAFile: reloadWrite(t, dir, "ca.pem", ca),
		candidateFactory: func(ctx context.Context, _ *tcredis.Config) (redis.UniversalClient, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				old := maximum.Load()
				if current <= old || maximum.CompareAndSwap(old, current) {
					break
				}
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go runReload(ctx, done, events, store, cfg, port.NopDiagnostics{})
	events <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate did not start")
	}
	for range 20 {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("parallel candidate count = %d, want 1", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel and join candidate retry")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active candidates after shutdown = %d", got)
	}
}

func passwordCandidateFactory(want, serverPassword string, ready *atomic.Bool) func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
	return func(ctx context.Context, cfg *tcredis.Config) (redis.UniversalClient, error) {
		if cfg.Password != want || (ready != nil && !ready.Load()) {
			return nil, errors.New("Redis has not accepted the projected credential generation")
		}
		verified := *cfg
		verified.Password = serverPassword
		return tcredis.NewClient(ctx, &verified)
	}
}

func awaitClientChange(t *testing.T, store *Store, old redis.UniversalClient) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if store.testClient() != old {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Redis generation change")
}
func awaitDiagnostic(t *testing.T, diagnostics *capturedDiagnostics, text string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(diagnostics.text(), text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for diagnostic %q; got %s", text, diagnostics.text())
}

func reloadTLSFixture(t *testing.T) (*tls.Config, string) {
	t.Helper()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "reload CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}
func reloadWrite(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func projectReloadSecret(t *testing.T, root, version, password string) {
	t.Helper()
	dir := filepath.Join(root, version)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reloadWrite(t, dir, "password", password)
}
