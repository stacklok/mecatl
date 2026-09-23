package mcpbroker

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type countingRedisClient struct {
	redis.UniversalClient
	closeCalls int
	pingErr    error
}

func (c *countingRedisClient) Close() error {
	c.closeCalls++
	return c.UniversalClient.Close()
}
func (c *countingRedisClient) Ping(ctx context.Context) *redis.StatusCmd {
	if c.pingErr != nil {
		cmd := redis.NewStatusCmd(ctx)
		cmd.SetErr(c.pingErr)
		return cmd
	}
	return c.UniversalClient.Ping(ctx)
}

func protectedStorageTestConfig(t *testing.T, factory RedisClientFactory) ProtectedStorageConfig {
	t.Helper()
	key := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x42}, credentialKeyBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	return ProtectedStorageConfig{Redis: ProtectedRedisConfig{
		Client:        factory,
		ClientConfig:  ProtectedRedisClientConfig{Addr: "redis.example:6379", TLS: true, DialTimeout: time.Second, OperationTimeout: time.Second},
		HealthTimeout: time.Second,
	}, Encryption: ProtectedEncryptionConfig{ActiveID: "active", Keys: []ProtectedEncryptionKey{{ID: "active", File: key}}}}
}

func TestProtectedStorageFailureBeforeTransferClosesClient(t *testing.T) {
	called := false
	missing := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		called = true
		return nil, errors.New("must not create client")
	})
	missing.Encryption.Keys[0].File = filepath.Join(t.TempDir(), "missing")
	if _, err := buildProtectedToolHiveStorage(t.Context(), missing, missing.Redis.Client); err == nil {
		t.Fatal("missing KEK accepted")
	}
	if called {
		t.Fatal("client created before KEK validation")
	}
	var client *countingRedisClient
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		client = &countingRedisClient{UniversalClient: newMiniRedis(t), pingErr: errors.New("unavailable")}
		return client, nil
	})
	if _, err := buildProtectedToolHiveStorage(t.Context(), cfg, cfg.Redis.Client); err == nil {
		t.Fatal("unhealthy storage accepted")
	}
	if client.closeCalls != 1 {
		t.Fatalf("failed startup close calls = %d, want 1", client.closeCalls)
	}
}

func TestProtectedStorageNormalShutdownClosesClientExactlyOnce(t *testing.T) {
	var client *countingRedisClient
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		client = &countingRedisClient{UniversalClient: newMiniRedis(t)}
		return client, nil
	})
	protected, err := buildProtectedToolHiveStorage(t.Context(), cfg, cfg.Redis.Client)
	if err != nil {
		t.Fatal(err)
	}
	auth, _, err := newToolHiveAuthServer(t.Context(), "https://broker.example", []authserver.UpstreamRunConfig{{Name: "upstream", Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: &authserver.OAuth2UpstreamRunConfig{AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", RedirectURI: "https://broker.example/callback"}}}, protected.storage)
	if err != nil {
		t.Fatal(err)
	}
	_ = auth.Close()
	_ = protected.Close()
	_ = protected.Close()
	if client.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", client.closeCalls)
	}
}

func TestProtectedStorageReadinessTracksHealth(t *testing.T) {
	client := &countingRedisClient{UniversalClient: newMiniRedis(t)}
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) { return client, nil })
	protected, err := buildProtectedToolHiveStorage(t.Context(), cfg, cfg.Redis.Client)
	if err != nil {
		t.Fatal(err)
	}
	p := &Process{Runtime: &Runtime{}, cancel: func() {}, protectedStorage: protected}
	client.pingErr = errors.New("down")
	if err := p.Ready(t.Context()); err == nil {
		t.Fatal("readiness accepted failed health")
	}
	client.pingErr = nil
	if err := p.Ready(t.Context()); err != nil {
		t.Fatalf("readiness after recovery: %v", err)
	}
}
func TestProtectedStorageProjectedSecretReads(t *testing.T) {
	root := t.TempDir()
	projected := filepath.Join(root, "..data")
	if err := os.Mkdir(projected, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "kek")
	want := bytes.Repeat([]byte{0xa5}, credentialKeyBytes)
	if err := os.WriteFile(filepath.Join(projected, "kek"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "kek"), path); err != nil {
		t.Fatal(err)
	}
	got, err := readProtectedKEK(path)
	if err != nil {
		t.Fatalf("readProtectedKEK: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("KEK = %x, want %x", got, want)
	}
	if err := os.WriteFile(path, append(want, 0x01), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedKEK(path); err == nil {
		t.Fatal("oversized KEK accepted")
	}
	if _, err := readProtectedKEK(root); err == nil {
		t.Fatal("directory accepted as KEK")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, want, 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedKEK(escape); err == nil {
		t.Fatal("escaping symlink accepted")
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedKEK(fifo); err == nil {
		t.Fatal("FIFO accepted as KEK")
	}
}

func TestEncryptedAuthStorageIsNotUnwrappable(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := newEncryptedAuthStorage(inner, testCredentialKeyRing(t))
	if err != nil {
		t.Fatal(err)
	}
	if unwrapped := storage.Unwrap(decorated); unwrapped != decorated {
		t.Fatalf("storage.Unwrap returned %T, want encrypted decorator", unwrapped)
	}
	auth, _, err := newToolHiveAuthServer(t.Context(), "https://broker.example", []authserver.UpstreamRunConfig{{
		Name: "upstream", Type: authserver.UpstreamProviderTypeOAuth2,
		OAuth2Config: &authserver.OAuth2UpstreamRunConfig{AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", RedirectURI: "https://broker.example/callback"},
	}}, decorated)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = auth.Close() }()
	if dcr := auth.DCRStore(); dcr != decorated {
		t.Fatalf("ToolHive DCR store = %T, want encrypted decorator", dcr)
	}
}

// failAfterPingHook lets connection setup and the startup health check pass and
// fails every later Redis command, so process construction fails after protected storage has
// already been transferred into the process.
type failAfterPingHook struct{}

func (failAfterPingHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func setupCommand(cmd redis.Cmder) bool {
	switch cmd.Name() {
	case "ping", "hello", "client", "auth", "select":
		return true
	}
	return false
}

func (failAfterPingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if setupCommand(cmd) {
			return next(ctx, cmd)
		}
		cmd.SetErr(errors.New("redis unavailable"))
		return cmd.Err()
	}
}
func (failAfterPingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if !setupCommand(cmd) {
				for _, failed := range cmds {
					failed.SetErr(errors.New("redis unavailable"))
				}
				return errors.New("redis unavailable")
			}
		}
		return next(ctx, cmds) // connection initialization pipeline
	}
}

// Every construction failure after storage transfer runs the same process
// rollback; this pins it at the first Redis-backed auth-server step.
func TestProtectedStorageAuthConstructionFailureClosesClient(t *testing.T) {
	var client *countingRedisClient
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		inner := newMiniRedis(t)
		inner.AddHook(failAfterPingHook{})
		client = &countingRedisClient{UniversalClient: inner}
		return client, nil
	})
	_, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{protectedToolHiveProfile("private")},
		ProtectedStorage: &cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "embedded authorization client") {
		t.Fatalf("construction error = %v, want failure registering the embedded authorization client", err)
	}
	if client == nil || client.closeCalls != 1 {
		t.Fatalf("rollback close calls = %v, want exactly 1", client)
	}
}

func TestProtectedStorageIsNeverBuiltForAnonymousProfiles(t *testing.T) {
	called := false
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		called = true
		return nil, errors.New("must not create client")
	})
	var requests atomic.Int32
	anonymous := ToolHiveProfile{Name: "public", URL: toolHiveDiscoveryServer(t, "status", &requests).URL, Auth: authNone}
	if _, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{Profiles: []ToolHiveProfile{anonymous}, ProtectedStorage: &cfg}); err == nil {
		t.Fatal("protected storage accepted for an anonymous-only process")
	}
	if called {
		t.Fatal("Redis client created for an anonymous-only process")
	}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{Profiles: []ToolHiveProfile{anonymous}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if process.protectedStorage != nil {
		t.Fatal("anonymous process holds protected storage")
	}
	if err := process.Ready(t.Context()); err != nil {
		t.Fatalf("anonymous readiness: %v", err)
	}
}

func TestProtectedAdmissionChecksHealthBeforeCreatingState(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"unused","token_type":"Bearer"}`))
	}))
	defer tokenServer.Close()
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{}}, "github")
	client := &countingRedisClient{UniversalClient: newMiniRedis(t)}
	cfg := protectedStorageTestConfig(t, func(ProtectedRedisClientConfig) (redis.UniversalClient, error) { return client, nil })
	protected, err := buildProtectedToolHiveStorage(t.Context(), cfg, cfg.Redis.Client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = protected.Close() })
	runtime.process.protectedStorage = protected
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	enroller := attached.(contract.WorkspaceEnrollmentHandle)

	client.pingErr = errors.New("down")
	if _, err := enroller.BeginWorkspaceEnrollment(t.Context()); err == nil {
		t.Fatal("enrollment admitted while protected storage is unhealthy")
	}
	runtime.mu.RLock()
	pending := len(runtime.states)
	runtime.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("rejected admission created %d callback states", pending)
	}

	client.pingErr = nil
	if _, err := enroller.BeginWorkspaceEnrollment(t.Context()); err != nil {
		t.Fatalf("enrollment after storage recovery: %v", err)
	}
}
