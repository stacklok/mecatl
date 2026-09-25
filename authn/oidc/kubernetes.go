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

	"github.com/stacklok/mecatl/authn/oidc/scopedhttps"
)

const maxKubernetesTokenBytes = 64 << 10

// KubernetesBootstrapConfig supplies the fixed Kubernetes API endpoints and a
// projected-token reader used to discover the API server's OIDC issuer. The
// token's issuer claim is untrusted until the configured discovery endpoint
// confirms it. TokenSource is called for every Kubernetes API request.
type KubernetesBootstrapConfig struct {
	DiscoveryURL string
	JWKSURI      string
	TokenSource  func() ([]byte, error)
}

// NewKubernetesValidator derives an issuer from the broker's projected
// Kubernetes token, confirms it through the configured discovery endpoint,
// and constructs a validator using the configured Kubernetes JWKS endpoint.
// Neither the token claim nor discovery response selects a network destination.
func NewKubernetesValidator(ctx context.Context, cfg Config, bootstrap KubernetesBootstrapConfig) (*Validator, error) {
	if bootstrap.TokenSource == nil {
		return nil, fmt.Errorf("%w: Kubernetes token source is required", ErrInvalidConfig)
	}
	if bootstrap.DiscoveryURL == "" || bootstrap.JWKSURI == "" {
		return nil, fmt.Errorf("%w: Kubernetes discovery and JWKS URLs are required", ErrInvalidConfig)
	}
	if !cfg.AllowPrivateHTTPSIssuer || cfg.TrustedCAFile == "" || len(cfg.TrustedCAPEM) == 0 {
		return nil, fmt.Errorf("%w: Kubernetes bootstrap requires private HTTPS issuer mode and an explicit trust bundle", ErrInvalidConfig)
	}
	if err := validateKubernetesEndpointOrigin(bootstrap.DiscoveryURL, bootstrap.JWKSURI); err != nil {
		return nil, fmt.Errorf("%w: Kubernetes bootstrap endpoints: %v", ErrInvalidConfig, err)
	}
	client, err := scopedhttps.NewSingleIssuerClient(ctx, []string{bootstrap.DiscoveryURL, bootstrap.JWKSURI}, cfg.TrustedCAPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: Kubernetes bootstrap transport: %v", ErrInvalidConfig, err)
	}
	client.Transport = kubernetesBearerTransport{next: client.Transport, source: bootstrap.TokenSource, endpoints: []string{bootstrap.DiscoveryURL, bootstrap.JWKSURI}}

	token, err := readKubernetesToken(bootstrap.TokenSource)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	candidate, err := issuerFromJWT(token)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	discovered, err := fetchDiscovery(ctx, client, bootstrap.DiscoveryURL)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	if discovered.Issuer != candidate {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("%w: Kubernetes discovery does not match configured identity endpoints", ErrInvalidConfig)
	}
	if err := validateHTTPSURL(discovered.JWKSURI); err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("%w: Kubernetes discovery jwks_uri: %v", ErrInvalidConfig, err)
	}
	cfg.Issuer, cfg.JWKSURI, cfg.HTTPClient, cfg.kubernetesBootstrap = candidate, bootstrap.JWKSURI, client, true
	validator, err := NewValidator(ctx, cfg)
	if err != nil {
		client.CloseIdleConnections()
	}
	return validator, err
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

func validateHTTPSURL(raw string) error {
	_, err := parseHTTPSURL(raw)
	return err
}

func parseHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
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

type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

func fetchDiscovery(ctx context.Context, client *http.Client, endpoint string) (discoveryDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return discoveryDocument{}, fmt.Errorf("%w: Kubernetes discovery request", ErrInvalidConfig)
	}
	response, err := client.Do(req)
	if err != nil {
		return discoveryDocument{}, fmt.Errorf("%w: Kubernetes discovery unavailable", ErrInvalidConfig)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return discoveryDocument{}, fmt.Errorf("%w: Kubernetes discovery rejected", ErrInvalidConfig)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return discoveryDocument{}, fmt.Errorf("%w: Kubernetes discovery response", ErrInvalidConfig)
	}
	var document discoveryDocument
	if json.Unmarshal(body, &document) != nil || document.Issuer == "" || document.JWKSURI == "" {
		return discoveryDocument{}, fmt.Errorf("%w: Kubernetes discovery response", ErrInvalidConfig)
	}
	return document, nil
}

func readKubernetesToken(source func() ([]byte, error)) (string, error) {
	data, err := source()
	if err != nil || len(data) > maxKubernetesTokenBytes {
		return "", fmt.Errorf("%w: read projected Kubernetes token", ErrInvalidConfig)
	}
	token := strings.TrimSpace(string(data))
	if token == "" || !isCompactJWT(token) {
		return "", fmt.Errorf("%w: read projected Kubernetes token", ErrInvalidConfig)
	}
	return token, nil
}

func isCompactJWT(token string) bool {
	if strings.Count(token, ".") != 2 {
		return false
	}
	for _, r := range token {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func issuerFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: projected Kubernetes token is malformed", ErrInvalidConfig)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(claims) > maxKubernetesTokenBytes {
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

type kubernetesBearerTransport struct {
	next      http.RoundTripper
	source    func() ([]byte, error)
	endpoints []string
}

func (t kubernetesBearerTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t kubernetesBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.Method != http.MethodGet || !isApprovedKubernetesEndpoint(req.URL, t.endpoints) {
		return nil, errors.New("kubernetes credential request is not an approved GET endpoint")
	}
	token, err := readKubernetesToken(t.source)
	if err != nil {
		return nil, errors.New("kubernetes credential unavailable")
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.next.RoundTrip(clone)
}

func isApprovedKubernetesEndpoint(requestURL *url.URL, endpoints []string) bool {
	if requestURL == nil || requestURL.User != nil || requestURL.Fragment != "" {
		return false
	}
	for _, raw := range endpoints {
		endpoint, err := url.Parse(raw)
		if err == nil && endpoint.Scheme == requestURL.Scheme && strings.EqualFold(endpoint.Host, requestURL.Host) && endpoint.Path == requestURL.Path && endpoint.RawQuery == requestURL.RawQuery {
			return true
		}
	}
	return false
}
