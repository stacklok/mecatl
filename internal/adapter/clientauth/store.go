// Package clientauth provides durable, target-bound public-client OIDC credentials.
package clientauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// #nosec G101 -- this is a namespace identifier, not a credential.
const credentialNamespace = "mecatl/clientauth/oidc/v1"
const keyringService = "mecatl.mecatui.clientauth"
const legacyKeyringAccount = "encrypted-store-key/v1"
const keyringAccountDomain = "mecatl/clientauth/keyring-account/v1\x00"
const keyringLockName = ".clientauth-store-key.lock"
const keyringLockRetry = 10 * time.Millisecond
const targetLockRetry = 10 * time.Millisecond
const targetLockDomain = "mecatl/clientauth/target-lock/v1\x00"
const httpsScheme = "https"

var (
	// ErrInvalidIdentity indicates invalid target identity data.
	ErrInvalidIdentity = errors.New("clientauth: invalid target identity")
	// ErrKeyUnavailable indicates that the credential encryption key is unavailable.
	ErrKeyUnavailable = errors.New("clientauth: encryption key unavailable")
	// ErrCorrupt indicates malformed or invalid stored credentials.
	ErrCorrupt = errors.New("clientauth: corrupt credential")
)

// Identity binds a credential to one canonical remote target and complete public-client configuration.
type Identity struct {
	Target, Issuer, ClientID, Audience, RedirectURI string
	Scopes                                          []string
}

// Canonical validates and normalizes the identity.
func (i Identity) Canonical() (Identity, error) {
	var err error
	for _, p := range []struct {
		name  string
		value *string
	}{{"target", &i.Target}} {
		*p.value, err = canonicalTarget(*p.value)
		if err != nil {
			return Identity{}, fmt.Errorf("%w: %s", ErrInvalidIdentity, p.name)
		}
	}
	i.Issuer, err = canonicalIssuerURL(i.Issuer)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: issuer", ErrInvalidIdentity)
	}
	i.RedirectURI, err = canonicalRedirectURI(i.RedirectURI)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: redirect URI", ErrInvalidIdentity)
	}
	if !safe(i.ClientID) || !safe(i.Audience) || len(i.Scopes) == 0 {
		return Identity{}, ErrInvalidIdentity
	}
	i.Scopes = slices.Clone(i.Scopes)
	for n, scope := range i.Scopes {
		if !safe(scope) {
			return Identity{}, fmt.Errorf("%w: scope %d", ErrInvalidIdentity, n)
		}
	}
	slices.Sort(i.Scopes)
	i.Scopes = slices.Compact(i.Scopes)
	return i, nil
}
func safe(v string) bool { return v != "" && len(v) <= 1024 && !strings.ContainsAny(v, "\x00\r\n") }
func canonicalTarget(raw string) (string, error) {
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#@") {
		return "", errors.New("invalid target")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host == "" || port == "" {
		return "", errors.New("target must be host:port")
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 {
		return "", errors.New("invalid target port")
	}
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, strconv.FormatUint(p, 10)), nil
}
func canonicalIssuerURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != httpsScheme || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("invalid issuer URL")
	}
	u.Scheme, u.Host, u.User, u.Fragment = httpsScheme, strings.ToLower(u.Host), nil, ""
	u.RawPath = ""
	return u.String(), nil
}

func canonicalRedirectURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != httpsScheme && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("invalid redirect URI")
	}
	if u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && u.Hostname() != "localhost" {
		return "", errors.New("non-loopback HTTP redirect URI")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""
	return u.String(), nil
}

func (i Identity) recordKey() ([]byte, error) {
	c, err := i.Canonical()
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(append([]byte("mecatl/clientauth/identity/v1\x00"), b...))
	return sum[:], nil
}

// KeyProvider supplies exactly 32 bytes of encryption material. It must never persist keys beside credentials.
type KeyProvider interface {
	StoreKey(context.Context) ([]byte, error)
}

// ExistingKeyProvider supplies an already-created store key without creating one.
// Destructive and read-only operations use this so an idempotent logout cannot
// create new keyring state.
type ExistingKeyProvider interface {
	ExistingStoreKey(context.Context) ([]byte, error)
}

// keyringBackend is the narrow OS-keyring seam used by deterministic tests.
type keyringBackend interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
}

type osKeyringBackend struct{}

func (osKeyringBackend) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (osKeyringBackend) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

// KeyringProvider stores one root-scoped encryption key in the OS credential manager.
type KeyringProvider struct {
	root    string
	account string
	lock    *flock.Flock
	backend keyringBackend
}

// NewKeyringProvider binds an OS-keyring provider to one absolute, clean store root.
func NewKeyringProvider(root string) (*KeyringProvider, error) {
	return newKeyringProvider(root, osKeyringBackend{})
}

func newKeyringProvider(root string, backend keyringBackend) (*KeyringProvider, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("clientauth: store root must be absolute and clean")
	}
	if backend == nil {
		return nil, errors.New("clientauth: keyring backend is required")
	}
	// #nosec G703 -- root was validated as an absolute, clean trusted adapter boundary above.
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("clientauth: create store root: %w", err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("clientauth: canonicalize store root: %w", err)
	}
	root = filepath.Clean(root)
	// #nosec G302 -- the key lock and credential store require an owner-only root.
	if err := os.Chmod(root, 0700); err != nil {
		return nil, fmt.Errorf("clientauth: protect store root: %w", err)
	}
	sum := sha256.Sum256(append([]byte(keyringAccountDomain), []byte(root)...))
	account := legacyKeyringAccount + "/root-" + hex.EncodeToString(sum[:])
	return &KeyringProvider{
		root: root, account: account, backend: backend,
		lock: flock.New(filepath.Join(root, keyringLockName), flock.SetPermissions(0600)),
	}, nil
}

func (p *KeyringProvider) storeRoot() string {
	if p == nil {
		return ""
	}
	return p.root
}

// ExistingStoreKey returns or migrates an existing key without generating one.
func (p *KeyringProvider) ExistingStoreKey(ctx context.Context) ([]byte, error) {
	return p.withLock(ctx, false)
}

// StoreKey returns the key used to protect the credential store, creating it if absent.
func (p *KeyringProvider) StoreKey(ctx context.Context) ([]byte, error) {
	return p.withLock(ctx, true)
}

func (p *KeyringProvider) withLock(ctx context.Context, create bool) ([]byte, error) {
	if p == nil || p.lock == nil || p.backend == nil {
		return nil, ErrKeyUnavailable
	}
	locked, err := p.lock.TryLockContext(ctx, keyringLockRetry)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: acquiring root key lock: %w", ErrKeyUnavailable, ctxErr)
		}
		return nil, fmt.Errorf("%w: acquiring root key lock: %w", ErrKeyUnavailable, err)
	}
	if !locked {
		return nil, fmt.Errorf("%w: root key lock was not acquired", ErrKeyUnavailable)
	}
	defer func() { _ = p.lock.Unlock() }()
	// SetPermissions applies on creation; chmod also repairs a pre-existing lock
	// from an older client before any keyring state is inspected.
	if err := os.Chmod(filepath.Join(p.root, keyringLockName), 0600); err != nil {
		return nil, fmt.Errorf("%w: protect root key lock: %w", ErrKeyUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}

	rootKey, found, err := p.readKey(p.account)
	if err != nil {
		// A present but malformed root key is authoritative and must fail closed.
		return nil, err
	}
	if found {
		return rootKey, nil
	}

	if legacyCredentialExists(p.root) {
		legacyKey, legacyFound, legacyErr := p.readKey(legacyKeyringAccount)
		if legacyErr == nil && legacyFound {
			if err := p.writeKey(p.account, legacyKey); err != nil {
				clear(legacyKey)
				return nil, err
			}
			return legacyKey, nil
		}
		if legacyErr != nil {
			return nil, legacyErr
		}
	}
	if !create {
		return nil, ErrKeyUnavailable
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("%w: generating a key failed: %w", ErrKeyUnavailable, err)
	}
	if err := p.writeKey(p.account, key); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

func legacyCredentialExists(root string) bool {
	// Keep this protocol calculation byte-identical to credentialstore's stable
	// namespacePhysicalName. An empty namespace can be left by merely opening a
	// store and is not evidence that this root used clientauth's legacy global key.
	framed := []byte("mecatl/credentialstore/namespace/v1")
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(credentialNamespace)))
	framed = append(framed, length[:]...)
	framed = append(framed, credentialNamespace...)
	digest := sha256.Sum256(framed)
	entries, err := os.ReadDir(filepath.Join(root, "ns-v1-"+hex.EncodeToString(digest[:])))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, "rec-v1-") && strings.HasSuffix(name, ".cred") {
			return true
		}
	}
	return false
}

var errCorruptKeyringValue = errors.New("corrupt keyring value")

func (p *KeyringProvider) readKey(account string) ([]byte, bool, error) {
	value, err := p.backend.Get(keyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: reading the OS keyring was refused: %w", ErrKeyUnavailable, err)
	}
	key, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, true, fmt.Errorf("%w: %w: stored key is corrupt or the wrong length", ErrKeyUnavailable, errCorruptKeyringValue)
	}
	return key, true, nil
}

func (p *KeyringProvider) writeKey(account string, key []byte) error {
	if err := p.backend.Set(keyringService, account, base64.RawStdEncoding.EncodeToString(key)); err != nil {
		return fmt.Errorf("%w: writing to the OS keyring was refused: %w", ErrKeyUnavailable, err)
	}
	return nil
}

// OpenStore opens this adapter's isolated encrypted credential namespace.
func OpenStore(ctx context.Context, root string, keys KeyProvider) (*credentialstore.EncryptedFileStore, error) {
	root, err := validateStoreRoot(root, keys)
	if err != nil {
		return nil, err
	}
	key, err := keys.StoreKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	if len(key) != 32 {
		return nil, ErrKeyUnavailable
	}
	defer clear(key)
	return credentialstore.NewEncryptedFile(root, credentialNamespace, key)
}

// OpenExistingStore opens the encrypted namespace without generating keyring state.
func OpenExistingStore(ctx context.Context, root string, keys ExistingKeyProvider) (*credentialstore.EncryptedFileStore, error) {
	root, err := validateStoreRoot(root, keys)
	if err != nil {
		return nil, err
	}
	key, err := keys.ExistingStoreKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	if len(key) != 32 {
		return nil, ErrKeyUnavailable
	}
	defer clear(key)
	return credentialstore.NewEncryptedFile(root, credentialNamespace, key)
}

type rootedKeyProvider interface{ storeRoot() string }

func validateStoreRoot(root string, keys any) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("clientauth: store root must be absolute and clean")
	}
	if keys == nil {
		return "", ErrKeyUnavailable
	}
	canonical := root
	if physical, err := filepath.EvalSymlinks(root); err == nil {
		canonical = filepath.Clean(physical)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("clientauth: canonicalize store root: %w", err)
	}
	if rooted, ok := keys.(rootedKeyProvider); ok && rooted.storeRoot() != canonical {
		return "", errors.New("clientauth: key provider root does not match store root")
	}
	return canonical, nil
}

// Token is the secret OAuth material, deliberately absent from Registry records.
type Token struct {
	AccessToken, RefreshToken, TokenType string
	Expiry                               string
}
type credentialEnvelope struct {
	Schema   string   `json:"schema"`
	Version  int      `json:"version"`
	Identity Identity `json:"identity"`
	Token    Token    `json:"token"`
}

const envelopeSchema = "mecatl.clientauth.oauth"

// CredentialRecord carries an opaque CAS version.
type CredentialRecord struct {
	Token   Token
	Version credentialstore.Version
}

// Credentials is a target-bound, CAS-safe credential repository.
type Credentials struct {
	store     credentialstore.Store
	fileStore *credentialstore.EncryptedFileStore
}

// NewCredentials creates a credential repository backed by store.
func NewCredentials(store credentialstore.Store) (*Credentials, error) {
	if store == nil {
		return nil, errors.New("clientauth: credential store is required")
	}
	fileStore, _ := store.(*credentialstore.EncryptedFileStore)
	return &Credentials{store: store, fileStore: fileStore}, nil
}

// Load retrieves and validates credentials for identity.
func (c *Credentials) Load(ctx context.Context, identity Identity) (CredentialRecord, error) {
	key, err := identity.recordKey()
	if err != nil {
		return CredentialRecord{}, err
	}
	rec, err := c.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, credentialstore.ErrCorrupt) {
			return CredentialRecord{}, ErrCorrupt
		}
		return CredentialRecord{}, err
	}
	var env credentialEnvelope
	if json.Unmarshal(rec.Value, &env) != nil || env.Schema != envelopeSchema || env.Version != 1 {
		return CredentialRecord{}, ErrCorrupt
	}
	canonical, err := identity.Canonical()
	if err != nil || !sameIdentity(env.Identity, canonical) || !validToken(env.Token) {
		return CredentialRecord{}, ErrCorrupt
	}
	return CredentialRecord{env.Token, rec.Version}, nil
}

// Save stores credentials for identity using optional CAS version expected.
func (c *Credentials) Save(ctx context.Context, identity Identity, token Token, expected *credentialstore.Version) (CredentialRecord, error) {
	canonical, err := identity.Canonical()
	if err != nil {
		return CredentialRecord{}, err
	}
	if !validToken(token) {
		return CredentialRecord{}, ErrCorrupt
	}
	key, _ := canonical.recordKey()
	body, _ := json.Marshal(credentialEnvelope{envelopeSchema, 1, canonical, token})
	rec, err := c.store.Put(ctx, key, body, expected)
	if err != nil {
		return CredentialRecord{}, err
	}
	return CredentialRecord{token, rec.Version}, nil
}

// replaceUnusable conditionally repairs a credential that is still corrupt. A
// semantically malformed plaintext record uses ordinary CAS; an unreadable
// encrypted envelope can be repaired only by the encrypted-file backend that
// owns the record lock.
func (c *Credentials) replaceUnusable(ctx context.Context, identity Identity, token Token) (CredentialRecord, error) {
	canonical, err := identity.Canonical()
	if err != nil {
		return CredentialRecord{}, err
	}
	if !validToken(token) {
		return CredentialRecord{}, ErrCorrupt
	}
	key, _ := canonical.recordKey()
	body, _ := json.Marshal(credentialEnvelope{envelopeSchema, 1, canonical, token})
	raw, err := c.store.Get(ctx, key)
	var written credentialstore.Record
	switch {
	case err == nil:
		var env credentialEnvelope
		if json.Unmarshal(raw.Value, &env) == nil && env.Schema == envelopeSchema && env.Version == 1 && sameIdentity(env.Identity, canonical) && validToken(env.Token) {
			return CredentialRecord{}, credentialstore.ErrConflict
		}
		written, err = c.store.Put(ctx, key, body, &raw.Version)
	case errors.Is(err, credentialstore.ErrCorrupt):
		if c.fileStore == nil {
			return CredentialRecord{}, errors.New("clientauth: credential store cannot safely replace a corrupt record")
		}
		written, err = c.fileStore.ReplaceCorrupt(ctx, key, body)
	default:
		return CredentialRecord{}, err
	}
	if err != nil {
		return CredentialRecord{}, err
	}
	return CredentialRecord{Token: token, Version: written.Version}, nil
}

// IsNotEnrolled reports whether err means nothing is stored for the target yet,
// as opposed to a registry or credential store that cannot be read. Callers must
// separate the two: the first is answered by running a login, the second is a
// local fault, and collapsing them turns an unenrolled target into what looks
// like a broken installation.
func IsNotEnrolled(err error) bool {
	return errors.Is(err, credentialstore.ErrNotFound)
}

// Upsert stores token for identity, replacing any credential already held for
// it. Enrolment is repeatable BY DESIGN: a login must be able to replace an
// expired, revoked, or rotated credential, so it cannot use Save's create-only
// precondition. Save's CAS is for the refresh path, where losing the race means
// another process rotated the token first and this one's write is stale.
//
// Upsert deliberately remains ordinary load-then-CAS convenience. Target-locked
// reauthentication of a corrupt current record goes through Enroll, which can
// prove registry reachability and use the store's conditional recovery seam.
func (c *Credentials) Upsert(ctx context.Context, identity Identity, token Token) (CredentialRecord, error) {
	if rec, err := c.Load(ctx, identity); err == nil {
		return c.Save(ctx, identity, token, &rec.Version)
	}
	return c.Save(ctx, identity, token, nil)
}

// Delete removes credentials for identity using expected CAS version.
func (c *Credentials) Delete(ctx context.Context, identity Identity, expected credentialstore.Version) error {
	key, err := identity.recordKey()
	if err != nil {
		return err
	}
	return c.store.Delete(ctx, key, expected)
}

// tokenValueLimit bounds one OAuth token value. It is deliberately far above the
// 1024 that safe() applies to identity fields: a JWT access token carrying group
// or role claims routinely passes a kilobyte (Entra with many group memberships
// is the usual case), and safe() rejecting those made the credential unstorable
// while the intended 16384 bound sat next to it as dead code.
const tokenValueLimit = 16384

// safeTokenValue permits an empty value — a refresh token is absent whenever the
// provider was not asked for, or declined, offline_access — but never a control
// character. safe() cannot be reused: it requires non-empty and caps at 1024.
func safeTokenValue(v string) bool {
	return len(v) <= tokenValueLimit && !strings.ContainsAny(v, "\x00\r\n")
}

func validToken(t Token) bool {
	return t.AccessToken != "" && safeTokenValue(t.AccessToken) &&
		safe(t.TokenType) &&
		safeTokenValue(t.RefreshToken) &&
		len(t.Expiry) <= 128 && !strings.ContainsAny(t.Expiry, "\x00\r\n")
}

// Equal reports whether two identities denote the same enrolment. Identity holds a
// slice, so it is not comparable with ==; callers outside this package need this.
func (i Identity) Equal(other Identity) bool { return sameIdentity(i, other) }

func sameIdentity(a, b Identity) bool {
	return a.Target == b.Target && a.Issuer == b.Issuer && a.ClientID == b.ClientID && a.Audience == b.Audience && a.RedirectURI == b.RedirectURI && slices.Equal(a.Scopes, b.Scopes)
}

// Registry is an owner-only, atomic, non-secret connection metadata registry.
type Registry struct {
	path              string
	root              string
	mu                sync.Mutex
	lock              *flock.Flock
	writeFault        func(string) error
	targetLockAttempt func()
}

// Connection is non-secret saved connection metadata. IssuerCAFile is used only
// for OIDC discovery, JWKS, refresh, and revocation; it is not server transport
// trust. UnmarshalJSON accepts the legacy tls_ca_file name for registry compatibility.
type Connection struct {
	Identity     Identity `json:"identity"`
	IssuerCAFile string   `json:"issuer_ca_file,omitempty"`
}

// UnmarshalJSON reads the former tls_ca_file field as issuer trust. When both are
// present, the explicit issuer_ca_file value wins, including an explicit empty value.
func (c *Connection) UnmarshalJSON(data []byte) error {
	var wire struct {
		Identity     Identity `json:"identity"`
		IssuerCAFile *string  `json:"issuer_ca_file"`
		LegacyCAFile string   `json:"tls_ca_file"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.Identity = wire.Identity
	if wire.IssuerCAFile != nil {
		c.IssuerCAFile = *wire.IssuerCAFile
	} else {
		c.IssuerCAFile = wire.LegacyCAFile
	}
	return nil
}

type registryFile struct {
	Version     int          `json:"version"`
	Connections []Connection `json:"connections"`
}

// OpenRegistry opens the connection metadata registry rooted at root.
func OpenRegistry(root string) (*Registry, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("clientauth: registry root must be absolute and clean")
	}
	// #nosec G703 -- root was validated as an absolute, clean trusted adapter boundary above.
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("clientauth: canonicalize registry root: %w", err)
	}
	root = filepath.Clean(root)
	// The registry directory contains connection metadata and is intentionally
	// owner-only; 0700 is stricter than the file-mode default required by gosec.
	// #nosec G302 -- preserving the deliberate owner-only directory permission.
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "clientauth-connections.json")
	return &Registry{path: path, root: root, lock: flock.New(path+".lock", flock.SetPermissions(0600))}, nil
}

// List returns all saved connections.
func (r *Registry) List() ([]Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.RLock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()
	return r.list()
}

func (r *Registry) list() ([]Connection, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if json.Unmarshal(data, &f) != nil || f.Version != 1 {
		return nil, ErrCorrupt
	}
	// A single entry that fails canonicalization or CA-path validation is
	// unusable for its OWN target only -- e.g. a pre-tightening record saved with
	// a relative issuer_ca_file before validIssuerCAFile required an absolute
	// one. It must not fail every OTHER target's List/FindTarget/Enroll: that
	// previously made one legacy or damaged row brick every saved login, with no
	// repair path, because the very machinery that replaces a target's entry
	// (Upsert) can only run after list() has already succeeded. Drop the bad
	// entry instead; a saved-target listing simply omits it, and its target's
	// next login (Upsert) replaces it outright.
	valid := make([]Connection, 0, len(f.Connections))
	for _, conn := range f.Connections {
		c, err := conn.Identity.Canonical()
		if err != nil || !validIssuerCAFile(conn.IssuerCAFile) {
			continue
		}
		conn.Identity = c
		valid = append(valid, conn)
	}
	return valid, nil
}

// FindTarget returns the saved connection for target.
func (r *Registry) FindTarget(target string) (Connection, error) {
	id, err := (Identity{Target: target, Issuer: "https://invalid.example", ClientID: "x", Audience: "x", RedirectURI: "http://127.0.0.1/", Scopes: []string{"x"}}).Canonical()
	if err != nil {
		return Connection{}, err
	}
	all, err := r.List()
	if err != nil {
		return Connection{}, err
	}
	var found []Connection
	for _, conn := range all {
		if conn.Identity.Target == id.Target {
			found = append(found, conn)
		}
	}
	if len(found) == 0 {
		return Connection{}, credentialstore.ErrNotFound
	}
	if len(found) != 1 {
		return Connection{}, ErrCorrupt
	}
	return found[0], nil
}

func validIssuerCAFile(path string) bool {
	return path == "" || filepath.IsAbs(path) && filepath.Clean(path) == path
}

// Upsert stores conn as the single enrolment for its target and returns the
// identities it superseded. Credentials are keyed by identity, not target, so a
// caller holding the credential store must discard those: otherwise a superseded
// refresh token stays on disk indefinitely, unreachable and unrevoked.
func (r *Registry) Upsert(conn Connection) ([]Identity, error) {
	id, err := conn.Identity.Canonical()
	if err != nil {
		return nil, err
	}
	if !validIssuerCAFile(conn.IssuerCAFile) {
		return nil, errors.New("clientauth: issuer CA path must be absolute and clean")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()
	all, err := r.list()
	if err != nil {
		return nil, err
	}
	// Key on TARGET, not the full identity, and COLLAPSE: drop every entry for this
	// target and write exactly one. FindTarget looks up by target alone and refuses
	// ambiguity with ErrCorrupt, so identity-keyed upsert let a re-enrolment against
	// a different issuer add a second entry and brick every later lookup, with no
	// logout or prune to recover. Replacing matches in place is not enough either --
	// a registry that already holds duplicates would keep them, just with identical
	// values. Collapsing repairs such a registry on the next login.
	kept := make([]Connection, 0, len(all)+1)
	var displaced []Identity
	for _, existing := range all {
		if existing.Identity.Target != id.Target {
			kept = append(kept, existing)
			continue
		}
		if !existing.Identity.Equal(id) {
			displaced = append(displaced, existing.Identity)
		}
	}
	all = append(kept, Connection{Identity: id, IssuerCAFile: conn.IssuerCAFile})
	if err := r.write(all); err != nil {
		return nil, err
	}
	return displaced, nil
}

// DeleteTarget removes every registry entry for target when those entries still
// equal expected. The expected snapshot is the registry-side CAS: a concurrent
// re-enrolment is retained rather than being removed by an older logout.
func (r *Registry) DeleteTarget(target string, expected []Connection) (int, error) {
	canonical, err := canonicalTarget(target)
	if err != nil {
		return 0, fmt.Errorf("%w: target", ErrInvalidIdentity)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.Lock(); err != nil {
		return 0, err
	}
	defer func() { _ = r.lock.Unlock() }()
	all, err := r.list()
	if err != nil {
		return 0, err
	}
	current := make([]Connection, 0, len(expected))
	kept := make([]Connection, 0, len(all))
	for _, conn := range all {
		if conn.Identity.Target == canonical {
			current = append(current, conn)
		} else {
			kept = append(kept, conn)
		}
	}
	if !sameConnections(current, expected) {
		return 0, credentialstore.ErrConflict
	}
	if len(current) == 0 {
		return 0, nil
	}
	if err := r.write(kept); err != nil {
		return 0, err
	}
	return len(current), nil
}

func sameConnections(a, b []Connection) bool {
	if len(a) != len(b) {
		return false
	}
	b = b[:len(a)]
	for i, conn := range a {
		if conn.IssuerCAFile != b[i].IssuerCAFile || !conn.Identity.Equal(b[i].Identity) {
			return false
		}
	}
	return true
}

func (r *Registry) targetSnapshot(target string) ([]Connection, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	var entries []Connection
	for _, conn := range all {
		if conn.Identity.Target == target {
			entries = append(entries, conn)
		}
	}
	return entries, nil
}

// replaceTarget conditionally replaces one target's entries while preserving
// unrelated targets changed by other processes.
func (r *Registry) replaceTarget(target string, expected, desired []Connection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = r.lock.Unlock() }()
	all, err := r.list()
	if err != nil {
		return err
	}
	current := make([]Connection, 0, len(expected))
	kept := make([]Connection, 0, len(all)+len(desired))
	for _, conn := range all {
		if conn.Identity.Target == target {
			current = append(current, conn)
		} else {
			kept = append(kept, conn)
		}
	}
	if !sameConnections(current, expected) {
		return credentialstore.ErrConflict
	}
	return r.write(append(kept, desired...))
}

func (r *Registry) lockTarget(ctx context.Context, target string) (func(), error) {
	if r.targetLockAttempt != nil {
		r.targetLockAttempt()
	}
	sum := sha256.Sum256(append([]byte(targetLockDomain+r.root+"\x00"), []byte(target)...))
	path := filepath.Join(r.root, ".clientauth-target-"+hex.EncodeToString(sum[:])+".lock")
	lock := flock.New(path, flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, targetLockRetry)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("clientauth: acquire target transaction: %w", err)
	}
	if !locked {
		return nil, errors.New("clientauth: target transaction lock was not acquired")
	}
	// #nosec G703 -- path is derived from the canonical registry root and a fixed SHA-256 digest.
	if err := os.Chmod(path, 0600); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("clientauth: protect target transaction lock: %w", err)
	}
	return func() { _ = lock.Unlock() }, nil
}

func (r *Registry) write(all []Connection) error {
	data, err := json.Marshal(registryFile{1, all})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.root, ".clientauth-connections-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if r.writeFault != nil {
		if err := r.writeFault("precommit"); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return err
	}
	committed = true
	dir, err := os.Open(r.root)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if r.writeFault != nil {
		if err := r.writeFault("postrename"); err != nil {
			return err
		}
	}
	return nil
}
