// Package clientauth provides durable, target-bound public-client OIDC credentials.
package clientauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	"unicode"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/resourceurl"
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
func safe(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
func canonicalTarget(raw string) (string, error) {
	if !safe(raw) || strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#@") {
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

var canonicalResourceURL = resourceurl.Canonical

func canonicalIssuerURL(raw string) (string, error) {
	if !safe(raw) {
		return "", errors.New("invalid issuer URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != httpsScheme || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("invalid issuer URL")
	}
	u.Scheme, u.Host, u.User, u.Fragment = httpsScheme, strings.ToLower(u.Host), nil, ""
	u.RawPath = ""
	return u.String(), nil
}

func canonicalRedirectURI(raw string) (string, error) {
	if !safe(raw) {
		return "", errors.New("invalid redirect URI")
	}
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
	// existingOnly is used by discovery/connect paths. It forbids all local
	// initialization, including lock-file creation and legacy-key migration.
	existingOnly bool
}

// NewKeyringProvider binds an OS-keyring provider to one absolute, clean store root.
func NewKeyringProvider(root string) (*KeyringProvider, error) {
	return newKeyringProvider(root, osKeyringBackend{})
}

// NewExistingKeyringProvider binds to keyring state that is already present.
// It never creates the store root or its lock, and never migrates legacy state.
func NewExistingKeyringProvider(root string) (*KeyringProvider, error) {
	return newExistingKeyringProvider(root, osKeyringBackend{})
}

func newExistingKeyringProvider(root string, backend keyringBackend) (*KeyringProvider, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || backend == nil {
		return nil, errors.New("clientauth: existing store root is invalid")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("clientauth: existing store root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("clientauth: existing store root is not a directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("clientauth: canonicalize store root: %w", err)
	}
	canonical = filepath.Clean(canonical)
	return &KeyringProvider{root: canonical, account: keyringAccountFor(canonical), backend: backend, existingOnly: true}, nil
}

// keyringAccountFor derives the root-scoped keyring account name. Two
// KeyringProviders bound to the same canonicalized root must always agree on
// this name, so both the creating and the existing-only constructor call
// through this one helper rather than each deriving it independently.
func keyringAccountFor(canonicalRoot string) string {
	sum := sha256.Sum256(append([]byte(keyringAccountDomain), []byte(canonicalRoot)...))
	return legacyKeyringAccount + "/root-" + hex.EncodeToString(sum[:])
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
	account := keyringAccountFor(root)
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
	if p != nil && p.existingOnly {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
		}
		if key, found, err := p.readKey(p.account); err != nil {
			return nil, err
		} else if found {
			return key, nil
		}
		// Legacy state is readable, but discovery must not migrate it by writing
		// a new root-scoped key.
		if key, found, err := p.readKey(legacyKeyringAccount); err != nil {
			return nil, err
		} else if found && legacyCredentialExists(p.root) {
			return key, nil
		}
		return nil, ErrKeyUnavailable
	}
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
	// An empty namespace can be left by merely opening a store and is not
	// evidence that this root used clientauth's legacy global key.
	entries, err := os.ReadDir(filepath.Join(root, credentialstore.NamespacePhysicalName(credentialNamespace)))
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
	store, err := credentialstore.OpenExistingEncryptedFile(root, credentialNamespace, key)
	if err != nil {
		return nil, fmt.Errorf("%w: existing credential store: %w", ErrKeyUnavailable, err)
	}
	return store, nil
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
	existingOnly      bool
	writeFault        func(string) error
	targetLockAttempt func()
}

// IssuerAddressPolicy controls which network addresses the issuer transport may dial.
type IssuerAddressPolicy string

// The closed issuer address-policy vocabulary. A saved row carrying anything
// else is quarantined rather than defaulted (see Connection.UnmarshalJSON).
const (
	IssuerAddressPolicyPrivate IssuerAddressPolicy = "private"
	IssuerAddressPolicyPublic  IssuerAddressPolicy = "public"
)

func (p IssuerAddressPolicy) valid() bool {
	return p == IssuerAddressPolicyPrivate || p == IssuerAddressPolicyPublic
}

// Connection is non-secret saved connection metadata. IssuerCAFile is used only
// for OIDC discovery, JWKS, refresh, and revocation; it is not server transport
// trust. UnmarshalJSON accepts the legacy tls_ca_file name for registry compatibility.
type Connection struct {
	Identity Identity `json:"identity"`
	// ResourceURL is the optional canonical HTTPS protected-resource identity.
	// It deliberately remains outside Identity so existing credential record keys
	// stay stable across this additive registry metadata change.
	ResourceURL         string              `json:"resource_url,omitempty"`
	IssuerCAFile        string              `json:"issuer_ca_file,omitempty"`
	IssuerAddressPolicy IssuerAddressPolicy `json:"issuer_address_policy,omitempty"`
}

// UnmarshalJSON reads the former tls_ca_file field as issuer trust. When both are
// present, the explicit issuer_ca_file value wins, including an explicit empty value.
func (c *Connection) UnmarshalJSON(data []byte) error {
	var wire struct {
		Identity     Identity        `json:"identity"`
		ResourceURL  string          `json:"resource_url"`
		IssuerCAFile *string         `json:"issuer_ca_file"`
		LegacyCAFile string          `json:"tls_ca_file"`
		Policy       json.RawMessage `json:"issuer_address_policy"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.Identity = wire.Identity
	c.ResourceURL = wire.ResourceURL
	if wire.IssuerCAFile != nil {
		c.IssuerCAFile = *wire.IssuerCAFile
	} else {
		c.IssuerCAFile = wire.LegacyCAFile
	}
	if len(wire.Policy) == 0 {
		c.IssuerAddressPolicy = IssuerAddressPolicyPrivate // legacy registry rows
	} else if json.Unmarshal(wire.Policy, &c.IssuerAddressPolicy) != nil || !c.IssuerAddressPolicy.valid() {
		return errors.New("invalid issuer address policy")
	}
	return nil
}

type registryRawFile struct {
	Version     int               `json:"version"`
	Connections []json.RawMessage `json:"connections"`
}

type registryRow struct {
	connection Connection
	raw        json.RawMessage
	valid      bool
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

// OpenExistingRegistry opens registry metadata without creating the root or
// registry file. The returned Registry's lock is real but inert (flock.New
// performs no I/O until Lock/TryLock is called), so a subsequent write through
// Enroll's existing-target transaction can still serialize correctly; a write
// against a root that was never created still fails cleanly (os.CreateTemp on
// a missing directory, or the lock file's own missing parent) rather than
// silently materializing new state.
func OpenExistingRegistry(root string) (*Registry, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("clientauth: registry root must be absolute and clean")
	}
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		path := filepath.Join(root, "clientauth-connections.json")
		return &Registry{path: path, root: filepath.Clean(root), existingOnly: true, lock: flock.New(path+".lock", flock.SetPermissions(0600))}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("clientauth: registry root is not a directory")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("clientauth: canonicalize registry root: %w", err)
	}
	root = filepath.Clean(root)
	path := filepath.Join(root, "clientauth-connections.json")
	return &Registry{path: path, root: root, existingOnly: true, lock: flock.New(path+".lock", flock.SetPermissions(0600))}, nil
}

// List returns all saved connections.
func (r *Registry) List() ([]Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.existingOnly {
		return r.list()
	}
	if err := r.lock.RLock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()
	return r.list()
}

func (r *Registry) list() ([]Connection, error) {
	rows, err := r.readRows()
	if err != nil {
		return nil, err
	}
	valid := make([]Connection, 0, len(rows))
	for _, row := range rows {
		if row.valid {
			valid = append(valid, row.connection)
		}
	}
	return valid, nil
}

func (r *Registry) readRows() ([]registryRow, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryRawFile
	if json.Unmarshal(data, &f) != nil || f.Version != 1 {
		return nil, ErrCorrupt
	}
	rows := make([]registryRow, 0, len(f.Connections))
	for _, raw := range f.Connections {
		var fields map[string]json.RawMessage
		var conn Connection
		valid := json.Unmarshal(raw, &fields) == nil && fields != nil && json.Unmarshal(raw, &conn) == nil
		if valid {
			for key := range fields {
				if key != "identity" && key != "resource_url" && key != "issuer_ca_file" && key != "tls_ca_file" && key != "issuer_address_policy" {
					valid = false
					break
				}
			}
		}
		if valid {
			canonical, canonicalErr := conn.Identity.Canonical()
			resource, resourceErr := canonicalResourceURL(conn.ResourceURL)
			valid = canonicalErr == nil && resourceErr == nil && validIssuerCAFile(conn.IssuerCAFile) && conn.IssuerAddressPolicy.valid()
			if valid {
				conn.Identity = canonical
				conn.ResourceURL = resource
			}
		}
		rows = append(rows, registryRow{connection: conn, raw: raw, valid: valid})
	}
	return rows, nil
}

// Find returns the one saved connection addressed by an exact canonical resource
// URL or target. Resource and target aliases are intentionally resolved through
// the same ambiguity check; a legacy row without ResourceURL is target-only.
func (r *Registry) Find(alias string) (Connection, error) {
	var (
		match func(Connection) bool
		err   error
	)
	if strings.Contains(alias, "://") {
		resource, resourceErr := canonicalResourceURL(alias)
		if resourceErr != nil {
			return Connection{}, ErrInvalidIdentity
		}
		match = func(conn Connection) bool { return conn.ResourceURL != "" && conn.ResourceURL == resource }
	} else if target, targetErr := canonicalTarget(alias); targetErr == nil {
		match = func(conn Connection) bool { return conn.Identity.Target == target }
	} else {
		if strings.ContainsAny(alias, "/?#@") {
			return Connection{}, ErrInvalidIdentity
		}
		resource, resourceErr := canonicalResourceURL("https://" + alias)
		if resourceErr != nil {
			return Connection{}, ErrInvalidIdentity
		}
		match = func(conn Connection) bool { return conn.ResourceURL != "" && conn.ResourceURL == resource }
	}
	all, err := r.List()
	if err != nil {
		return Connection{}, err
	}
	found := make([]Connection, 0, 1)
	for _, conn := range all {
		if match(conn) {
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

func (r *Registry) targetForAlias(alias string) (string, error) {
	if target, err := canonicalTarget(alias); err == nil {
		return target, nil
	}
	conn, err := r.Find(alias)
	if err != nil {
		return "", err
	}
	return conn.Identity.Target, nil
}

// resourceForAlias identifies a resource alias without consulting registry
// state. It lets logout take the same resource-before-target lock order as
// enrollment, so a resource cannot move targets between resolution and delete.
func resourceForAlias(alias string) (string, bool, error) {
	if strings.Contains(alias, "://") {
		resource, err := canonicalResourceURL(alias)
		return resource, true, err
	}
	if _, err := canonicalTarget(alias); err == nil {
		return "", false, nil
	}
	if strings.ContainsAny(alias, "/?#@") {
		return "", false, ErrInvalidIdentity
	}
	resource, err := canonicalResourceURL("https://" + alias)
	return resource, true, err
}

// FindTarget returns the saved connection for target. Registry read failures take
// precedence; a target outside the enrollment grammar cannot name a saved row and
// returns credentialstore.ErrNotFound after a clean read.
func (r *Registry) FindTarget(target string) (Connection, error) {
	all, err := r.List()
	if err != nil {
		return Connection{}, err
	}
	id, err := (Identity{Target: target, Issuer: "https://invalid.example", ClientID: "x", Audience: "x", RedirectURI: "http://127.0.0.1/", Scopes: []string{"x"}}).Canonical()
	if err != nil {
		return Connection{}, credentialstore.ErrNotFound
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

// normalizeConnection canonicalises and validates a connection on the way IN to
// the registry. Both write paths (Upsert and Enroll) MUST go through it: a row
// that fails these rules is quarantined by readRows on the next load, so a
// caller that skipped validation would report a successful login whose entry is
// then invisible -- leaving its refresh token on disk, unreachable and
// unrevoked.
func normalizeConnection(conn Connection) (Connection, error) {
	// Programmatic legacy callers predate the persisted policy. They enroll with
	// the historical private posture; JSON input itself remains strict.
	if conn.IssuerAddressPolicy == "" {
		conn.IssuerAddressPolicy = IssuerAddressPolicyPrivate
	}
	id, err := conn.Identity.Canonical()
	if err != nil {
		return Connection{}, err
	}
	conn.Identity = id
	resource, err := canonicalResourceURL(conn.ResourceURL)
	if err != nil {
		return Connection{}, errors.New("clientauth: resource URL must be canonical HTTPS")
	}
	conn.ResourceURL = resource
	if !validIssuerCAFile(conn.IssuerCAFile) {
		return Connection{}, errors.New("clientauth: issuer CA path must be absolute and clean")
	}
	if !conn.IssuerAddressPolicy.valid() {
		return Connection{}, errors.New("clientauth: invalid issuer connection policy")
	}
	return conn, nil
}

func validIssuerCAFile(path string) bool {
	return path == "" || filepath.IsAbs(path) && filepath.Clean(path) == path
}

// Upsert stores conn as the single enrolment for its target and returns the
// identities it superseded. Credentials are keyed by identity, not target, so a
// caller holding the credential store must discard those: otherwise a superseded
// refresh token stays on disk indefinitely, unreachable and unrevoked.
func (r *Registry) Upsert(conn Connection) ([]Identity, error) {
	conn, err := normalizeConnection(conn)
	if err != nil {
		return nil, err
	}
	id := conn.Identity
	var unlockResource func()
	if conn.ResourceURL != "" {
		unlockResource, err = r.lockTarget(context.Background(), "resource:"+conn.ResourceURL)
		if err != nil {
			return nil, err
		}
		defer unlockResource()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()
	rows, err := r.readRows()
	if err != nil {
		return nil, err
	}
	// Collapse valid entries for this target, but carry quarantined raw rows
	// through untouched. They are intentionally invisible to callers while
	// remaining recoverable and lossless across unrelated mutations.
	kept := make([]registryRow, 0, len(rows)+1)
	var displaced []Identity
	for _, row := range rows {
		if !row.valid {
			kept = append(kept, row)
			continue
		}
		existing := row.connection
		if existing.Identity.Target != id.Target && (conn.ResourceURL == "" || existing.ResourceURL != conn.ResourceURL) {
			kept = append(kept, registryRow{connection: existing, valid: true})
			continue
		}
		if !existing.Identity.Equal(id) {
			displaced = append(displaced, existing.Identity)
		}
	}
	kept = append(kept, registryRow{connection: Connection{Identity: id, ResourceURL: conn.ResourceURL, IssuerCAFile: conn.IssuerCAFile, IssuerAddressPolicy: conn.IssuerAddressPolicy}, valid: true})
	if err := r.writeRows(kept); err != nil {
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
	rows, err := r.readRows()
	if err != nil {
		return 0, err
	}
	current := make([]Connection, 0, len(expected))
	kept := make([]registryRow, 0, len(rows))
	for _, row := range rows {
		if !row.valid {
			kept = append(kept, row)
			continue
		}
		conn := row.connection
		if conn.Identity.Target == canonical {
			current = append(current, conn)
		} else {
			kept = append(kept, registryRow{connection: conn, valid: true})
		}
	}
	if !sameConnections(current, expected) {
		return 0, credentialstore.ErrConflict
	}
	if len(current) == 0 {
		return 0, nil
	}
	if err := r.writeRows(kept); err != nil {
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
		left, right := conn.IssuerAddressPolicy, b[i].IssuerAddressPolicy
		if left == "" {
			left = IssuerAddressPolicyPrivate
		}
		if right == "" {
			right = IssuerAddressPolicyPrivate
		}
		if conn.ResourceURL != b[i].ResourceURL || conn.IssuerCAFile != b[i].IssuerCAFile || left != right || !conn.Identity.Equal(b[i].Identity) {
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

func (r *Registry) enrollmentSnapshot(target, resource string) ([]Connection, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	entries := make([]Connection, 0, len(all))
	for _, conn := range all {
		if conn.Identity.Target == target || resource != "" && conn.ResourceURL == resource {
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
	rows, err := r.readRows()
	if err != nil {
		return err
	}
	current := make([]Connection, 0, len(expected))
	kept := make([]registryRow, 0, len(rows)+len(desired))
	for _, row := range rows {
		if !row.valid {
			kept = append(kept, row)
			continue
		}
		conn := row.connection
		if conn.Identity.Target == target {
			current = append(current, conn)
		} else {
			kept = append(kept, registryRow{connection: conn, valid: true})
		}
	}
	if !sameConnections(current, expected) {
		return credentialstore.ErrConflict
	}
	for _, conn := range desired {
		kept = append(kept, registryRow{connection: conn, valid: true})
	}
	return r.writeRows(kept)
}

// replaceEnrollment conditionally replaces the target and removes any competing
// row for the same canonical resource. The caller holds the resource lock, so
// this is the single atomic resource-to-target enrollment transition.
func (r *Registry) replaceEnrollment(target, resource string, expected, desired []Connection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = r.lock.Unlock() }()
	rows, err := r.readRows()
	if err != nil {
		return err
	}
	current := make([]Connection, 0, len(expected))
	kept := make([]registryRow, 0, len(rows)+len(desired))
	for _, row := range rows {
		if !row.valid {
			kept = append(kept, row)
			continue
		}
		conn := row.connection
		if conn.Identity.Target == target {
			current = append(current, conn)
			continue
		}
		if resource != "" && conn.ResourceURL == resource {
			continue
		}
		kept = append(kept, registryRow{connection: conn, valid: true})
	}
	if !sameConnections(current, expected) {
		return credentialstore.ErrConflict
	}
	for _, conn := range desired {
		kept = append(kept, registryRow{connection: conn, valid: true})
	}
	return r.writeRows(kept)
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

func (r *Registry) writeRows(rows []registryRow) error {
	connections := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		if row.valid {
			data, err := json.Marshal(row.connection)
			if err != nil {
				return err
			}
			connections = append(connections, data)
		} else {
			connections = append(connections, row.raw)
		}
	}
	data, err := json.Marshal(registryRawFile{Version: 1, Connections: connections})
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
