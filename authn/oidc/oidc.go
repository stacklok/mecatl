// Package oidc verifies OIDC bearer tokens and projects their already-verified
// claims into the engine's narrow caller identity.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
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
	// Deprecated: use AllowPrivateHTTPSIssuer with TrustedCAFile for a private
	// HTTPS issuer. This legacy escape hatch remains for isolated tests that need
	// both HTTP and private-address access.
	InsecureAllowPrivateIssuer bool
	// AllowPrivateHTTPSIssuer permits only the configured issuer and optional
	// JWKS host's resolved private addresses. Its internal scoped transport
	// re-validates addresses on every dial, bounds keep-alives, refuses redirects,
	// and retains HTTPS and TLS hostname verification.
	// TrustedCAFile is required when this mode is enabled.
	AllowPrivateHTTPSIssuer bool
	// TrustedCAFile is the PEM CA bundle path. It is required (non-empty) when
	// AllowPrivateHTTPSIssuer is enabled, and is also passed through to the
	// underlying validator's own default-client CA loading for the legacy
	// InsecureAllowPrivateIssuer path. This package never reads it itself: see
	// TrustedCAPEM.
	TrustedCAFile string
	// TrustedCAPEM is the CA bundle's PEM-encoded bytes, used by
	// AllowPrivateHTTPSIssuer's scoped transport to validate the issuer
	// certificate. The caller is responsible for reading TrustedCAFile from
	// disk; this package must not touch the host filesystem (ADR 0206).
	TrustedCAPEM []byte
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
	// internalClient is owned by this validator; caller-supplied clients remain
	// caller-owned and are never closed here.
	internalClient *http.Client
	healthClient   *http.Client
	healthURL      string
	closeOnce      sync.Once
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
	if cfg.AllowPrivateHTTPSIssuer && cfg.TrustedCAFile == "" {
		return nil, fmt.Errorf("%w: trusted CA file is empty when private HTTPS issuer mode is enabled", ErrInvalidConfig)
	}
	if cfg.AllowPrivateHTTPSIssuer && cfg.HTTPClient != nil {
		return nil, fmt.Errorf("%w: custom HTTP client is not allowed with private HTTPS issuer mode", ErrInvalidConfig)
	}
	toolhiveConfig := authnConfig(cfg)
	var internalClient *http.Client
	if cfg.AllowPrivateHTTPSIssuer {
		client, err := newPrivateHTTPSClient(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		internalClient = client
		toolhiveConfig.HTTPClient = client
	} else {
		toolhiveConfig.HTTPClient = cfg.HTTPClient
	}
	validator, err := authn.NewValidator(ctx, toolhiveConfig)
	if err != nil {
		if internalClient != nil {
			internalClient.CloseIdleConnections()
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	healthClient := cfg.HTTPClient
	if internalClient != nil {
		healthClient = internalClient
	}
	if healthClient == nil {
		healthClient = http.DefaultClient
	}
	return &Validator{validator: validator, internalClient: internalClient, healthClient: healthClient, healthURL: cfg.JWKSURI}, nil
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
		CACertPath:        cfg.TrustedCAFile,
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

// Ready performs a bounded, read-only verifier dependency check. It never
// presents a credential or changes identity-provider state.
func (v *Validator) Ready(ctx context.Context) error {
	if v == nil || v.healthURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.healthURL, nil)
	if err != nil {
		return fmt.Errorf("OIDC health request: %w", err)
	}
	response, err := v.healthClient.Do(req)
	if err != nil {
		return fmt.Errorf("OIDC health request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("OIDC health endpoint returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return fmt.Errorf("OIDC health response: %w", err)
	}
	if len(body) > 1<<20 || !usableJWKS(body) {
		return errors.New("OIDC health endpoint returned no usable signing keys")
	}
	return nil
}

type jwksDocument struct {
	Keys []struct {
		KID string `json:"kid"`
		KTY string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
		X   string `json:"x"`
		Y   string `json:"y"`
	} `json:"keys"`
}

func usableJWKS(body []byte) bool {
	var document jwksDocument
	if json.Unmarshal(body, &document) != nil {
		return false
	}
	for _, key := range document.Keys {
		if key.KID == "" {
			continue
		}
		switch key.KTY {
		case "RSA":
			if key.N != "" && key.E != "" {
				return true
			}
		case "EC", "OKP":
			if key.X != "" && (key.KTY == "OKP" || key.Y != "") {
				return true
			}
		}
	}
	return false
}

// Close stops background JWKS refresh.
func (v *Validator) Close() error {
	if v == nil {
		return nil
	}
	v.closeOnce.Do(func() {
		if v.validator != nil {
			v.validator.Close()
		}
		if v.internalClient != nil {
			v.internalClient.CloseIdleConnections()
		}
	})
	return nil
}

func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var authnErr *authn.Error
	if errors.As(err, &authnErr) {
		category := authnCategory(authnErr.Reason)
		switch authnErr.Code {
		case authn.CodeUnavailable:
			return authenticationRejection{category: category, err: fmt.Errorf("%w: %s", ErrIdentityUnavailable, authnErr.Reason)}
		case authn.CodeInvalidToken, authn.CodeInvalidRequest:
			return authenticationRejection{category: category, err: fmt.Errorf("%w: %s", ErrInvalidToken, authnErr.Reason)}
		}
	}
	return authenticationRejection{category: "invalid_token", err: fmt.Errorf("%w: unrecognised validator failure", ErrInvalidToken)}
}

type authenticationRejection struct {
	category string
	err      error
}

func (e authenticationRejection) Error() string { return e.err.Error() }
func (e authenticationRejection) Unwrap() error { return e.err }

// AuthenticationRejectionCategory exposes only a closed, operator-safe label.
func (e authenticationRejection) AuthenticationRejectionCategory() string { return e.category }

func authnCategory(reason authn.Reason) string {
	switch reason {
	case authn.ReasonAudience:
		return "wrong_audience"
	case authn.ReasonIssuer:
		return "wrong_issuer"
	case authn.ReasonMalformed:
		return "malformed"
	case authn.ReasonSignature:
		return "signature"
	case authn.ReasonUnknownKID:
		return "unknown_kid"
	case authn.ReasonExpired:
		return "expired"
	case authn.ReasonNotYetValid:
		return "not_yet_valid"
	case authn.ReasonKeysUnavailable:
		return "jwks_unavailable"
	case authn.ReasonKeysStale:
		return "jwks_stale"
	default:
		return "invalid_token"
	}
}
