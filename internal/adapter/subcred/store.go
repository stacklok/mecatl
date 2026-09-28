// Package subcred persists model-subscription OAuth grants.
//
// Grants are held in the host's existing opaque credential store rather than
// in auth.yaml: a refresh rotates both halves of the grant on every renewal,
// and auth.yaml is a plaintext operator-authored file whose contract is an
// immutable value the operator supplies. Rotating secrets belong behind the
// encrypted, compare-and-swap store instead.
package subcred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// Namespace is the credential-store namespace holding subscription grants.
const Namespace = "mecatl-subscription"

// ErrNoGrant reports that no grant is stored for a provider.
var ErrNoGrant = errors.New("subcred: no subscription grant is stored")

// Grant is a persisted subscription grant. It is provider-neutral so one store
// serves every subscription provider; unused fields stay empty.
//
// Every rendering is redacted: the tokens are bearer secrets and the account
// fields are identity.
type Grant struct {
	Provider     string    `json:"provider"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	AuthorizedAt time.Time `json:"authorized_at,omitzero"`
	AccountID    string    `json:"account_id,omitempty"`
	Email        string    `json:"email,omitempty"`
	OrgID        string    `json:"org_id,omitempty"`
	OrgName      string    `json:"org_name,omitempty"`
}

var (
	_ fmt.Formatter  = Grant{}
	_ slog.LogValuer = Grant{}
)

// Format keeps every fmt rendering secret-safe, including when nested.
func (g Grant) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, g.redacted())
}

// LogValue gives structured handlers the same redacted representation as fmt.
func (g Grant) LogValue() slog.Value { return slog.StringValue(g.redacted()) }

func (g Grant) redacted() string {
	provider := g.Provider
	if provider == "" {
		provider = "unknown"
	}
	switch {
	case g.AccessToken == "":
		return "subscription grant for " + provider + " (absent)"
	case g.RefreshToken == "":
		return "subscription grant for " + provider + " (access only)"
	default:
		return "subscription grant for " + provider + " (renewable)"
	}
}

// Store persists grants in a namespace-bound opaque credential store.
//
// Writes are compare-and-swap against the version observed at read time, so a
// concurrent process that rotated the grant first is detected rather than
// silently overwritten with a token the provider has already retired.
type Store struct {
	mu      sync.Mutex
	backing credentialstore.Store
	// versions retains the version each provider's record carried when last
	// read or written, which is what makes the next write conditional.
	versions map[string]credentialstore.Version
}

// New wraps an opened credential store.
func New(backing credentialstore.Store) (*Store, error) {
	if backing == nil {
		return nil, errors.New("subcred: a credential store is required")
	}
	return &Store{backing: backing, versions: map[string]credentialstore.Version{}}, nil
}

func recordKey(provider string) ([]byte, error) {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return nil, errors.New("subcred: provider is required")
	}
	return []byte("grant/" + trimmed), nil
}

// Load returns the stored grant for a provider, or ErrNoGrant.
func (s *Store) Load(ctx context.Context, provider string) (Grant, error) {
	key, err := recordKey(provider)
	if err != nil {
		return Grant{}, err
	}
	record, err := s.backing.Get(ctx, key)
	if err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			return Grant{}, fmt.Errorf("%w for %s", ErrNoGrant, provider)
		}
		return Grant{}, err
	}

	var grant Grant
	if err := json.Unmarshal(record.Value, &grant); err != nil {
		// A record that cannot be decoded is not repaired in place: the
		// operator re-runs login rather than having a partial grant guessed at.
		return Grant{}, errors.New("subcred: stored subscription grant is unreadable; run the subscription login again")
	}
	s.mu.Lock()
	s.versions[provider] = record.Version
	s.mu.Unlock()
	return grant, nil
}

// Save writes a grant, conditional on the version last observed for that
// provider. A first write is create-only.
func (s *Store) Save(ctx context.Context, grant Grant) error {
	key, err := recordKey(grant.Provider)
	if err != nil {
		return err
	}
	if grant.AccessToken == "" {
		return errors.New("subcred: refusing to store a grant with no access token")
	}
	// Persisting the grant is this package's purpose. The bytes go only into
	// the namespace-bound opaque credential store, which is encrypted at rest
	// with owner-only custody; they are never logged, printed, or returned to
	// a caller, and every rendering of Grant is redacted.
	encoded, err := json.Marshal(grant) //nolint:gosec // G117: serializing the secret into the encrypted credential store is the intended operation
	if err != nil {
		return err
	}

	s.mu.Lock()
	previous, known := s.versions[grant.Provider]
	s.mu.Unlock()

	var expected *credentialstore.Version
	if known {
		expected = &previous
	}
	record, err := s.backing.Put(ctx, key, encoded, expected)
	if err == nil {
		s.mu.Lock()
		s.versions[grant.Provider] = record.Version
		s.mu.Unlock()
		return nil
	}

	// A create-only write that conflicts means a record already exists, and a
	// conditional write that conflicts means a peer rotated first. Both are
	// resolved by re-reading and retrying once against the current version, so
	// a fresh login can replace an existing grant.
	if !errors.Is(err, credentialstore.ErrConflict) {
		return err
	}
	current, getErr := s.backing.Get(ctx, key)
	if getErr != nil {
		return err
	}
	retried, putErr := s.backing.Put(ctx, key, encoded, &current.Version)
	if putErr != nil {
		return putErr
	}
	s.mu.Lock()
	s.versions[grant.Provider] = retried.Version
	s.mu.Unlock()
	return nil
}

// Delete removes a provider's grant. A missing grant is not an error, so
// logout is idempotent.
func (s *Store) Delete(ctx context.Context, provider string) error {
	key, err := recordKey(provider)
	if err != nil {
		return err
	}
	record, err := s.backing.Get(ctx, key)
	if err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.backing.Delete(ctx, key, record.Version); err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	delete(s.versions, provider)
	s.mu.Unlock()
	return nil
}
