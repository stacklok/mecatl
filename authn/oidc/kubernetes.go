package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/stacklok/toolhive-core/authn"
	"github.com/stacklok/toolhive-core/networking"
)

const maxKubernetesTokenBytes = 64 << 10

// KubernetesBootstrapConfig supplies the fixed Kubernetes API endpoints and the
// broker's projected service-account token. The token's issuer claim is
// untrusted until the configured discovery endpoint confirms it.
//
// TokenFile is sent as the bearer to both endpoints and is re-read for every
// request, so rotation is picked up. TokenSource returns that same token's
// contents once, to read its issuer claim: this package may not read files
// itself, so the caller does.
type KubernetesBootstrapConfig struct {
	DiscoveryURL string
	JWKSURI      string
	TokenFile    string
	TokenSource  func() ([]byte, error)
}

// NewKubernetesValidator derives the cluster's issuer from the projected token,
// confirms it through the configured discovery endpoint, and constructs a
// validator that fetches keys from the configured JWKS endpoint. Neither the
// token claim nor the discovery response selects a network destination.
//
// cfg.Issuer and cfg.JWKSURI must be empty: both come from bootstrap.
// cfg.TrustedCAFile is the CA that signs the API server's certificate. The API
// server is reached over private addresses, so TLS verification against that
// CA is what authenticates it. A dedicated cfg.Audience is required: every pod
// in the cluster can mint tokens from this issuer, so only the audience
// separates the broker's callers from all of them.
//
// The bearer is re-read per request by toolhive-core v0.0.51 or later.
func NewKubernetesValidator(ctx context.Context, cfg Config, bootstrap KubernetesBootstrapConfig) (*Validator, error) {
	switch {
	case bootstrap.TokenFile == "" || bootstrap.TokenSource == nil:
		return nil, fmt.Errorf("%w: Kubernetes token file and token source are required", ErrInvalidConfig)
	case bootstrap.DiscoveryURL == "" || bootstrap.JWKSURI == "":
		return nil, fmt.Errorf("%w: Kubernetes discovery and JWKS URLs are required", ErrInvalidConfig)
	case cfg.Issuer != "" || cfg.JWKSURI != "":
		return nil, fmt.Errorf("%w: Kubernetes issuer and JWKS URL come from the bootstrap", ErrInvalidConfig)
	case cfg.Audience == "" || cfg.AllowAnyAudience:
		return nil, fmt.Errorf("%w: Kubernetes validation requires a dedicated audience", ErrInvalidConfig)
	case cfg.TrustedCAFile == "":
		return nil, fmt.Errorf("%w: Kubernetes validation requires a trusted CA file", ErrInvalidConfig)
	case cfg.HTTPClient != nil || cfg.InsecureAllowPrivateIssuer || cfg.AllowPrivateHTTPSIssuer:
		return nil, fmt.Errorf("%w: Kubernetes validation builds its own HTTP client", ErrInvalidConfig)
	}
	if err := validateKubernetesEndpointOrigin(bootstrap.DiscoveryURL, bootstrap.JWKSURI); err != nil {
		return nil, fmt.Errorf("%w: Kubernetes bootstrap endpoints: %v", ErrInvalidConfig, err)
	}
	token, err := readKubernetesToken(bootstrap.TokenSource)
	if err != nil {
		return nil, err
	}
	candidate, err := issuerFromJWT(token)
	if err != nil {
		return nil, err
	}

	// One client serves the discovery request, core's key fetches, and Ready, so
	// each goes through the same transport, CA, token file and timeout.
	client, err := networking.NewHttpClientBuilder().
		WithPrivateIPs(true).
		WithCABundle(cfg.TrustedCAFile).
		WithTokenFromFile(bootstrap.TokenFile).
		WithTimeout(readinessTimeout).
		Build()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	client.CheckRedirect = refuseRedirects // the bearer must not follow a redirect
	fail := func(err error) (*Validator, error) {
		client.CloseIdleConnections()
		return nil, err
	}

	discovered, err := fetchDiscoveryIssuer(ctx, client, bootstrap.DiscoveryURL)
	if err != nil {
		return fail(err)
	}
	if discovered != candidate {
		return fail(fmt.Errorf("%w: Kubernetes discovery does not match the projected token issuer", ErrInvalidConfig))
	}
	cfg.Issuer, cfg.JWKSURI = candidate, bootstrap.JWKSURI
	toolhiveConfig := authnConfig(cfg)
	toolhiveConfig.HTTPClient = client
	validator, err := authn.NewValidator(ctx, toolhiveConfig)
	if err != nil {
		return fail(fmt.Errorf("%w: %v", ErrInvalidConfig, err))
	}
	return &Validator{validator: validator, internalClient: client, healthClient: client, healthURL: cfg.JWKSURI}, nil
}

func validateKubernetesEndpointOrigin(discoveryURL, jwksURL string) error {
	discovery, err := parseHTTPSURL(discoveryURL)
	if err != nil {
		return fmt.Errorf("discovery URL: %v", err)
	}
	jwks, err := parseHTTPSURL(jwksURL)
	if err != nil {
		return fmt.Errorf("JWKS URL: %v", err)
	}
	if !strings.EqualFold(discovery.Hostname(), jwks.Hostname()) || effectivePort(discovery) != effectivePort(jwks) {
		return errors.New("discovery and JWKS URLs must use the same HTTPS origin")
	}
	return nil
}

func parseHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("must be an absolute HTTPS URL without credentials or a fragment")
	}
	return u, nil
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

// fetchDiscoveryIssuer returns the issuer the discovery endpoint reports. Errors
// are generic: the request carries the projected token.
func fetchDiscoveryIssuer(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("%w: Kubernetes discovery request", ErrInvalidConfig)
	}
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: Kubernetes discovery unavailable", ErrInvalidConfig)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: Kubernetes discovery rejected", ErrInvalidConfig)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityDocumentBytes+1))
	if err != nil || len(body) > maxIdentityDocumentBytes {
		return "", fmt.Errorf("%w: Kubernetes discovery response", ErrInvalidConfig)
	}
	var document struct {
		Issuer string `json:"issuer"`
	}
	if json.Unmarshal(body, &document) != nil || document.Issuer == "" {
		return "", fmt.Errorf("%w: Kubernetes discovery response", ErrInvalidConfig)
	}
	return document.Issuer, nil
}

// readKubernetesToken returns the token source's contents as a compact JWT.
// Errors never include the token.
func readKubernetesToken(source func() ([]byte, error)) (string, error) {
	data, err := source()
	token := strings.TrimSpace(string(data))
	if err != nil || len(data) > maxKubernetesTokenBytes || strings.Count(token, ".") != 2 {
		return "", fmt.Errorf("%w: read projected Kubernetes token", ErrInvalidConfig)
	}
	return token, nil
}

// issuerFromJWT reads the unverified issuer claim of a compact JWT.
func issuerFromJWT(token string) (string, error) {
	claims, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		return "", fmt.Errorf("%w: projected Kubernetes token is malformed", ErrInvalidConfig)
	}
	var payload struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(claims, &payload) != nil || payload.Issuer == "" {
		return "", fmt.Errorf("%w: projected Kubernetes token has no issuer", ErrInvalidConfig)
	}
	return payload.Issuer, nil
}
