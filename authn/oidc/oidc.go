// Package oidc verifies OIDC bearer tokens and projects their already-verified
// claims into the engine's narrow caller identity.
package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/stacklok/toolhive-core/authn"
	"github.com/stacklok/toolhive-core/networking"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	// maxIdentityDocumentBytes bounds a discovery or JWKS document read here.
	maxIdentityDocumentBytes = 1 << 20
	// readinessTimeout matches the timeout ToolHive applies to its own JWKS client.
	readinessTimeout = 15 * time.Second
)

// Config is the trusted issuer, audience, and key-fetch policy for a Validator.
type Config struct {
	// Issuer is the exact OIDC issuer accepted in the token's iss claim.
	Issuer string
	// JWKSURI pins the signing-key endpoint; empty uses OIDC discovery.
	JWKSURI string
	// Audience is the single service audience accepted in the token's aud claim.
	Audience string
	// AllowAnyAudience permits an empty Audience for resource clients whose
	// authorization server does not bind access tokens to an audience.
	AllowAnyAudience bool
	// MaxJWKSStaleness bounds cached-key use during an identity-provider outage;
	// zero disables the upper bound.
	MaxJWKSStaleness time.Duration
	// InsecureAllowPrivateIssuer permits HTTP and private issuer/JWKS addresses.
	// Deprecated: use AllowPrivateHTTPSIssuer with TrustedCAFile for a private
	// HTTPS issuer. This legacy escape hatch remains for isolated tests that need
	// both HTTP and private-address access.
	InsecureAllowPrivateIssuer bool
	// AllowPrivateHTTPSIssuer permits only the resolved private addresses of the
	// JWKS host when JWKSURI is set (the issuer is then never contacted), or of
	// the issuer host when JWKSURI is empty. Its internal scoped transport
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
	// disk; this package must not touch the host filesystem.
	TrustedCAPEM []byte
	// HTTPClient optionally supplies trusted roots and transport policy. Nil uses
	// the validator's hardened client. When set, the caller is responsible for
	// preserving equivalent redirect and private-address protections.
	HTTPClient *http.Client
	// OwnerFromSubID keys the returned principal by the `iss` and `sub` inside
	// the token's `sub_id` claim, an RFC 9493 `iss_sub` subject identifier,
	// rather than by the token's own `iss` and `sub`. As preconditions it
	// requires an RFC 9068 access token (`typ` header `at+jwt`) whose
	// `client_id` claim is in AcceptedClientIDs. Off by default.
	OwnerFromSubID bool
	// AcceptedClientIDs lists the OAuth clients whose access tokens are accepted.
	// It is required, and only allowed, with OwnerFromSubID.
	AcceptedClientIDs []string
}

// accessTokenType is the RFC 9068 JWT access-token media type. toolhive-core
// also accepts the `application/` prefix and rejects a missing `typ`.
const accessTokenType = "at+jwt"

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
	// subIDOwner is non-nil exactly when OwnerFromSubID is on. It is a pointer
	// so Validator stays comparable.
	subIDOwner *subIDOwner
}

// subIDOwner holds the OwnerFromSubID settings; acceptedClientIDs is non-empty.
type subIDOwner struct {
	acceptedClientIDs []string
}

// NewValidator constructs a fail-closed OIDC validator. Issuer must be non-empty;
// Audience must also be non-empty unless AllowAnyAudience is explicitly enabled.
// Secure issuer/JWKS transport remains enabled.
func NewValidator(ctx context.Context, cfg Config) (*Validator, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("%w: issuer is empty", ErrInvalidConfig)
	}
	if cfg.Audience == "" && !cfg.AllowAnyAudience {
		return nil, fmt.Errorf("%w: audience is empty", ErrInvalidConfig)
	}
	if cfg.AllowPrivateHTTPSIssuer && cfg.TrustedCAFile == "" {
		return nil, fmt.Errorf("%w: trusted CA file is empty when private HTTPS issuer mode is enabled", ErrInvalidConfig)
	}
	if cfg.AllowPrivateHTTPSIssuer && cfg.HTTPClient != nil {
		return nil, fmt.Errorf("%w: custom HTTP client is not allowed with private HTTPS issuer mode", ErrInvalidConfig)
	}
	if err := validateOwnerFromSubID(cfg); err != nil {
		return nil, err
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
	// Ready probes with the validator's own client when this package built it.
	healthClient := internalClient
	if healthClient == nil && cfg.JWKSURI != "" {
		healthClient, err = readinessClient(cfg)
		if err != nil {
			validator.Close()
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}
	out := &Validator{validator: validator, internalClient: internalClient, healthClient: healthClient, healthURL: cfg.JWKSURI}
	// validateOwnerFromSubID guarantees the client list is non-empty exactly
	// when OwnerFromSubID is on.
	if cfg.OwnerFromSubID {
		out.subIDOwner = &subIDOwner{acceptedClientIDs: slices.Clone(cfg.AcceptedClientIDs)}
	}
	return out, nil
}

// validateOwnerFromSubID requires a non-empty, well-formed client list exactly
// when OwnerFromSubID is on, so neither half is a silent no-op.
func validateOwnerFromSubID(cfg Config) error {
	if !cfg.OwnerFromSubID {
		if len(cfg.AcceptedClientIDs) != 0 {
			return fmt.Errorf("%w: accepted client IDs require owner from sub_id", ErrInvalidConfig)
		}
		return nil
	}
	if len(cfg.AcceptedClientIDs) == 0 {
		return fmt.Errorf("%w: owner from sub_id requires accepted client IDs", ErrInvalidConfig)
	}
	if slices.Contains(cfg.AcceptedClientIDs, "") {
		return fmt.Errorf("%w: accepted client IDs must not be empty", ErrInvalidConfig)
	}
	return nil
}

// readinessClient returns the client Ready uses when the validator owns no
// transport. It applies the policy ToolHive applies to its own JWKS fetches, so
// a readiness probe cannot follow a redirect to an internal address or hang: a
// supplied client keeps its transport but gains redirect refusal and a timeout
// when it set none, and otherwise the client enforces the private-address, CA,
// and timeout policy of the validator's configuration.
func readinessClient(cfg Config) (*http.Client, error) {
	if cfg.HTTPClient != nil {
		client := *cfg.HTTPClient
		if client.CheckRedirect == nil {
			client.CheckRedirect = refuseRedirects
		}
		if client.Timeout == 0 {
			client.Timeout = readinessTimeout
		}
		return &client, nil
	}
	client, err := networking.NewHttpClientBuilder().
		WithPrivateIPs(cfg.InsecureAllowPrivateIssuer).
		WithInsecureAllowHTTP(cfg.InsecureAllowPrivateIssuer).
		WithCABundle(cfg.TrustedCAFile).
		WithTimeout(readinessTimeout).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build readiness client: %w", err)
	}
	client.CheckRedirect = refuseRedirects
	return client, nil
}

func refuseRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func authnConfig(cfg Config) authn.Config {
	audiences := []string{cfg.Audience}
	allowAnyAudience := cfg.Audience == "" && cfg.AllowAnyAudience
	if allowAnyAudience {
		audiences = nil
	}
	var tokenTypes []string
	if cfg.OwnerFromSubID {
		tokenTypes = []string{accessTokenType}
	}
	return authn.Config{
		AcceptedTokenTypes: tokenTypes,
		Issuer:             cfg.Issuer,
		Audiences:          audiences,
		JWKSURL:            cfg.JWKSURI,
		MaxJWKSStaleness:   cfg.MaxJWKSStaleness,
		AllowAnyAudience:   allowAnyAudience,
		InsecureAllowHTTP:  cfg.InsecureAllowPrivateIssuer,
		AllowPrivateIP:     cfg.InsecureAllowPrivateIssuer,
		CACertPath:         cfg.TrustedCAFile,
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
	if v.subIDOwner == nil {
		return out, nil
	}
	if clientID, ok := principal.Claims["client_id"].(string); !ok || !slices.Contains(v.subIDOwner.acceptedClientIDs, clientID) {
		return nil, subIDRejection("client_id claim is missing or not an accepted client")
	}
	identity := issSubIdentifier(principal.Claims["sub_id"])
	if identity == nil {
		return nil, subIDRejection("sub_id claim is missing or not a well-formed iss_sub identifier")
	}
	out.Issuer, out.Subject = identity.Issuer, identity.Subject
	return out, nil
}

// issSubIdentifier reads an RFC 9493 `iss_sub` subject identifier, applying the
// same identity rule as the token's own `iss` and `sub`: both non-empty strings
// free of the reserved owner-key separator. Nil means it is not usable.
func issSubIdentifier(claim any) *session.Principal {
	subID, isObject := claim.(map[string]any)
	if !isObject || subID["format"] != "iss_sub" {
		return nil
	}
	return session.PrincipalFromClaims(subID)
}

func subIDRejection(reason string) error {
	return authenticationRejection{category: "invalid_token", err: fmt.Errorf("%w: %s", ErrInvalidToken, reason)}
}

// Ready performs a bounded, read-only check that the configured JWKS endpoint
// returns usable signing keys. It changes no identity-provider state and adds
// no credential of its own; a validator from NewKubernetesValidator sends its
// token file's contents to that one endpoint. Redirects are refused.
//
// Ready checks nothing when no JWKSURI is configured (discovery mode), where
// the endpoint is only known to the underlying validator, and returns nil.
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
	body, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityDocumentBytes+1))
	if err != nil {
		return fmt.Errorf("OIDC health response: %w", err)
	}
	if len(body) > maxIdentityDocumentBytes || !usableJWKS(body) {
		return errors.New("OIDC health endpoint returned no usable signing keys")
	}
	return nil
}

// usableJWKS reports whether body is a key set holding at least one key that
// parses as an RSA or EC public key, the key types toolhive-core accepts. It
// parses the way core does, so a malformed key does not hide its siblings.
// It approximates core's policy rather than reproducing it: core also enforces
// a minimum RSA size, use, key_ops, and algorithm match, so a set of only
// weak or mismatched keys passes here while core rejects it.
func usableJWKS(body []byte) bool {
	set, err := jwk.Parse(body, jwk.WithStrictKeySetParsing(false))
	if err != nil {
		return false
	}
	for i := range set.Len() {
		key, ok := set.Key(i)
		if !ok {
			continue
		}
		if _, err := jwk.Export[*rsa.PublicKey](key); err == nil {
			return true
		}
		if _, err := jwk.Export[*ecdsa.PublicKey](key); err == nil {
			return true
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
		switch authnErr.Reason {
		case authn.ReasonKeysUnavailable, authn.ReasonKeysStale:
			return authenticationRejection{category: category, err: fmt.Errorf("%w: %s", ErrIdentityUnavailable, authnErr.Reason)}
		}
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
