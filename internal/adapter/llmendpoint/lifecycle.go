package llmendpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

const (
	// CredentialNamespace is the isolated encrypted native-provider credential namespace.
	CredentialNamespace   = "mecatl/provider-oidc/v1"
	transactionLockDomain = "mecatl/provider-oidc/transaction-lock/v1\x00"
)

// ErrNotEnrolled reports absent, identity-drifted, corrupt, or unavailable protected state.
var ErrNotEnrolled = errors.New("native LLM endpoint is not enrolled")

// TrustIdentity binds one independently configured network trust policy.
type TrustIdentity struct{ Policy, CADigest string }

// CredentialIdentity is the complete endpoint-bound durable credential identity.
type CredentialIdentity struct {
	SchemaVersion                                           int
	EndpointID, Gateway, Issuer, ClientID, ResourceAudience string
	Scopes                                                  []string
	RedirectURI                                             string
	IssuerTrust, GatewayTrust                               TrustIdentity
}

// Token is transient OAuth material persisted only inside the protected record.
type Token struct {
	AccessToken, RefreshToken, TokenType string
	Expiry                               time.Time
}

// CredentialRecord carries a token and its exact opaque CAS version.
type CredentialRecord struct {
	Token   Token
	Version credentialstore.Version
}

type envelope struct {
	Schema   string             `json:"schema"`
	Version  int                `json:"version"`
	Identity CredentialIdentity `json:"identity"`
	Token    tokenJSON          `json:"token"`
}
type tokenJSON struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

const recordSchema = "mecatl.provider-oidc.credential"

// CredentialRepository protects identity validation and CAS over one namespace-bound store.
type CredentialRepository struct{ store credentialstore.Store }

// NewCredentialRepository binds record validation and CAS to store.
func NewCredentialRepository(store credentialstore.Store) *CredentialRepository {
	return &CredentialRepository{store: store}
}

// Load retrieves an exact-identity, supported-schema credential.
func (r *CredentialRepository) Load(ctx context.Context, id CredentialIdentity) (CredentialRecord, error) {
	if r == nil || r.store == nil {
		return CredentialRecord{}, ErrNotEnrolled
	}
	key, err := identityKey(id)
	if err != nil {
		return CredentialRecord{}, ErrNotEnrolled
	}
	rec, err := r.store.Get(ctx, key)
	if err != nil {
		return CredentialRecord{}, errors.Join(ErrNotEnrolled, err)
	}
	var env envelope
	if json.Unmarshal(rec.Value, &env) != nil || env.Schema != recordSchema || env.Version != 1 || !sameIdentity(env.Identity, id) || !validToken(fromJSON(env.Token)) {
		return CredentialRecord{}, ErrNotEnrolled
	}
	return CredentialRecord{Token: fromJSON(env.Token), Version: rec.Version}, nil
}

// Save creates or exact-version-replaces a credential and reconciles ambiguous commits.
func (r *CredentialRepository) Save(ctx context.Context, id CredentialIdentity, tok Token, expected *credentialstore.Version) (CredentialRecord, error) {
	if r == nil || r.store == nil || !validToken(tok) {
		return CredentialRecord{}, ErrNotEnrolled
	}
	key, err := identityKey(id)
	if err != nil {
		return CredentialRecord{}, ErrNotEnrolled
	}
	body, err := json.Marshal(envelope{Schema: recordSchema, Version: 1, Identity: canonicalIdentity(id), Token: toJSON(tok)})
	if err != nil {
		return CredentialRecord{}, ErrNotEnrolled
	}
	rec, putErr := r.store.Put(ctx, key, body, expected)
	if putErr == nil {
		return CredentialRecord{Token: tok, Version: rec.Version}, nil
	}
	// A filesystem CAS may have committed its atomic rename before reporting a
	// directory-sync error. Exact reread is the only safe reconciliation.
	observed, getErr := r.store.Get(ctx, key)
	if getErr == nil && slices.Equal(observed.Value, body) {
		return CredentialRecord{Token: tok, Version: observed.Version}, nil
	}
	return CredentialRecord{}, putErr
}

// Delete removes only the exact loaded credential version.
func (r *CredentialRepository) Delete(ctx context.Context, id CredentialIdentity, expected credentialstore.Version) error {
	key, err := identityKey(id)
	if err != nil {
		return ErrNotEnrolled
	}
	return r.store.Delete(ctx, key, expected)
}

func validToken(t Token) bool {
	return t.AccessToken != "" && t.RefreshToken != "" && t.TokenType == "Bearer" && !t.Expiry.IsZero()
}
func toJSON(t Token) tokenJSON   { return tokenJSON(t) }
func fromJSON(t tokenJSON) Token { return Token(t) }
func canonicalIdentity(id CredentialIdentity) CredentialIdentity {
	id.Scopes = append([]string(nil), id.Scopes...)
	slices.Sort(id.Scopes)
	id.Scopes = slices.Compact(id.Scopes)
	return id
}
func sameIdentity(a, b CredentialIdentity) bool {
	a = canonicalIdentity(a)
	b = canonicalIdentity(b)
	return a.SchemaVersion == b.SchemaVersion && a.EndpointID == b.EndpointID && a.Gateway == b.Gateway && a.Issuer == b.Issuer && a.ClientID == b.ClientID && a.ResourceAudience == b.ResourceAudience && slices.Equal(a.Scopes, b.Scopes) && a.RedirectURI == b.RedirectURI && a.IssuerTrust == b.IssuerTrust && a.GatewayTrust == b.GatewayTrust
}
func identityKey(id CredentialIdentity) ([]byte, error) {
	id = canonicalIdentity(id)
	if id.SchemaVersion != 1 || id.EndpointID == "" || id.Gateway == "" || id.Issuer == "" || id.ClientID == "" || id.ResourceAudience == "" || len(id.Scopes) == 0 || id.RedirectURI == "" || id.IssuerTrust.Policy == "" || id.GatewayTrust.Policy == "" {
		return nil, ErrNotEnrolled
	}
	body, _ := json.Marshal(id)
	sum := sha256.Sum256(append([]byte("mecatl/provider-oidc/record-key/v1\x00"), body...))
	return []byte(hex.EncodeToString(sum[:])), nil
}

// CredentialRecordKey returns the endpoint's versioned opaque store key.
func CredentialRecordKey(id CredentialIdentity) ([]byte, error) { return identityKey(id) }

// ProtectedStoreConfig requires explicit keyring material and has no fallback source.
type ProtectedStoreConfig struct {
	Root         string
	Keyring      Keyring
	ExistingOnly bool
}

// Keyring returns root-bound encryption key material and optionally permits creation.
type Keyring interface {
	Key(context.Context, bool) ([]byte, error)
}

// NewProtectedStore opens only the encrypted, explicitly keyring-backed namespace.
func NewProtectedStore(ctx context.Context, cfg ProtectedStoreConfig) (credentialstore.Store, error) {
	if cfg.Root == "" || cfg.Keyring == nil {
		return nil, ErrNotEnrolled
	}
	key, err := cfg.Keyring.Key(ctx, !cfg.ExistingOnly)
	if err != nil || len(key) != 32 {
		return nil, errors.Join(ErrNotEnrolled, err)
	}
	defer clear(key)
	var store credentialstore.Store
	if cfg.ExistingOnly {
		store, err = credentialstore.OpenExistingEncryptedFile(cfg.Root, CredentialNamespace, key)
	} else {
		store, err = credentialstore.NewEncryptedFile(cfg.Root, CredentialNamespace, key)
	}
	if err != nil {
		return nil, errors.Join(ErrNotEnrolled, err)
	}
	return store, nil
}

// Locker serializes one complete endpoint credential transaction.
type Locker interface {
	With(context.Context, CredentialIdentity, func(context.Context) error) error
}

// TransactionLocker owns hashed endpoint-scoped cross-process flock transactions.
type TransactionLocker struct{ root string }

// NewTransactionLocker validates an explicit owner-only credential root.
func NewTransactionLocker(root string) (*TransactionLocker, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrNotEnrolled
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrNotEnrolled
	}
	return &TransactionLocker{root: root}, nil
}

// With holds the endpoint transaction lock for fn and honors context cancellation.
func (l *TransactionLocker) With(ctx context.Context, id CredentialIdentity, fn func(context.Context) error) error {
	key, err := identityKey(id)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte(transactionLockDomain), key...))
	path := filepath.Join(l.root, ".provider-oidc-"+hex.EncodeToString(sum[:])+".lock")
	fl := flock.New(path, flock.SetPermissions(0600))
	ok, err := fl.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !ok {
		return errors.New("provider OIDC transaction lock not acquired")
	}
	defer func() { _ = fl.Unlock() }()
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	return fn(ctx)
}

type memoryLocker struct{ mu sync.Mutex }

// MemoryLocker returns an in-process test locker.
func MemoryLocker() Locker { return &memoryLocker{} }
func (l *memoryLocker) With(ctx context.Context, _ CredentialIdentity, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn(ctx)
}

// Lifecycle serializes refresh exchange and durable CAS commit.
type Lifecycle struct {
	Repository  *CredentialRepository
	Locker      Locker
	Authorize   func(context.Context) (Token, error)
	Exchange    func(context.Context, string) (Token, error)
	AfterCommit func()
}

// Status is the closed, value-free local credential state vocabulary.
type Status string

const (
	// StatusUsable means the local record is currently usable.
	StatusUsable Status = "usable"
	// StatusNotEnrolled means no exact-identity record exists.
	StatusNotEnrolled Status = "not-enrolled"
	// StatusExpired means the local record has expired.
	StatusExpired Status = "expired"
	// StatusCorrupt means protected local state failed validation.
	StatusCorrupt Status = "corrupt"
	// StatusStorageUnavailable means local protected storage cannot be read.
	StatusStorageUnavailable Status = "storage-unavailable"
	// StatusRejected means the provider rejected the retained credential.
	StatusRejected Status = "rejected"
)

// Status inspects only the local record. It never authorizes, refreshes, or
// performs provider communication.
func (l Lifecycle) Status(ctx context.Context, id CredentialIdentity, now time.Time) Status {
	if l.Repository == nil || l.Locker == nil {
		return StatusStorageUnavailable
	}
	var rec CredentialRecord
	err := l.Locker.With(ctx, id, func(ctx context.Context) error {
		var err error
		rec, err = l.Repository.Load(ctx, id)
		return err
	})
	switch {
	case err == nil && !rec.Token.Expiry.After(now):
		return StatusExpired
	case err == nil:
		return StatusUsable
	case errors.Is(err, credentialstore.ErrCorrupt):
		return StatusCorrupt
	case errors.Is(err, credentialstore.ErrUnavailable), errors.Is(err, credentialstore.ErrClosed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StatusStorageUnavailable
	default:
		return StatusNotEnrolled
	}
}

// Logout makes exact-version local deletion authoritative before making one
// bounded best-effort revocation call with the retained in-memory material.
func (l Lifecycle) Logout(ctx context.Context, id CredentialIdentity, revoke func(context.Context, Token) error) error {
	if l.Repository == nil || l.Locker == nil {
		return ErrNotEnrolled
	}
	var retained Token
	err := l.Locker.With(ctx, id, func(ctx context.Context) error {
		rec, err := l.Repository.Load(ctx, id)
		if errors.Is(err, credentialstore.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := l.Repository.Delete(ctx, id, rec.Version); err != nil {
			return err
		}
		retained = rec.Token
		return nil
	})
	if err != nil || retained.RefreshToken == "" || revoke == nil {
		return err
	}
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = revoke(revokeCtx, retained)
	return nil
}

// Enroll serializes authorization-code exchange and first credential commit.
func (l Lifecycle) Enroll(ctx context.Context, id CredentialIdentity) error {
	if l.Repository == nil || l.Locker == nil || l.Authorize == nil {
		return ErrNotEnrolled
	}
	return l.Locker.With(ctx, id, func(ctx context.Context) error {
		tok, err := l.Authorize(ctx)
		if err != nil {
			return err
		}
		var expected *credentialstore.Version
		current, err := l.Repository.Load(ctx, id)
		switch {
		case err == nil:
			expected = &current.Version
		case errors.Is(err, credentialstore.ErrNotFound):
		default:
			return err
		}
		_, err = l.Repository.Save(ctx, id, tok, expected)
		return err
	})
}

// Refresh exchanges the exact loaded refresh token and commits rotation before return.
func (l Lifecycle) Refresh(ctx context.Context, id CredentialIdentity) (Token, error) {
	return l.refresh(ctx, id, "")
}

// RefreshRejected refreshes rejected unless another synchronized caller already
// replaced it, in which case the newer durable token is reused.
func (l Lifecycle) RefreshRejected(ctx context.Context, id CredentialIdentity, rejected string) (Token, error) {
	return l.refresh(ctx, id, rejected)
}

func (l Lifecycle) refresh(ctx context.Context, id CredentialIdentity, rejected string) (Token, error) {
	var out Token
	if l.Repository == nil || l.Locker == nil || l.Exchange == nil {
		return out, ErrNotEnrolled
	}
	err := l.Locker.With(ctx, id, func(ctx context.Context) error {
		rec, err := l.Repository.Load(ctx, id)
		if err != nil {
			return err
		}
		if rejected != "" && rec.Token.AccessToken != rejected {
			out = rec.Token
			return nil
		}
		next, err := l.Exchange(ctx, rec.Token.RefreshToken)
		if err != nil {
			var retrieve *oauth2.RetrieveError
			if errors.As(err, &retrieve) && retrieve.ErrorCode == "invalid_grant" {
				if delErr := l.Repository.Delete(ctx, id, rec.Version); delErr != nil && !errors.Is(delErr, credentialstore.ErrNotFound) {
					return delErr
				}
				return ErrNotEnrolled
			}
			return err
		}
		if next.RefreshToken == "" {
			next.RefreshToken = rec.Token.RefreshToken
		}
		saved, err := l.Repository.Save(ctx, id, next, &rec.Version)
		if err != nil {
			return fmt.Errorf("persist refreshed credential: %w", err)
		}
		out = saved.Token
		if l.AfterCommit != nil {
			l.AfterCommit()
		}
		return nil
	})
	return out, err
}
