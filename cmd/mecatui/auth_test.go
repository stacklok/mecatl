package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

type authTokenSourceFunc func(context.Context) (string, error)

func (f authTokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

func TestAuthTokenSourceMapsOnlyRecoveryCauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want client.AuthReason
	}{
		{"not enrolled", &clientauth.LoginRequiredError{Cause: clientauth.NotEnrolled}, client.AuthNotEnrolled},
		{"session expired", &clientauth.LoginRequiredError{Cause: clientauth.SessionExpired}, client.AuthSessionExpired},
		{"credential unusable", &clientauth.LoginRequiredError{Cause: clientauth.CredentialUnusable}, client.AuthCredentialUnusable},
		{"cleanup", errors.Join(errors.New("remove failed"), clientauth.ErrCredentialCleanup), client.AuthCredentialCleanup},
		{"issuer", clientauth.ErrDiscovery, client.AuthStorageUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := mapAuthTokenSource(authTokenSourceFunc(func(context.Context) (string, error) { return "", tc.err }))
			_, err := source.Token(t.Context())
			var authErr *client.AuthError
			if !errors.As(err, &authErr) || authErr.Reason != tc.want {
				t.Fatalf("Token() error = %v; want AuthError(%s)", err, tc.want)
			}
		})
	}
}

func TestAuthTokenSourcePreservesUnclassifiedErrors(t *testing.T) {
	unknownCause := clientauth.LoginRequiredCause("future_cause")
	for _, want := range []error{
		context.Canceled,
		clientauth.ErrTokenExchange,
		errors.New("token validation failed"),
		&clientauth.LoginRequiredError{Cause: unknownCause},
	} {
		source := mapAuthTokenSource(authTokenSourceFunc(func(context.Context) (string, error) { return "", want }))
		_, got := source.Token(t.Context())
		if got != want {
			t.Fatalf("Token() error = %v; want exact passthrough %v", got, want)
		}
	}
}

func TestAuthTokenSourceFetchesOnce(t *testing.T) {
	calls := 0
	source := mapAuthTokenSource(authTokenSourceFunc(func(context.Context) (string, error) {
		calls++
		return "opaque", nil
	}))
	if token, err := source.Token(t.Context()); err != nil || token != "opaque" || calls != 1 {
		t.Fatalf("Token() = %q, %v; calls=%d", token, err, calls)
	}
}
