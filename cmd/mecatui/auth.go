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
	if err == nil {
		return token, nil
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
			return "", err
		}
		return "", &client.AuthError{Reason: reason}
	}
	if errors.Is(err, clientauth.ErrCredentialCleanup) {
		return "", &client.AuthError{Reason: client.AuthCredentialCleanup}
	}
	if errors.Is(err, clientauth.ErrDiscovery) {
		return "", &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	return "", err
}

func mapAuthTokenSource(source client.TokenSource) client.TokenSource {
	return authTokenSource{source: source}
}
