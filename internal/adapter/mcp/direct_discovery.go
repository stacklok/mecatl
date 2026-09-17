package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxDirectDiscoveryBody = 1 << 20

// ErrDirectPathMetadataFallbackUnsupported means a pathful protected resource
// did not expose its RFC 9728 endpoint. Falling back to an origin-wide document
// would weaken the resource binding, so it is deliberately not attempted.
var ErrDirectPathMetadataFallbackUnsupported = errors.New("MCP pathful resource metadata fallback is unsupported")

// DirectIssuerDiscovery is the validated, credential-free discovery result.
type DirectIssuerDiscovery struct {
	Issuer                                     string
	AuthorizationEndpoint                      string
	TokenEndpoint                              string
	RegistrationEndpoint                       string
	AuthorizationResponseIssParameterSupported bool
}

// DiscoverDirectIssuer discovers the OAuth authorization server for one exact
// HTTPS MCP resource. It sends no credentials and performs no registration.
func DiscoverDirectIssuer(ctx context.Context, resource string) (DirectIssuerDiscovery, error) {
	canonical, err := exactDirectResource(resource)
	if err != nil {
		return DirectIssuerDiscovery{}, errors.New("MCP resource URL must be exact query-free HTTPS")
	}
	resourceURL, _ := url.Parse(canonical)
	client, err := newDirectDiscoveryClient(canonical)
	if err != nil {
		return DirectIssuerDiscovery{}, errors.New("MCP discovery transport is unavailable")
	}
	defer client.CloseIdleConnections()
	probeClient := *client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := directGet(ctx, &probeClient, canonical)
	if err != nil {
		return DirectIssuerDiscovery{}, errors.New("MCP protected-resource discovery failed")
	}
	metadata, err := directResourceMetadata(response.Header.Values("WWW-Authenticate"))
	_ = response.Body.Close()
	if err != nil {
		return DirectIssuerDiscovery{}, err
	}

	metadataURL := metadata
	if metadataURL != "" {
		u, parseErr := exactDirectURL(metadataURL)
		if parseErr != nil || urlOrigin(u) != urlOrigin(resourceURL) {
			return DirectIssuerDiscovery{}, errors.New("MCP resource_metadata must be a query-free HTTPS URL on the MCP resource origin")
		}
	} else {
		metadataURL = directEndpointMetadataURL(resourceURL)
	}
	document, status, err := directProtectedResource(ctx, client, metadataURL, canonical)
	if err != nil {
		return DirectIssuerDiscovery{}, err
	}
	if status == http.StatusNotFound && metadata == "" {
		if resourceURL.EscapedPath() != "/" {
			return DirectIssuerDiscovery{}, ErrDirectPathMetadataFallbackUnsupported
		}
		fallback := urlOrigin(resourceURL) + "/.well-known/oauth-protected-resource"
		if fallback != metadataURL {
			document, status, err = directProtectedResource(ctx, client, fallback, canonical)
			if err != nil {
				return DirectIssuerDiscovery{}, err
			}
		}
	}
	if status != http.StatusOK {
		return DirectIssuerDiscovery{}, errors.New("MCP resource metadata was not available")
	}
	issuer, err := exactDirectURL(document.AuthorizationServers[0])
	if err != nil {
		return DirectIssuerDiscovery{}, errors.New("MCP resource metadata advertised an invalid issuer")
	}
	issuerClient, err := newDirectDiscoveryClient(issuer.String())
	if err != nil {
		return DirectIssuerDiscovery{}, errors.New("MCP issuer discovery transport is unavailable")
	}
	defer issuerClient.CloseIdleConnections()
	return directIssuerMetadata(ctx, issuerClient, issuer.String())
}

func exactDirectResource(raw string) (string, error) {
	canonical, err := canonicalOAuthResource(raw)
	if err != nil || raw != canonical {
		return "", errors.New("not exact")
	}
	u, _ := url.Parse(canonical)
	if u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("not HTTPS")
	}
	return canonical, nil
}

func exactDirectURL(raw string) (*url.URL, error) {
	u, err := validateHTTPURL("MCP discovery URL", raw, true)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || raw != u.String() {
		return nil, errors.New("not exact")
	}
	return u, nil
}

func directEndpointMetadataURL(resource *url.URL) string {
	return urlOrigin(resource) + "/.well-known/oauth-protected-resource" + resource.EscapedPath()
}

func newDirectDiscoveryClient(origin string) (*http.Client, error) {
	client, _, err := newOAuthHTTPClient(origin, OAuthOptions{Issuer: origin, Network: OAuthNetworkPolicy{MaxRedirects: maxOAuthRedirects}})
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = oauthRedirectPolicy(maxOAuthRedirects)
	return client, nil
}

func directGet(ctx context.Context, client *http.Client, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

type directPRM struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

func directProtectedResource(ctx context.Context, client *http.Client, target, resource string) (directPRM, int, error) {
	response, err := directGet(ctx, client, target)
	if err != nil {
		return directPRM{}, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return directPRM{}, response.StatusCode, nil
	}
	if !directJSONContentType(response.Header.Get("Content-Type")) {
		return directPRM{}, response.StatusCode, errors.New("MCP resource metadata must be JSON")
	}
	var document directPRM
	if err := directDecodeJSON(response.Body, &document); err != nil || document.Resource != resource || len(document.AuthorizationServers) != 1 {
		return directPRM{}, response.StatusCode, errors.New("MCP resource metadata must bind the resource and advertise exactly one issuer")
	}
	return document, response.StatusCode, nil
}

func directJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func directDecodeJSON(body io.Reader, target any) error {
	contents, err := io.ReadAll(io.LimitReader(body, maxDirectDiscoveryBody+1))
	if err != nil {
		return err
	}
	if len(contents) > maxDirectDiscoveryBody {
		return errors.New("MCP discovery response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func directIssuerMetadata(ctx context.Context, client *http.Client, issuer string) (DirectIssuerDiscovery, error) {
	u, _ := url.Parse(issuer)
	base := urlOrigin(u) + "/.well-known/oauth-authorization-server"
	if u.EscapedPath() != "" && u.EscapedPath() != "/" {
		base += u.EscapedPath()
	}
	oidcPath := urlOrigin(u) + "/.well-known/openid-configuration"
	appendOIDC := ""
	if u.EscapedPath() != "" && u.EscapedPath() != "/" {
		oidcPath += u.EscapedPath()
		appendOIDC = issuer + "/.well-known/openid-configuration"
	}
	candidates := []string{base, oidcPath}
	if appendOIDC != "" {
		candidates = append(candidates, appendOIDC)
	}
	for _, target := range candidates {
		response, err := directGet(ctx, client, target)
		if err != nil {
			return DirectIssuerDiscovery{}, errors.New("MCP issuer metadata discovery failed")
		}
		if response.StatusCode == http.StatusNotFound {
			_ = response.Body.Close()
			continue
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return DirectIssuerDiscovery{}, errors.New("MCP issuer metadata was not available")
		}
		if !directJSONContentType(response.Header.Get("Content-Type")) {
			_ = response.Body.Close()
			return DirectIssuerDiscovery{}, errors.New("MCP issuer metadata must be JSON")
		}
		var metadata struct {
			Issuer                                     string `json:"issuer"`
			AuthorizationEndpoint                      string `json:"authorization_endpoint"`
			TokenEndpoint                              string `json:"token_endpoint"`
			RegistrationEndpoint                       string `json:"registration_endpoint"`
			AuthorizationResponseIssParameterSupported bool   `json:"authorization_response_iss_parameter_supported"`
		}
		decodeErr := directDecodeJSON(response.Body, &metadata)
		_ = response.Body.Close()
		if decodeErr != nil || metadata.Issuer != issuer {
			return DirectIssuerDiscovery{}, errors.New("MCP issuer metadata is invalid")
		}
		for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RegistrationEndpoint} {
			endpointURL, endpointErr := exactDirectURL(endpoint)
			if endpointErr != nil || urlOrigin(endpointURL) != urlOrigin(u) {
				return DirectIssuerDiscovery{}, errors.New("MCP issuer endpoint is invalid")
			}
		}
		return DirectIssuerDiscovery{Issuer: issuer, AuthorizationEndpoint: metadata.AuthorizationEndpoint, TokenEndpoint: metadata.TokenEndpoint, RegistrationEndpoint: metadata.RegistrationEndpoint, AuthorizationResponseIssParameterSupported: metadata.AuthorizationResponseIssParameterSupported}, nil
	}
	return DirectIssuerDiscovery{}, errors.New("MCP issuer metadata was not available")
}

// directResourceMetadata selects a complete Bearer challenge. resource_metadata
// and scope are compared as one value, so no parameter is inherited from a
// separate challenge.
func directResourceMetadata(values []string) (string, error) {
	var selected directChallenge
	selectedSet := false
	for _, value := range values {
		challenges, err := parseDirectChallenges(value)
		if err != nil {
			return "", errors.New("MCP WWW-Authenticate header is invalid")
		}
		for _, challenge := range challenges {
			if !strings.EqualFold(challenge.scheme, "Bearer") {
				continue
			}
			metadata, hasMetadata := challenge.params["resource_metadata"]
			_, hasScope := challenge.params["scope"]
			// A scope-bearing challenge is one atomic resource declaration. A
			// scope from one challenge must never be combined with metadata from
			// another, and a scope without metadata is ambiguous.
			if hasScope && !hasMetadata {
				return "", errors.New("MCP Bearer scope requires resource_metadata in the same challenge")
			}
			if !hasMetadata {
				continue
			}
			if selectedSet && (selected.params["resource_metadata"] != metadata || selected.params["scope"] != challenge.params["scope"]) {
				return "", errors.New("MCP Bearer challenges advertise conflicting resource_metadata or scope")
			}
			selected = challenge
			selectedSet = true
		}
	}
	if !selectedSet {
		return "", nil
	}
	return selected.params["resource_metadata"], nil
}

type directChallenge struct {
	scheme string
	params map[string]string
}

func parseDirectChallenges(s string) ([]directChallenge, error) {
	var out []directChallenge
	i := 0
	for {
		skipDirectOWS(s, &i)
		if i == len(s) {
			return out, nil
		}
		scheme, ok := directToken(s, &i)
		if !ok {
			return nil, errors.New("scheme")
		}
		c := directChallenge{scheme: scheme, params: map[string]string{}}
		skipDirectOWS(s, &i)
		for i < len(s) && s[i] != ',' {
			key, valid := directToken(s, &i)
			if !valid {
				return nil, errors.New("parameter")
			}
			skipDirectOWS(s, &i)
			if i == len(s) || s[i] != '=' {
				return nil, errors.New("token68 unsupported")
			}
			i++
			skipDirectOWS(s, &i)
			value, quoted, valid := directValue(s, &i)
			if !valid {
				return nil, errors.New("value")
			}
			key = strings.ToLower(key)
			if key == "resource_metadata" && !quoted {
				return nil, errors.New("resource metadata value")
			}
			if _, exists := c.params[key]; exists {
				return nil, errors.New("duplicate")
			}
			c.params[key] = value
			skipDirectOWS(s, &i)
			if i == len(s) {
				break
			}
			if s[i] != ',' {
				return nil, errors.New("separator")
			}
			comma := i
			i++
			skipDirectOWS(s, &i)
			look := i
			_, tokenOK := directToken(s, &look)
			skipDirectOWS(s, &look)
			if !tokenOK {
				return nil, errors.New("separator")
			}
			if look == len(s) || s[look] != '=' {
				i = comma
				break
			}
		}
		out = append(out, c)
		skipDirectOWS(s, &i)
		if i == len(s) {
			return out, nil
		}
		if s[i] != ',' {
			return nil, errors.New("separator")
		}
		i++
	}
}
func skipDirectOWS(s string, i *int) {
	for *i < len(s) && (s[*i] == ' ' || s[*i] == '\t') {
		*i++
	}
}
func directToken(s string, i *int) (string, bool) {
	start := *i
	for *i < len(s) && strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", rune(s[*i])) {
		*i++
	}
	return s[start:*i], *i > start
}
func directValue(s string, i *int) (string, bool, bool) {
	if *i >= len(s) {
		return "", false, false
	}
	if s[*i] != '"' {
		value, ok := directToken(s, i)
		return value, false, ok
	}
	*i++
	var b strings.Builder
	for *i < len(s) {
		ch := s[*i]
		*i++
		if ch == '"' {
			return b.String(), true, true
		}
		if ch == '\\' {
			if *i == len(s) || (s[*i] != '\\' && s[*i] != '"') {
				return "", true, false
			}
			ch = s[*i]
			*i++
		}
		if ch < 0x20 || ch == 0x7f {
			return "", true, false
		}
		b.WriteByte(ch)
	}
	return "", true, false
}
