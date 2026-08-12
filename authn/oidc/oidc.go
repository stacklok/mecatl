// Package oidc verifies OIDC bearer tokens and projects their already-verified
// claims into the engine's narrow caller identity.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/toolhive-core/authn"

	"github.com/stacklok/mecatl/engine/session"
)

// Config is the trusted issuer, audience, and key-fetch policy for a Validator.
type Config struct {
	// Issuer is the exact OIDC issuer accepted in the token's iss claim.
	Issuer string
	// JWKSURI pins the signing-key endpoint; empty uses OIDC discovery.
	JWKSURI string
	// Audience is the single service audience accepted in the token's aud claim.
	Audience string
	// MaxJWKSStaleness bounds cached-key use during an identity-provider outage;
	// zero disables the upper bound.
	MaxJWKSStaleness time.Duration
	// InsecureAllowPrivateIssuer permits HTTP and private issuer/JWKS addresses.
	// It is intended only for isolated tests and disables SSRF protections.
	InsecureAllowPrivateIssuer bool
	// HTTPClient optionally supplies trusted roots and transport policy. Nil uses
	// the validator's hardened client. When set, the caller is responsible for
	// preserving equivalent redirect and private-address protections.
	HTTPClient *http.Client
}

// ErrInvalidToken identifies a malformed, invalid, or otherwise inadmissible
// bearer credential.
var ErrInvalidToken = errors.New("oidc: invalid token")

// ErrInvalidConfig identifies configuration that cannot construct a validator.
// The wrapped error deliberately does not expose ToolHive error types.
var ErrInvalidConfig = errors.New("oidc: invalid configuration")

// ErrIdentityUnavailable identifies a transient inability to obtain trusted key
// material from the identity provider.
var ErrIdentityUnavailable = errors.New("oidc: identity unavailable")

// Validator owns token verification and its background JWKS refresh. Call Close
// when it is no longer needed.
type Validator struct {
	validator *authn.Validator
}

// NewValidator constructs a fail-closed OIDC validator. Issuer and Audience
// must both be non-empty. Secure issuer/JWKS transport and audience validation
// remain enabled unless Config explicitly opts out where documented.
func NewValidator(ctx context.Context, cfg Config) (*Validator, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("%w: issuer is empty", ErrInvalidConfig)
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("%w: audience is empty", ErrInvalidConfig)
	}
	toolhiveConfig := authnConfig(cfg)
	toolhiveConfig.HTTPClient = cfg.HTTPClient
	validator, err := authn.NewValidator(ctx, toolhiveConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return &Validator{validator: validator}, nil
}

func authnConfig(cfg Config) authn.Config {
	return authn.Config{
		Issuer:            cfg.Issuer,
		Audiences:         []string{cfg.Audience},
		JWKSURL:           cfg.JWKSURI,
		MaxJWKSStaleness:  cfg.MaxJWKSStaleness,
		AllowAnyAudience:  false,
		InsecureAllowHTTP: cfg.InsecureAllowPrivateIssuer,
		AllowPrivateIP:    cfg.InsecureAllowPrivateIssuer,
	}
}

// Validate verifies bearer and returns its caller identity. A successfully
// verified claim set without a usable issuer and subject is rejected.
func (v *Validator) Validate(ctx context.Context, bearer string) (*session.Principal, error) {
	principal, err := v.validator.Validate(ctx, bearer)
	if err != nil {
		return nil, mapError(err)
	}
	out := session.PrincipalFromClaims(principal.Claims)
	if out == nil {
		return nil, fmt.Errorf("%w: verified claims have no issuer or subject", ErrInvalidToken)
	}
	return out, nil
}

// Close stops background JWKS refresh.
func (v *Validator) Close() error {
	if v != nil && v.validator != nil {
		v.validator.Close()
	}
	return nil
}

func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var authnErr *authn.Error
	if errors.As(err, &authnErr) {
		switch authnErr.Code {
		case authn.CodeUnavailable:
			return fmt.Errorf("%w: %s", ErrIdentityUnavailable, authnErr.Reason)
		case authn.CodeInvalidToken, authn.CodeInvalidRequest:
			return fmt.Errorf("%w: %s", ErrInvalidToken, authnErr.Reason)
		}
	}
	return fmt.Errorf("%w: unrecognised validator failure", ErrInvalidToken)
}
