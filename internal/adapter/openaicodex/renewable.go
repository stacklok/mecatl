package openaicodex

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// refreshSkew renews a grant before it actually expires, so a token cannot
// lapse between the check and the provider receiving the request.
const refreshSkew = time.Minute

// ErrNoGrant reports that no subscription grant is available to renew.
var ErrNoGrant = errors.New("openai-codex: no subscription grant is stored; run a subscription login")

// TokenStore persists a grant across refreshes and process restarts.
//
// Save is called with a rotated grant while the renewal lock is held, so an
// implementation must not call back into the renewer. A Save failure fails the
// request: silently using a token that was not persisted would strand the
// rotated refresh token and force a fresh interactive login.
type TokenStore interface {
	Load(context.Context) (OAuthTokens, error)
	Save(context.Context, OAuthTokens) error
}

// RenewableCredential resolves a valid Credential per request, refreshing the
// grant when it is within refreshSkew of expiry.
//
// Renewal is single-flighted: concurrent requests that observe the same
// expiring token must produce one refresh, because each refresh rotates the
// refresh token and a second concurrent exchange would present one the
// provider has already retired.
type RenewableCredential struct {
	mu        sync.Mutex
	tokens    OAuthTokens
	store     TokenStore
	client    *http.Client
	now       func() time.Time
	endpoints endpointSet
}

// NewRenewableCredential builds a renewer over a persisted grant. now is the
// clock used for expiry decisions; nil uses time.Now.
func NewRenewableCredential(store TokenStore, client *http.Client, now func() time.Time) (*RenewableCredential, error) {
	if store == nil {
		return nil, errors.New("openai-codex: renewable credential requires a token store")
	}
	if now == nil {
		now = time.Now
	}
	if client == nil {
		client = newOAuthHTTPClient()
	}
	return &RenewableCredential{store: store, client: client, now: now, endpoints: defaultEndpoints}, nil
}

// Credential returns a credential valid at the current instant, refreshing the
// grant first when required.
func (c *RenewableCredential) Credential(ctx context.Context) (Credential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tokens.AccessToken == "" {
		loaded, err := c.store.Load(ctx)
		if err != nil {
			return Credential{}, err
		}
		c.tokens = loaded
	}
	if c.tokens.AccessToken == "" {
		return Credential{}, ErrNoGrant
	}

	now := c.now()
	if credential, err := c.tokens.Credential(now); err == nil && !c.expiring(now) {
		return credential, nil
	}
	if c.tokens.RefreshToken == "" {
		// Nothing to renew with: an access-only grant (a pasted token) is
		// still usable until it lapses, so surface its own validation verdict
		// rather than a renewal error.
		return c.tokens.Credential(now)
	}

	rotated, err := refresh(ctx, c.client, c.tokens.RefreshToken, c.endpoints)
	if err != nil {
		return Credential{}, err
	}
	if err := c.store.Save(ctx, rotated); err != nil {
		return Credential{}, err
	}
	c.tokens = rotated
	return rotated.Credential(c.now())
}

func (c *RenewableCredential) expiring(now time.Time) bool {
	return !c.tokens.ExpiresAt.IsZero() && !now.Add(refreshSkew).Before(c.tokens.ExpiresAt)
}
