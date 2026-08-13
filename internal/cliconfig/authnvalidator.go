package cliconfig

import (
	"context"
	"errors"
	"fmt"

	oidcauthn "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// authnValidator keeps the server's identity-error contract at the root module
// boundary while reusable token verification lives in authn/oidc.
type authnValidator struct {
	validator *oidcauthn.Validator
}

func (a authnValidator) Validate(ctx context.Context, bearer string) (*session.Principal, error) {
	principal, err := a.validator.Validate(ctx, bearer)
	if err != nil {
		return nil, errToSentinel(err)
	}
	return principal, nil
}

func (a authnValidator) Close() error { return a.validator.Close() }

func errToSentinel(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, oidcauthn.ErrIdentityUnavailable):
		return fmt.Errorf("%w: %v", server.ErrIdentityUnavailable, err)
	case errors.Is(err, oidcauthn.ErrInvalidToken):
		return fmt.Errorf("%w: %v", server.ErrInvalidToken, err)
	default:
		return fmt.Errorf("%w: unrecognised validator failure", server.ErrInvalidToken)
	}
}

func defaultNewValidator(ctx context.Context, c OIDCConfig) (server.PrincipalValidator, error) {
	validator, err := oidcauthn.NewValidator(ctx, oidcauthn.Config{
		Issuer:                     c.Issuer,
		JWKSURI:                    c.JWKSURI,
		Audience:                   c.Audience,
		MaxJWKSStaleness:           c.MaxJWKSStaleness,
		InsecureAllowPrivateIssuer: c.InsecureAllowPrivateIssuer,
		HTTPClient:                 c.httpClient,
	})
	if err != nil {
		return nil, err
	}
	return authnValidator{validator: validator}, nil
}
