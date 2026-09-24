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
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redisconn"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
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
		out[i] = fmt.Sprint(value)
	}
	return out
}

func TestRedisWatchDiagnosticsUseReasonCodesOnly(t *testing.T) {
	server := miniredis.RunT(t)
	_, ca := reloadTLSFixture(t)
	dir := t.TempDir()
	caFile := reloadWrite(t, dir, "ca.pem", ca)
	diagnostics := &capturedDiagnostics{}
	secret := "secret-watch-error-marker"
	cfg := Config{Addr: server.Addr(), CAFile: caFile, Diagnostics: diagnostics}
	deps := defaultStoreDependencies()
	deps.initialClient = func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
		return redis.NewClient(&redis.Options{Addr: server.Addr()}), nil
	}
	deps.watcher = func(_ []string, _ time.Duration, _ time.Duration, _ func(), onError func(error)) (*filewatch.Watcher, error) {
		onError(fmt.Errorf("%s at %s", secret, caFile))
		return nil, errors.New("stop after diagnostic")
	}
	_, err := newWithConfig(cfg, deps)
	if err == nil {
		t.Fatal("NewWithConfig succeeded with failing watcher fixture")
	}
	logs := diagnostics.text()
	if !strings.Contains(logs, "reason watch_error") {
		t.Fatalf("watch diagnostic missing reason code: %s", logs)
	}
	for _, forbidden := range []string{secret, caFile, ca} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("watch diagnostic leaked %q: %s", forbidden, logs)
		}
	}
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
	cfg := Config{Addr: server.Addr(), CAFile: caFile, PasswordFile: passwordFile, Diagnostics: diagnostics}
	deps := defaultStoreDependencies()
	deps.candidate = passwordCandidateFactory("new-secret-value", "old", nil)
	store, err := newWithConfig(cfg, deps)
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

func TestProductionCandidateRotatesCAAndPasswordTransactionally(t *testing.T) {
	tlsOne, caOne := reloadTLSFixture(t)
	backendOne, err := miniredis.RunTLS(tlsOne)
	if err != nil {
		t.Fatal(err)
	}
	defer backendOne.Close()
	backendOne.RequireAuth("password-one")

	tlsTwo, caTwo := reloadTLSFixture(t)
	backendTwo, err := miniredis.RunTLS(tlsTwo)
	if err != nil {
		t.Fatal(err)
	}
	defer backendTwo.Close()
	backendTwo.RequireAuth("password-two")

	proxy := newSwitchingProxy(t, backendOne.Addr())
	defer proxy.Close()
	dir := t.TempDir()
	caFile := reloadWrite(t, dir, "ca.pem", caOne)
	passwordFile := reloadWrite(t, dir, "password", "password-one")
	diagnostics := &capturedDiagnostics{}
	store, err := NewWithConfig(Config{Addr: proxy.Addr(), CAFile: caFile, PasswordFile: passwordFile, Diagnostics: diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := store.testClient()
	oldFollow := store.testFollowClient()

	reloadWrite(t, dir, "ca.pem", caTwo)
	awaitDiagnostic(t, diagnostics, "retrying")
	if store.testClient() != old {
		t.Fatal("untrusted intermediate CA displaced active client")
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("old generation failed after rejected CA: %v", err)
	}

	proxy.Switch(backendTwo.Addr())
	reloadWrite(t, dir, "password", "password-two")
	awaitClientChange(t, store, old)
	if store.testFollowClient() == oldFollow {
		t.Fatal("rotated production configuration left the follow client on the old generation")
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("rotated production client failed: %v", err)
	}
}

type switchingProxy struct {
	listener net.Listener
	mu       sync.RWMutex
	target   string
	done     chan struct{}
}

func newSwitchingProxy(t *testing.T, target string) *switchingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &switchingProxy{listener: listener, target: target, done: make(chan struct{})}
	go p.serve()
	return p
}

func (p *switchingProxy) Addr() string { return p.listener.Addr().String() }

func (p *switchingProxy) Switch(target string) {
	p.mu.Lock()
	p.target = target
	p.mu.Unlock()
}

func (p *switchingProxy) Close() {
	_ = p.listener.Close()
	<-p.done
}

func (p *switchingProxy) serve() {
	defer close(p.done)
	for {
		front, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.RLock()
		target := p.target
		p.mu.RUnlock()
		go proxyConnection(front, target)
	}
}

func proxyConnection(front net.Conn, target string) {
	defer front.Close()
	back, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer back.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(back, front); done <- struct{}{} }()
	go func() { _, _ = io.Copy(front, back); done <- struct{}{} }()
	<-done
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
	cfg := Config{Addr: server.Addr(), CAFile: reloadWrite(t, dir, "ca.pem", ca), PasswordFile: passwordFile, Diagnostics: diagnostics}
	deps := defaultStoreDependencies()
	deps.candidate = passwordCandidateFactory("eventual", "old", &redisReady)
	store, err := newWithConfig(cfg, deps)
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
	store, err := NewWithConfig(Config{Addr: server.Addr(), CAFile: caFile, Diagnostics: diagnostics})
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

func TestConnectionConfigRejectsNonRegularCredentialTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T) string
	}{
		{name: "directory", build: func(t *testing.T) string { return t.TempDir() }},
		{name: "device", build: func(*testing.T) string { return "/dev/null" }},
		{name: "fifo", build: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "credential.fifo")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.build(t)
			called := false
			_, err := connectionConfigWithReader(Config{Addr: "127.0.0.1:6379", TLS: true, PasswordFile: path}, func(*os.File, int64) ([]byte, error) {
				called = true
				return nil, errors.New("must not read")
			})
			if err == nil || !strings.Contains(err.Error(), "not regular") {
				t.Fatalf("connectionConfig error = %v", err)
			}
			if called {
				t.Fatal("non-regular credential target was read")
			}
		})
	}
}

func TestConnectionConfigRejectsOversizedCredentialBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxCredentialFileSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = connectionConfigWithReader(Config{Addr: "127.0.0.1:6379", TLS: true, PasswordFile: path}, func(*os.File, int64) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("connectionConfig error = %v", err)
	}
	if called {
		t.Fatal("oversized credential was read")
	}
}

func TestReloadLifecycleCloseIsBoundedByJoinGrace(t *testing.T) {
	server := miniredis.RunT(t)
	initial := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	diagnostics := &capturedDiagnostics{}
	store := &Store{clients: newClientGenerations(initial), diagnostics: diagnostics, closeGrace: 20 * time.Millisecond}
	dir := t.TempDir()
	passwordFile := reloadWrite(t, dir, "password", "secret")
	cfg := Config{Addr: server.Addr(), TLS: true, PasswordFile: passwordFile, Diagnostics: diagnostics}
	readStarted := make(chan struct{})
	continueRead := make(chan struct{})
	var readOnce sync.Once
	var candidate *closeTrackingClient
	deps := defaultStoreDependencies()
	deps.joinGrace = 20 * time.Millisecond
	deps.readFile = func(file *os.File, maxBytes int64) ([]byte, error) {
		readOnce.Do(func() { close(readStarted) })
		<-continueRead
		return readCredentialFile(file, maxBytes)
	}
	deps.candidate = func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
		candidate = &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
		return candidate, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	lifecycle := &reloadLifecycle{cancel: cancel, done: done, diagnostics: diagnostics, joinGrace: deps.joinGrace}
	store.reload = lifecycle
	go runReload(ctx, done, events, store, cfg, deps, diagnostics)
	events <- struct{}{}
	<-readStarted

	started := time.Now()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Store.Close blocked on credential read for %v", elapsed)
	}
	if got := strings.Count(diagnostics.text(), "reason reload_worker count 1"); got != 1 {
		t.Fatalf("reload-worker timeout warnings = %d, logs=%q", got, diagnostics.text())
	}

	close(continueRead)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload worker did not exit after stalled read completed")
	}
	deadline := time.Now().Add(time.Second)
	for candidate != nil && candidate.closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if candidate == nil || candidate.closes.Load() != 1 {
		t.Fatal("late candidate was not rejected and closed")
	}
}

func TestConnectionConfigRejectsProjectedMixedGeneration(t *testing.T) {
	root := t.TempDir()
	_, caOne := reloadTLSFixture(t)
	_, caTwo := reloadTLSFixture(t)
	v1 := filepath.Join(root, "..v1")
	if err := os.Mkdir(v1, 0o700); err != nil {
		t.Fatal(err)
	}
	reloadWrite(t, v1, "ca.pem", caOne)
	reloadWrite(t, v1, "password", "password-one")
	v2 := filepath.Join(root, "..v2")
	if err := os.Mkdir(v2, 0o700); err != nil {
		t.Fatal(err)
	}
	reloadWrite(t, v2, "ca.pem", caTwo)
	reloadWrite(t, v2, "password", "password-two")
	if err := os.Symlink("..v1", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.pem", "password"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	passwordRead := make(chan struct{})
	continueRead := make(chan struct{})
	reader := func(file *os.File, maxBytes int64) ([]byte, error) {
		if file.Name() == filepath.Join(root, "password") {
			close(passwordRead)
			<-continueRead
		}
		return readCredentialFile(file, maxBytes)
	}
	result := make(chan error, 1)
	go func() {
		_, err := connectionConfigWithReader(Config{
			Addr: "127.0.0.1:6379", CAFile: filepath.Join(root, "ca.pem"), PasswordFile: filepath.Join(root, "password"),
		}, reader)
		result <- err
	}()
	<-passwordRead
	next := filepath.Join(root, "..data.next")
	if err := os.Symlink("..v2", next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	close(continueRead)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "changed while being read") {
			t.Fatalf("connectionConfig error = %v, want mixed-generation rejection", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connectionConfig did not finish")
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
	cfg := Config{Addr: server.Addr(), CAFile: reloadWrite(t, root, "ca.pem", ca), PasswordFile: filepath.Join(root, "password")}
	deps := defaultStoreDependencies()
	deps.candidate = passwordCandidateFactory("new", "old", nil)
	store, err := newWithConfig(cfg, deps)
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
	cfg := Config{Addr: server.Addr(), AllowPlaintext: true}
	deps := defaultStoreDependencies()
	deps.candidate = func(ctx context.Context, cfg *tcredis.Config) (redis.UniversalClient, error) {
		client, err := tcredis.NewClient(ctx, cfg)
		if err != nil {
			return nil, err
		}
		tracked = &closeTrackingClient{UniversalClient: client}
		return tracked, nil
	}
	if err := reloadCandidate(context.Background(), store, cfg, deps); !errors.Is(err, errStoreClosed) {
		t.Fatalf("reload after close: %v", err)
	}
	if tracked == nil || tracked.closes.Load() != 1 {
		t.Fatal("rejected candidate was not closed")
	}
}

func TestReloadExhaustionWaitsForNewFileEvent(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	var attempts atomic.Int32
	nextAttempt := make(chan struct{})
	cfg := Config{Addr: "127.0.0.1:1", AllowPlaintext: true}
	deps := defaultStoreDependencies()
	deps.backoff = func(int) time.Duration { return 0 }
	deps.jitter = nil
	deps.candidate = func(ctx context.Context, _ *tcredis.Config) (redis.UniversalClient, error) {
		attempt := attempts.Add(1)
		if attempt <= reloadMaxAttempts {
			return nil, errors.New("candidate rejected")
		}
		close(nextAttempt)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	diagnostics := &capturedDiagnostics{}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go runReload(ctx, done, events, store, cfg, deps, diagnostics)
	events <- struct{}{}
	awaitDiagnostic(t, diagnostics, "outcome failed attempts 8 reason candidate_rejected")
	if got := attempts.Load(); got != reloadMaxAttempts {
		t.Fatalf("attempts after exhaustion = %d, want %d", got, reloadMaxAttempts)
	}
	select {
	case <-nextAttempt:
		t.Fatal("reload attempted again without a new file event")
	default:
	}

	events <- struct{}{}
	select {
	case <-nextAttempt:
	case <-time.After(2 * time.Second):
		t.Fatal("new file event did not reset exhausted attempts")
	}
	cancel()
	<-done
	if got := attempts.Load(); got != reloadMaxAttempts+1 {
		t.Fatalf("attempts after reset event = %d, want %d", got, reloadMaxAttempts+1)
	}
}

func TestReloadEventDuringAttemptRestartsAtAttemptOne(t *testing.T) {
	store, server := newGenerationTestStore(t)
	var calls atomic.Int32
	started := make(chan struct{})
	deps := defaultStoreDependencies()
	deps.candidate = func(ctx context.Context, _ *tcredis.Config) (redis.UniversalClient, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return redis.NewClient(&redis.Options{Addr: server.Addr()}), nil
	}
	cfg := Config{Addr: server.Addr(), AllowPlaintext: true}
	diagnostics := &capturedDiagnostics{}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go runReload(ctx, done, events, store, cfg, deps, diagnostics)
	events <- struct{}{}
	<-started
	events <- struct{}{}
	awaitDiagnostic(t, diagnostics, "outcome succeeded attempt 1")
	cancel()
	<-done
	if got := calls.Load(); got != 2 {
		t.Fatalf("candidate calls = %d, want 2", got)
	}
}

func TestReloadEventDuringBackoffRestartsAtAttemptOne(t *testing.T) {
	store, server := newGenerationTestStore(t)
	var calls atomic.Int32
	deps := defaultStoreDependencies()
	deps.backoff = func(int) time.Duration { return time.Hour }
	deps.jitter = nil
	deps.candidate = func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("candidate rejected")
		}
		return redis.NewClient(&redis.Options{Addr: server.Addr()}), nil
	}
	cfg := Config{Addr: server.Addr(), AllowPlaintext: true}
	diagnostics := &capturedDiagnostics{}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go runReload(ctx, done, events, store, cfg, deps, diagnostics)
	events <- struct{}{}
	awaitDiagnostic(t, diagnostics, "outcome retrying attempt 1")
	events <- struct{}{}
	awaitDiagnostic(t, diagnostics, "outcome succeeded attempt 1")
	cancel()
	<-done
	if got := calls.Load(); got != 2 {
		t.Fatalf("candidate calls = %d, want 2", got)
	}
}

func TestReloadRetryJitterIsBoundedCappedAndDeterministic(t *testing.T) {
	deps := storeDependencies{
		backoff: func(int) time.Duration { return 1500 * time.Millisecond },
		jitter:  func(delay time.Duration) time.Duration { return delay + 250*time.Millisecond },
	}
	if got := reloadRetryDelay(3, time.Second, deps); got != 1750*time.Millisecond {
		t.Fatalf("deterministic jitter delay = %v", got)
	}
	deps.jitter = func(time.Duration) time.Duration { return 3 * time.Second }
	if got := reloadRetryDelay(3, time.Second, deps); got != reloadMaxBackoff {
		t.Fatalf("capped jitter delay = %v", got)
	}

	var jitterBase time.Duration
	deps.backoff = func(int) time.Duration { return 10 * reloadMaxBackoff }
	deps.jitter = func(delay time.Duration) time.Duration {
		jitterBase = delay
		return delay + 100*time.Millisecond
	}
	if got := reloadRetryDelay(9, time.Second, deps); got != 1700*time.Millisecond {
		t.Fatalf("maximum-base positive jitter delay = %v", got)
	}
	if jitterBase != 1600*time.Millisecond {
		t.Fatalf("maximum jitter base = %v", jitterBase)
	}

	defaults := defaultStoreDependencies()
	for range 100 {
		got := reloadRetryDelay(1, time.Second, defaults)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("default jitter out of bounds: %v", got)
		}
	}
}

func TestReloadEventsStaySingleFlightAndShutdownCancelsCandidate(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	_, ca := reloadTLSFixture(t)
	dir := t.TempDir()
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 1)
	cfg := Config{Addr: "127.0.0.1:1", CAFile: reloadWrite(t, dir, "ca.pem", ca)}
	deps := defaultStoreDependencies()
	deps.candidate = func(ctx context.Context, _ *tcredis.Config) (redis.UniversalClient, error) {
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
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go runReload(ctx, done, events, store, cfg, deps, port.NopDiagnostics{})
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
