package mcp

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var (
	// ErrOAuthLoginRequired reports that no usable durable credential remains.
	ErrOAuthLoginRequired = errors.New("mcp oauth: login required")
	// ErrOAuthUnavailable reports that OAuth cannot proceed without exposing its cause.
	ErrOAuthUnavailable = errors.New("mcp oauth: unavailable")
)

// OAuthError exposes a stable safe category and may retain only a sanitized,
// closed OAuth callback diagnostic.
type OAuthError struct {
	kind       error
	diagnostic error
}

func (e *OAuthError) Error() string {
	if e.diagnostic != nil {
		return e.diagnostic.Error()
	}
	if errors.Is(e.kind, ErrOAuthLoginRequired) {
		return "mcp OAuth login required"
	}
	if errors.Is(e.kind, ErrOAuthDCRRecoveryRequired) {
		return ErrOAuthDCRRecoveryRequired.Error()
	}
	return "mcp OAuth unavailable"
}

// Is reports whether the error has the requested safe OAuth category.
func (e *OAuthError) Is(target error) bool {
	return target == e.kind
}

func (e *OAuthError) Unwrap() error { return e.diagnostic }

func projectOAuthError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var provider *oauthlogin.AuthorizationErrorResponse
	if errors.As(err, &provider) {
		return &OAuthError{kind: ErrOAuthUnavailable, diagnostic: provider.Sanitized()}
	}
	var rejected *oauthlogin.CallbackRejectedError
	if errors.As(err, &rejected) {
		return &OAuthError{kind: ErrOAuthUnavailable, diagnostic: rejected.Sanitized()}
	}
	if errors.Is(err, ErrOAuthDCRRecoveryRequired) {
		return &OAuthError{kind: ErrOAuthDCRRecoveryRequired}
	}
	if errors.Is(err, ErrOAuthLoginRequired) {
		return &OAuthError{kind: ErrOAuthLoginRequired}
	}
	return &OAuthError{kind: ErrOAuthUnavailable}
}
