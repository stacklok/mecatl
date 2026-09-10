package oidcclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	keyringapi "github.com/zalando/go-keyring"
)

const (
	keyringService = "mecatl/provider-oidc/v1"
	keyringDomain  = "mecatl/provider-oidc/keyring-account/v1\x00"
)

// Keyring supplies one credential-root-bound encryption key from the OS keyring.
type Keyring struct {
	root    string
	account string
}

// NewKeyring binds OS-keyring access to one explicit owner-only credential root.
func NewKeyring(root string) (*Keyring, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("%w: OIDC credential root must be absolute and clean", ErrStorage)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: OIDC credential root is unavailable or not owner-only", ErrStorage)
	}
	sum := sha256.Sum256(append([]byte(keyringDomain), []byte(root)...))
	return &Keyring{root: root, account: "root-" + hex.EncodeToString(sum[:])}, nil
}

// Key retrieves the root-bound key. create permits first creation but never a fallback backend.
func (k *Keyring) Key(ctx context.Context, create bool) ([]byte, error) {
	if k == nil {
		return nil, fmt.Errorf("%w: OIDC keyring is unavailable", ErrStorage)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(k.root, ".provider-oidc-keyring.lock"), flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: OIDC keyring lock is unavailable", ErrStorage)
	}
	if !locked {
		return nil, fmt.Errorf("%w: OIDC keyring lock was not acquired", ErrStorage)
	}
	defer func() { _ = lock.Unlock() }()
	if err := os.Chmod(lock.Path(), 0o600); err != nil {
		return nil, fmt.Errorf("%w: OIDC keyring lock could not be protected", ErrStorage)
	}
	encoded, err := keyringapi.Get(keyringService, k.account)
	if err == nil {
		return decodeKey(encoded)
	}
	if !errors.Is(err, keyringapi.ErrNotFound) || !create {
		return nil, fmt.Errorf("%w: OIDC keyring entry is unavailable", ErrStorage)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("%w: OIDC encryption key generation failed", ErrStorage)
	}
	if err := keyringapi.Set(keyringService, k.account, base64.RawStdEncoding.EncodeToString(key)); err != nil {
		clear(key)
		return nil, fmt.Errorf("%w: OIDC keyring entry could not be created", ErrStorage)
	}
	return key, nil
}

func decodeKey(encoded string) ([]byte, error) {
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, fmt.Errorf("%w: OIDC keyring entry is corrupt", ErrStorage)
	}
	return key, nil
}
