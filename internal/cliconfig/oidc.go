// Package cliconfig: OIDC caller-identity wiring shared by the server mains
// (cmd/mecated, cmd/mecak8s), so the flag names, the required-audience rule and
// the fail-CLOSED startup decision cannot drift between them (ADR 0204
// decision 3).
//
// Token VALIDATION is not implemented here and never will be: it is delegated to
// the validator behind server.PrincipalValidator. This file only resolves the
// operator's flags into one and decides that a broken resolution is FATAL.

package cliconfig

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// OIDCConfig carries the caller-identity flags. The zero value is identity OFF
// — the byte-identical no-auth posture.
type OIDCConfig struct {
	// Issuer is the IdP that mints the tokens (the `iss` claim, byte-exact). It
	// is the ON switch: empty means caller identity is off.
	Issuer string
	// JWKSURI, when set, is the STATIC signing-key endpoint; it short-circuits
	// OIDC discovery (the offline-test and air-gap hook).
	JWKSURI string
	// Audience is the `aud` this deployment accepts. REQUIRED when Issuer is
	// set: an audience-less verifier accepts tokens minted for other services.
	Audience string

	// MaxJWKSStaleness bounds how long cached signing keys remain trusted when
	// refresh cannot reach the IdP. Zero explicitly disables the upper bound.
	MaxJWKSStaleness time.Duration

	// NewValidator constructs the token validator. Nil selects the production
	// toolhive-core/authn adapter. Tests may replace it to observe construction or
	// force a startup failure; it is not a deployment extension point.
	NewValidator func(ctx context.Context, c OIDCConfig) (server.PrincipalValidator, error)

	// httpClient is an in-package test seam for a local TLS issuer's private CA.
	// Production flag parsing leaves it nil, preserving the hardened client.
	httpClient *http.Client

	// InsecureAllowPrivateIssuer is the deprecated legacy escape hatch. It permits
	// BOTH HTTP and private issuer/JWKS addresses; use AllowPrivateHTTPSIssuer
	// with TrustedCAFile for a private HTTPS issuer.
	InsecureAllowPrivateIssuer bool
	// AllowPrivateHTTPSIssuer permits only private-address admission for an HTTPS
	// issuer/JWKS endpoint. It requires TrustedCAFile and retains TLS, hostname,
	// redirect, and DNS-pinned dialing protections.
	AllowPrivateHTTPSIssuer bool
	// TrustedCAFile is the PEM CA bundle required by AllowPrivateHTTPSIssuer.
	TrustedCAFile string
}

// InsecureIssuerWarning returns the operator-facing warning for a configuration
// that has relaxed the issuer-URL and private-address checks, or "" when it has
// not.
//
// It is a returned STRING rather than a log call because cliconfig owns no logger
// — the same shape as ResolvedKeys.AuthFileWarning, which the cmd/ mains surface
// with slog.Warn. Both server mains log this one too.
//
// Silence here is how a test flag becomes a production vulnerability: an operator
// who copy-pastes it out of a test fixture gets no other signal that they have
// switched off an SSRF defence.
func (c OIDCConfig) InsecureIssuerWarning() string {
	if !c.Enabled() {
		return ""
	}
	if c.InsecureAllowPrivateIssuer {
		return "SECURITY: --oidc-insecure-allow-private-issuer is deprecated and is set. " +
			"It permits BOTH an http:// issuer/JWKS URL and private, loopback, or link-local " +
			"addresses, disabling the guard against a jwks_uri aimed at cloud instance metadata " +
			"(169.254.169.254). Use --oidc-allow-private-https-issuer with --oidc-ca-cert-file " +
			"for a private HTTPS issuer; must NOT be used in a real deployment."
	}
	if c.AllowPrivateHTTPSIssuer {
		return "SECURITY: --oidc-allow-private-https-issuer is set. Only the configured issuer/JWKS " +
			"hosts' pinned private addresses are admitted; HTTPS, CA and hostname validation, redirect refusal, " +
			"and per-dial DNS-pinned checks remain enforced."
	}
	return ""
}

// Enabled reports whether the operator asked for caller identity.
func (c OIDCConfig) Enabled() bool { return c.Issuer != "" }

// DefaultMaxJWKSStaleness is the bounded-by-default key-cache policy (ADR 0205).
const DefaultMaxJWKSStaleness = time.Hour

// RegisterOIDCFlags registers the caller-identity flags on fs. Both server
// mains call it so the names and help text are identical.
func RegisterOIDCFlags(fs *flag.FlagSet, c *OIDCConfig) {
	fs.StringVar(&c.Issuer, "oidc-issuer", "",
		"OIDC issuer URL (the `iss` claim, byte-exact) whose tokens identify callers. Setting it turns caller identity ON: every request must present a bearer the IdP vouches for, and the verified (iss, sub) is recorded as the session owner. Empty (default) disables it — requests are processed unauthenticated exactly as before. Requires --oidc-audience; a validator that cannot be constructed is FATAL, never a silent fall-back to unauthenticated")
	fs.StringVar(&c.JWKSURI, "oidc-jwks-uri", "",
		"STATIC JWKS endpoint for --oidc-issuer; short-circuits OIDC discovery (the air-gapped / pinned-key deployment). Empty derives it from the issuer's discovery document")
	fs.BoolVar(&c.InsecureAllowPrivateIssuer, "oidc-insecure-allow-private-issuer", false,
		"DEPRECATED TEST-ONLY legacy escape hatch. Permits BOTH an http:// OIDC issuer/JWKS URL and private, loopback, or link-local addresses, disabling the cloud-metadata SSRF guard. Use --oidc-allow-private-https-issuer with --oidc-ca-cert-file for a private HTTPS issuer; never set this in production")
	fs.BoolVar(&c.AllowPrivateHTTPSIssuer, "oidc-allow-private-https-issuer", false,
		"Permit an HTTPS OIDC issuer/JWKS URL to resolve to a private, loopback, or link-local address. Requires --oidc-ca-cert-file; HTTPS, CA and hostname validation, redirect refusal, and DNS-pinned dialing remain enforced")
	fs.StringVar(&c.TrustedCAFile, "oidc-ca-cert-file", "",
		"PEM CA bundle for --oidc-allow-private-https-issuer; required when private HTTPS issuer admission is enabled")
	fs.StringVar(&c.Audience, "oidc-audience", "",
		"audience (`aud`) this deployment accepts, REQUIRED with --oidc-issuer: an audience-less verifier would accept tokens minted for a different service")
	fs.DurationVar(&c.MaxJWKSStaleness, "oidc-max-jwks-staleness", DefaultMaxJWKSStaleness,
		"maximum age of cached JWKS signing keys when refresh cannot reach the IdP; stale, unrefreshable keys yield 503 instead of validating tokens. 0 disables the upper bound; negative values are rejected")
}

// ErrOIDCMisconfigured is returned when caller identity is requested but cannot
// be wired. It is fatal at startup by design.
var ErrOIDCMisconfigured = errors.New("oidc: misconfigured")

// ValidateOIDCAuthToken rejects two incompatible edge-authentication modes. An
// opaque static bearer cannot also be the OIDC JWT that caller identity validates.
// Keeping this in shared config makes mecated and mecak8s fail identically.
func ValidateOIDCAuthToken(c OIDCConfig, authToken string) error {
	if c.Enabled() && authToken != "" {
		return fmt.Errorf("%w: --auth-token and --oidc-issuer are mutually exclusive", ErrOIDCMisconfigured)
	}
	return nil
}

// OIDCValidator resolves c into a token validator, or (nil, nil) when caller
// identity is off (the unchanged path).
//
// ctx MUST be the SERVER-ROOT context: the validator owns background JWKS
// refresh, so binding it to a per-request context would tear key rotation down
// with the first request.
//
// Every failure is FATAL — the caller must refuse to start. Degrading to the
// unauthenticated path here would silently turn an authenticated deployment into
// an open one.
func OIDCValidator(ctx context.Context, c OIDCConfig) (server.PrincipalValidator, error) {
	if c.MaxJWKSStaleness < 0 {
		return nil, fmt.Errorf("%w: --oidc-max-jwks-staleness must not be negative: %s", ErrOIDCMisconfigured, c.MaxJWKSStaleness)
	}
	if !c.Enabled() {
		return nil, nil
	}
	if c.AllowPrivateHTTPSIssuer && c.TrustedCAFile == "" {
		return nil, fmt.Errorf("%w: --oidc-allow-private-https-issuer requires --oidc-ca-cert-file", ErrOIDCMisconfigured)
	}
	if c.NewValidator == nil {
		// Default to the real adapter (authnvalidator.go). It is defaulted HERE, in
		// the one place the config is resolved, rather than in each main: a main
		// that forgot would refuse to start with "no validator available", which is
		// fail-closed but indistinguishable from the pre-dependency state — the
		// exact confusion that made a kind rollout look like a validator bug.
		c.NewValidator = defaultNewValidator
	}
	// The validator's background JWKS refresh has no caller: it runs as the
	// explicit system principal (ADR 0204 decision 7).
	v, err := c.NewValidator(syscaller.Context(ctx, syscaller.RootJWKSRefresh), c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOIDCMisconfigured, err)
	}
	if v == nil {
		return nil, fmt.Errorf("%w: validator constructor returned no validator", ErrOIDCMisconfigured)
	}
	return v, nil
}
