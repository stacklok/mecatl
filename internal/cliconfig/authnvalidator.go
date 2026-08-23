package cliconfig

import (
	"context"
	"errors"
	"fmt"
	"os"

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
	// authn/oidc must not touch the host OS (ADR 0206): this layer reads the CA
	// file and hands the parsed bytes down.
	var caPEM []byte
	if c.AllowPrivateHTTPSIssuer {
		body, err := os.ReadFile(c.TrustedCAFile)
		if err != nil {
			return nil, fmt.Errorf("%w: read --oidc-ca-cert-file: %w", ErrOIDCMisconfigured, err)
		}
		caPEM = body
	}
	validator, err := oidcauthn.NewValidator(ctx, oidcauthn.Config{
		Issuer:                     c.Issuer,
		JWKSURI:                    c.JWKSURI,
		Audience:                   c.Audience,
		MaxJWKSStaleness:           c.MaxJWKSStaleness,
		InsecureAllowPrivateIssuer: c.InsecureAllowPrivateIssuer,
		AllowPrivateHTTPSIssuer:    c.AllowPrivateHTTPSIssuer,
		TrustedCAFile:              c.TrustedCAFile,
		TrustedCAPEM:               caPEM,
		HTTPClient:                 c.httpClient,
	})
	if err != nil {
		return nil, err
	}
	return authnValidator{validator: validator}, nil
}
