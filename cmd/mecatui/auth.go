package main

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

type authTokenSource struct {
	source client.TokenSource
}

func (s authTokenSource) Token(ctx context.Context) (string, error) {
	token, err := s.source.Token(ctx)
	return token, mapAuthError(err)
}

// refreshingAuthTokenSource also forwards forced refreshes, so the client can
// send a fresh token on session-opening requests.
type refreshingAuthTokenSource struct {
	authTokenSource
	refresher client.TokenRefresher
}

func (s refreshingAuthTokenSource) Refresh(ctx context.Context) (string, error) {
	token, err := s.refresher.Refresh(ctx)
	return token, mapAuthError(err)
}

func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	var required *clientauth.LoginRequiredError
	if errors.As(err, &required) {
		var reason client.AuthReason
		switch required.Cause {
		case clientauth.NotEnrolled:
			reason = client.AuthNotEnrolled
		case clientauth.SessionExpired:
			reason = client.AuthSessionExpired
		case clientauth.CredentialUnusable:
			reason = client.AuthCredentialUnusable
		default:
			return err
		}
		return &client.AuthError{Reason: reason}
	}
	if errors.Is(err, clientauth.ErrCredentialCleanup) {
		return &client.AuthError{Reason: client.AuthCredentialCleanup}
	}
	// ErrDiscovery means the issuer was unreachable, its TLS was untrusted, or
	// JWKS failed to load; ErrTokenExchange that its token endpoint failed.
	// Both are infrastructure/network problems, not evidence the local
	// keyring/registry/store is broken. The marker leaves them in AuthFailure's
	// deliberate unclassified case, as at startup, and lets a failed forced
	// refresh report them instead of the server's rejection.
	if errors.Is(err, clientauth.ErrDiscovery) || errors.Is(err, clientauth.ErrTokenExchange) {
		return &client.IssuerUnavailableError{Err: err}
	}
	return err
}

func mapAuthTokenSource(source client.TokenSource) client.TokenSource {
	wrapped := authTokenSource{source: source}
	if refresher, ok := source.(client.TokenRefresher); ok {
		return refreshingAuthTokenSource{authTokenSource: wrapped, refresher: refresher}
	}
	return wrapped
}
