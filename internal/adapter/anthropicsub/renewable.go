package anthropicsub

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	// refreshSkew renews a grant before it lapses, so a token cannot expire
	// between the check and the provider receiving the request.
	refreshSkew = time.Minute

	// renewTimeout bounds one renewal including persistence. It replaces the
	// caller's deadline rather than extending it.
	renewTimeout = 30 * time.Second
)

// ErrNoGrant reports that no subscription grant is available.
var ErrNoGrant = errors.New("anthropic: no subscription grant is stored; run the subscription login")

// GrantStore persists a grant across refreshes and process restarts.
//
// Save is called while the renewal lock is held, so an implementation must not
// call back into the renewer. A Save failure fails the request: using a token
// that was not persisted would strand the rotated refresh half.
type GrantStore interface {
	Load(context.Context) (OAuthTokens, error)
	Save(context.Context, OAuthTokens) error
}

// Renewable resolves a valid access token per request, refreshing when the
// grant is within refreshSkew of expiry. It satisfies CredentialSource.
//
// Renewal is single-flighted: every refresh rotates the refresh token, so two
// concurrent exchanges would present one the provider has already retired.
type Renewable struct {
	mu     sync.Mutex
	tokens OAuthTokens
	store  GrantStore
	client *http.Client
	now    func() time.Time

	endpoints endpointSet
}

var _ CredentialSource = (*Renewable)(nil)

// NewRenewable builds a renewer over a persisted grant. now is the clock used
// for expiry decisions; nil uses time.Now.
func NewRenewable(store GrantStore, client *http.Client, now func() time.Time) (*Renewable, error) {
	if store == nil {
		return nil, errors.New("anthropic: renewable credential requires a grant store")
	}
	if now == nil {
		now = time.Now
	}
	if client == nil {
		client = newHTTPClient()
	}
	return &Renewable{store: store, client: client, now: now, endpoints: defaultEndpoints}, nil
}

// AccessToken returns a token valid at the current instant.
func (r *Renewable) AccessToken(ctx context.Context) (string, error) {
	tokens, err := r.resolve(ctx)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

// AccountID returns the account the grant is scoped to. It is routing
// metadata, so an absent value is not an error.
func (r *Renewable) AccountID(ctx context.Context) (string, error) {
	tokens, err := r.resolve(ctx)
	if err != nil {
		return "", err
	}
	return tokens.AccountID, nil
}

// GrantExpiresAt reports the absolute deadline of the loaded grant family so a
// caller can warn before the monthly re-login falls due.
func (r *Renewable) GrantExpiresAt(ctx context.Context) (time.Time, error) {
	tokens, err := r.resolve(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return tokens.GrantExpiresAt(), nil
}

func (r *Renewable) resolve(ctx context.Context) (OAuthTokens, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tokens.AccessToken == "" {
		loaded, err := r.store.Load(ctx)
		if err != nil {
			return OAuthTokens{}, err
		}
		r.tokens = loaded
	}
	if r.tokens.AccessToken == "" {
		return OAuthTokens{}, ErrNoGrant
	}

	now := r.now()
	if !r.expiring(now) {
		return r.tokens, nil
	}
	if r.tokens.RefreshToken == "" {
		if r.tokens.Expired(now) {
			return OAuthTokens{}, errors.New("anthropic: subscription access token expired and carries no refresh token; run the subscription login again")
		}
		return r.tokens, nil
	}

	// Renewal must not inherit the caller's cancellation. The provider retires
	// the submitted refresh token as it rotates, so a request abandoned
	// mid-exchange would discard a grant that is already dead server-side and
	// leave the stored one permanently invalid.
	renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), renewTimeout)
	defer cancel()

	rotated, err := refresh(renewCtx, r.client, r.tokens, r.endpoints)
	if err != nil {
		return OAuthTokens{}, err
	}
	if err := r.store.Save(renewCtx, rotated); err != nil {
		return OAuthTokens{}, err
	}
	r.tokens = rotated
	return rotated, nil
}

func (r *Renewable) expiring(now time.Time) bool {
	return !r.tokens.ExpiresAt.IsZero() && !now.Add(refreshSkew).Before(r.tokens.ExpiresAt)
}
