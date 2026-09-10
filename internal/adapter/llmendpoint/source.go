package llmendpoint

import (
	"context"
	"time"
)

// LifecycleSource adapts the durable credential lifecycle to gateway request
// transport. It never holds the lifecycle lock while an HTTP request is in
// flight.
type LifecycleSource struct {
	Identity   CredentialIdentity
	Repository *CredentialRepository
	Lifecycle  Lifecycle
	Now        func() time.Time
}

// Token returns a usable cached bearer, refreshing an expired record before it
// leaves the lifecycle transaction.
func (s *LifecycleSource) Token(ctx context.Context) (string, error) {
	if s == nil || s.Repository == nil || s.Lifecycle.Locker == nil {
		return "", ErrNotEnrolled
	}
	var rec CredentialRecord
	err := s.Lifecycle.Locker.With(ctx, s.Identity, func(ctx context.Context) error {
		var err error
		rec, err = s.Repository.Load(ctx, s.Identity)
		return err
	})
	if err != nil {
		return "", err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if rec.Token.Expiry.After(now()) {
		return rec.Token.AccessToken, nil
	}
	tok, err := s.Lifecycle.RefreshRejected(ctx, s.Identity, rec.Token.AccessToken)
	return tok.AccessToken, err
}

// Refresh handles one rejected bearer. The lifecycle lock makes concurrent
// callers observe and reuse a newer token rather than refreshing it again.
func (s *LifecycleSource) Refresh(ctx context.Context, rejected string) (string, error) {
	if s == nil {
		return "", ErrNotEnrolled
	}
	tok, err := s.Lifecycle.RefreshRejected(ctx, s.Identity, rejected)
	return tok.AccessToken, err
}
