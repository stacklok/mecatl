package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
		metadataURL = urlOrigin(resourceURL) + "/.well-known/oauth-protected-resource" + resourceURL.EscapedPath()
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
		}
	}
	if err != nil || status != http.StatusOK {
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
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return directPRM{}, response.StatusCode, nil
	}
	var document directPRM
	if err := directDecodeJSON(io.LimitReader(response.Body, 1<<20+1), &document); err != nil || document.Resource != resource || len(document.AuthorizationServers) != 1 {
		return directPRM{}, response.StatusCode, errors.New("MCP resource metadata must bind the resource and advertise exactly one issuer")
	}
	return document, response.StatusCode, nil
}

func directDecodeJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
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
	oidc := urlOrigin(u) + "/.well-known/openid-configuration"
	if u.EscapedPath() != "" && u.EscapedPath() != "/" {
		oidc += u.EscapedPath()
	}
	for _, target := range []string{base, oidc} {
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
		var metadata struct {
			Issuer                                     string `json:"issuer"`
			AuthorizationEndpoint                      string `json:"authorization_endpoint"`
			TokenEndpoint                              string `json:"token_endpoint"`
			RegistrationEndpoint                       string `json:"registration_endpoint"`
			AuthorizationResponseIssParameterSupported bool   `json:"authorization_response_iss_parameter_supported"`
		}
		decodeErr := directDecodeJSON(io.LimitReader(response.Body, 1<<20+1), &metadata)
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

// directResourceMetadata parses each RFC 9110 challenge atomically: a parameter
// cannot leak from Basic (or another Bearer challenge) into the selected Bearer one.
func directResourceMetadata(values []string) (string, error) {
	var selected string
	for _, value := range values {
		challenges, err := parseDirectChallenges(value)
		if err != nil {
			return "", errors.New("MCP WWW-Authenticate header is invalid")
		}
		for _, challenge := range challenges {
			if !strings.EqualFold(challenge.scheme, "Bearer") {
				continue
			}
			candidate := challenge.params["resource_metadata"]
			if candidate == "" {
				continue
			}
			if selected != "" && selected != candidate {
				return "", errors.New("MCP Bearer challenges advertise conflicting resource_metadata")
			}
			selected = candidate
		}
	}
	return selected, nil
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
