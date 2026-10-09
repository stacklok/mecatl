package mcpbroker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialenvelope"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialstore"
)

func newMiniRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

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
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x42}, credentialenvelope.KeyBytes), 0o600); err != nil {
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
	auth, err := runner.NewEmbeddedAuthServerWithStorage(t.Context(), &authserver.RunConfig{
		SchemaVersion: "v1", Issuer: "https://broker.example", AllowedAudiences: []string{"https://broker.example"},
		Upstreams: []authserver.UpstreamRunConfig{{Name: "upstream", Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: &authserver.OAuth2UpstreamRunConfig{AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", RedirectURI: "https://broker.example/callback"}}},
	}, protected.storage)
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

func TestProtectedStorageProjectedSecretReads(t *testing.T) {
	root := t.TempDir()
	projected := filepath.Join(root, "..data")
	if err := os.Mkdir(projected, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "kek")
	want := bytes.Repeat([]byte{0xa5}, credentialenvelope.KeyBytes)
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
	decorated, err := credentialstore.New(inner, testCredentialKeyRing(t), toolHiveAuthStoragePrefix)
	if err != nil {
		t.Fatal(err)
	}
	if unwrapped := storage.Unwrap(decorated); unwrapped != decorated {
		t.Fatalf("storage.Unwrap returned %T, want encrypted decorator", unwrapped)
	}
	auth, err := runner.NewEmbeddedAuthServerWithStorage(t.Context(), &authserver.RunConfig{
		SchemaVersion: "v1", Issuer: "https://broker.example", AllowedAudiences: []string{"https://broker.example"},
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: "upstream", Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", RedirectURI: "https://broker.example/callback"},
		}},
	}, decorated)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = auth.Close() }()
	if dcr := auth.DCRStore(); dcr != decorated {
		t.Fatalf("ToolHive DCR store = %T, want encrypted decorator", dcr)
	}
}
