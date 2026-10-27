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

// TestAuthTokenSourceMarksIssuerFailures: an unreachable issuer, rejected
// discovery, or failed token endpoint is an infrastructure problem, not
// evidence the local keyring/registry/store is broken. It stays unclassified
// rather than steer the user toward local-storage recovery, and is marked so a
// failed forced refresh reports it instead of the server's rejection.
func TestAuthTokenSourceMarksIssuerFailures(t *testing.T) {
	for _, want := range []error{clientauth.ErrDiscovery, clientauth.ErrTokenExchange} {
		source := mapAuthTokenSource(refreshingAuthTokenSourceFunc(func(context.Context) (string, error) { return "", want }))
		_, tokenErr := source.Token(t.Context())
		_, refreshErr := source.(client.TokenRefresher).Refresh(t.Context())
		for _, got := range []error{tokenErr, refreshErr} {
			var issuer *client.IssuerUnavailableError
			if !errors.Is(got, want) || !errors.As(got, &issuer) || got.Error() != want.Error() {
				t.Fatalf("error = %v; want %v marked as an issuer failure", got, want)
			}
			if reason, ok := client.AuthFailure(got, true); ok {
				t.Fatalf("AuthFailure(%v) = %q; want unclassified", got, reason)
			}
		}
	}
}

type refreshingAuthTokenSourceFunc func(context.Context) (string, error)

func (f refreshingAuthTokenSourceFunc) Token(ctx context.Context) (string, error)   { return f(ctx) }
func (f refreshingAuthTokenSourceFunc) Refresh(ctx context.Context) (string, error) { return f(ctx) }

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
