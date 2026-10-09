package mcpbroker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialenvelope"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialstore"
)

const maxProtectedKEKBytes = credentialenvelope.KeyBytes

// ProtectedStorageConfig configures encrypted ToolHive credential storage.
type ProtectedStorageConfig struct {
	Redis      ProtectedRedisConfig
	Encryption ProtectedEncryptionConfig
}

// ProtectedRedisClientConfig is the adapter-neutral input passed to the
// composition-owned Redis client factory.
type ProtectedRedisClientConfig struct {
	Addr             string
	UsernameFile     string
	PasswordFile     string
	CAFile           string
	TLS              bool
	AllowPlaintext   bool
	DialTimeout      time.Duration
	OperationTimeout time.Duration
}

// RedisClientFactory constructs the Redis client used for protected ToolHive storage.
type RedisClientFactory func(ProtectedRedisClientConfig) (redis.UniversalClient, error)

// ProtectedRedisConfig configures the broker-owned Redis client.
type ProtectedRedisConfig struct {
	Client        RedisClientFactory
	ClientConfig  ProtectedRedisClientConfig
	HealthTimeout time.Duration
}

// ProtectedEncryptionConfig configures the immutable KEK ring.
type ProtectedEncryptionConfig struct {
	ActiveID string
	Keys     []ProtectedEncryptionKey
}

// ProtectedEncryptionKey names one mounted 32-byte KEK.
type ProtectedEncryptionKey struct{ ID, File string }

type protectedToolHiveStorage struct {
	client        redis.UniversalClient
	storage       *credentialstore.Storage
	keys          *credentialenvelope.KeyRing
	healthTimeout time.Duration
	closeOnce     sync.Once
	closeErr      error
}

func newProtectedToolHiveStorage(ctx context.Context, cfg ProtectedStorageConfig) (*protectedToolHiveStorage, error) {
	return buildProtectedToolHiveStorage(ctx, cfg, cfg.Redis.Client)
}

func buildProtectedToolHiveStorage(ctx context.Context, cfg ProtectedStorageConfig, makeClient RedisClientFactory) (*protectedToolHiveStorage, error) {
	clientConfig := cfg.Redis.ClientConfig
	if makeClient == nil {
		return nil, credentialenvelope.ErrUnavailable
	}
	if !clientConfig.TLS || clientConfig.AllowPlaintext || cfg.Redis.HealthTimeout <= 0 || cfg.Redis.HealthTimeout > 30*time.Second || len(cfg.Encryption.Keys) == 0 {
		return nil, credentialenvelope.ErrUnavailable
	}
	keys := make(map[string][]byte, len(cfg.Encryption.Keys))
	for _, item := range cfg.Encryption.Keys {
		if _, exists := keys[item.ID]; exists || item.ID == "" {
			return nil, credentialenvelope.ErrUnavailable
		}
		body, err := readProtectedKEK(item.File)
		if err != nil {
			return nil, credentialenvelope.ErrUnavailable
		}
		keys[item.ID] = body
	}
	ring, err := credentialenvelope.NewKeyRing(cfg.Encryption.ActiveID, keys)
	if err != nil {
		return nil, err
	}
	client, err := makeClient(clientConfig)
	if err != nil {
		return nil, errors.New("protected storage unavailable")
	}
	inner := storage.NewRedisStorageWithClient(client, toolHiveAuthStoragePrefix)
	decorated, err := credentialstore.New(inner, ring, toolHiveAuthStoragePrefix)
	if err != nil {
		_ = client.Close()
		return nil, credentialenvelope.ErrUnavailable
	}
	out := &protectedToolHiveStorage{client: client, storage: decorated, keys: ring, healthTimeout: cfg.Redis.HealthTimeout}
	healthCtx, cancel := context.WithTimeout(ctx, cfg.Redis.HealthTimeout)
	defer cancel()
	if err := out.Health(healthCtx); err != nil {
		_ = out.Close()
		return nil, errors.New("protected storage unavailable")
	}
	return out, nil
}

func (s *protectedToolHiveStorage) Health(ctx context.Context) error {
	if s == nil || s.client == nil {
		return credentialenvelope.ErrUnavailable
	}
	return s.client.Ping(ctx).Err()
}
func (s *protectedToolHiveStorage) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.storage != nil {
			s.closeErr = s.storage.Close()
		} else if s.client != nil {
			s.closeErr = s.client.Close()
		}
	})
	return s.closeErr
}

func readProtectedKEK(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, credentialenvelope.ErrUnavailable
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, credentialenvelope.ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, credentialenvelope.ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != maxProtectedKEKBytes {
		return nil, credentialenvelope.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(f, maxProtectedKEKBytes+1))
	if err != nil || len(body) != maxProtectedKEKBytes {
		return nil, credentialenvelope.ErrUnavailable
	}
	return body, nil
}
