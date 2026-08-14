package mcp

import (
	"context"
	"errors"
)

var (
	// ErrOAuthLoginRequired reports that no usable durable credential remains.
	ErrOAuthLoginRequired = errors.New("mcp oauth: login required")
	// ErrOAuthUnavailable reports that OAuth cannot proceed without exposing its cause.
	ErrOAuthUnavailable = errors.New("mcp oauth: unavailable")
)

// OAuthError exposes only a stable safe category and never retains its cause.
type OAuthError struct {
	kind error
}

func (e *OAuthError) Error() string {
	if errors.Is(e.kind, ErrOAuthLoginRequired) {
		return "mcp OAuth login required"
	}
	return "mcp OAuth unavailable"
}

// Is reports whether the error has the requested safe OAuth category.
func (e *OAuthError) Is(target error) bool {
	return target == e.kind
}

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
	if errors.Is(err, ErrOAuthLoginRequired) {
		return &OAuthError{kind: ErrOAuthLoginRequired}
	}
	return &OAuthError{kind: ErrOAuthUnavailable}
}
