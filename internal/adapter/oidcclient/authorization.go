// Package oidcclient contains target-independent host-side OIDC public-client mechanics.
package oidcclient

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	authoidc "github.com/stacklok/mecatl/authn/oidc"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var (
	// ErrDiscovery reports rejected or unavailable exact-issuer metadata.
	ErrDiscovery = errors.New("oidcclient: discovery rejected")
	// ErrAuthorization reports a rejected browser callback or invalid client configuration.
	ErrAuthorization = errors.New("oidcclient: authorization rejected")
	// ErrToken reports an exchange or access-token profile rejection.
	ErrToken = errors.New("oidcclient: token rejected")
	// ErrStorage reports unavailable or corrupt protected local key material.
	ErrStorage = errors.New("oidcclient: protected storage unavailable")
)

// Config describes one exact-issuer public-client authorization-code exchange.
type Config struct {
	Issuer, ClientID, Audience, RedirectURI string
	Scopes                                  []string
	HTTPClient                              *http.Client
	Present                                 func(context.Context, string) (oauthlogin.Result, error)
	ValidateAccessToken                     func(context.Context, string) error
}

// Token is the transient result of an authorization-code or refresh exchange.
type Token struct {
	AccessToken, RefreshToken, TokenType string
	Expiry                               time.Time
}

type discovery struct {
	Issuer                   string   `json:"issuer"`
	AuthorizationEndpoint    string   `json:"authorization_endpoint"`
	TokenEndpoint            string   `json:"token_endpoint"`
	JWKSURI                  string   `json:"jwks_uri"`
	RevocationEndpoint       string   `json:"revocation_endpoint"`
	CodeChallengeMethods     []string `json:"code_challenge_methods_supported"`
	IssuerParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// AuthorizationCode performs exact-issuer discovery and one PKCE-S256 exchange.
func AuthorizationCode(ctx context.Context, cfg Config) (Token, error) {
	if !validConfig(ctx, cfg) {
		return Token{}, ErrAuthorization
	}
	doc, err := discover(ctx, cfg.HTTPClient, cfg.Issuer)
	if err != nil {
		return Token{}, ErrDiscovery
	}
	if doc.Issuer != cfg.Issuer || !slices.Contains(doc.CodeChallengeMethods, "S256") || !discoveryEndpointsConfined(cfg.Issuer, doc) {
		return Token{}, ErrDiscovery
	}
	validate, closeValidator, err := accessValidator(ctx, cfg, doc)
	if err != nil {
		return Token{}, ErrDiscovery
	}
	defer closeValidator()
	verifier, err := randomValue(32)
	if err != nil {
		return Token{}, ErrAuthorization
	}
	state, err := randomValue(32)
	if err != nil {
		return Token{}, ErrAuthorization
	}
	oc := oauth2.Config{ClientID: cfg.ClientID, RedirectURL: cfg.RedirectURI, Endpoint: oauth2.Endpoint{AuthURL: doc.AuthorizationEndpoint, TokenURL: doc.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams}, Scopes: append([]string(nil), cfg.Scopes...)}
	result, err := cfg.Present(ctx, oc.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier)))
	if err != nil {
		return Token{}, errors.Join(ErrAuthorization, safeContextError(err))
	}
	if result.State != state || result.Code == "" {
		return Token{}, ErrAuthorization
	}
	if result.Iss == "" {
		if doc.IssuerParameterSupported {
			return Token{}, ErrAuthorization
		}
	} else if result.Iss != cfg.Issuer {
		return Token{}, ErrAuthorization
	}
	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, cfg.HTTPClient)
	tok, err := oc.Exchange(exchangeCtx, result.Code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Token{}, ErrToken
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.TokenType != "Bearer" {
		return Token{}, ErrToken
	}
	if err := validate(ctx, tok.AccessToken); err != nil {
		return Token{}, ErrToken
	}
	return Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}, nil
}

// Refresh exchanges one retained refresh token through exact-issuer discovery.
// Provider-controlled detail is collapsed; invalid_grant alone remains a
// body-free structured RetrieveError so exact-version cleanup can recognize it.
func Refresh(ctx context.Context, cfg Config, refreshToken string) (Token, error) {
	if refreshToken == "" || cfg.HTTPClient == nil || !secureIssuer(cfg.Issuer) || cfg.ClientID == "" {
		return Token{}, ErrToken
	}
	doc, err := discover(ctx, cfg.HTTPClient, cfg.Issuer)
	if err != nil || doc.Issuer != cfg.Issuer || !discoveryEndpointsConfined(cfg.Issuer, doc) {
		return Token{}, ErrDiscovery
	}
	oc := oauth2.Config{ClientID: cfg.ClientID, Endpoint: oauth2.Endpoint{TokenURL: doc.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams}, Scopes: append([]string(nil), cfg.Scopes...)}
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, cfg.HTTPClient)
	tok, err := oc.TokenSource(tokenCtx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.ErrorCode == "invalid_grant" {
			return Token{}, &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
		}
		return Token{}, ErrToken
	}
	if tok.AccessToken == "" || tok.TokenType != "Bearer" {
		return Token{}, ErrToken
	}
	return Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}, nil
}

// ValidateAccessToken validates a refreshed access token against exact-issuer
// metadata. Callers that durably retain refresh-token rotation must invoke this
// only after committing the exchange result.
func ValidateAccessToken(ctx context.Context, cfg Config, token string) error {
	if token == "" || cfg.HTTPClient == nil || !secureIssuer(cfg.Issuer) || cfg.Audience == "" {
		return ErrToken
	}
	doc, err := discover(ctx, cfg.HTTPClient, cfg.Issuer)
	if err != nil || doc.Issuer != cfg.Issuer || !discoveryEndpointsConfined(cfg.Issuer, doc) {
		return ErrDiscovery
	}
	validate, closeValidator, err := accessValidator(ctx, cfg, doc)
	if err != nil {
		return ErrDiscovery
	}
	defer closeValidator()
	if err := validate(ctx, token); err != nil {
		return ErrToken
	}
	return nil
}

// Revoke makes one RFC 7009 request. It never includes provider response text in
// its returned error.
func Revoke(ctx context.Context, cfg Config, token, hint string) error {
	if token == "" || cfg.HTTPClient == nil || !secureIssuer(cfg.Issuer) || cfg.ClientID == "" {
		return ErrToken
	}
	doc, err := discover(ctx, cfg.HTTPClient, cfg.Issuer)
	if err != nil || doc.Issuer != cfg.Issuer || !discoveryEndpointsConfined(cfg.Issuer, doc) || doc.RevocationEndpoint == "" {
		return ErrDiscovery
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}, "client_id": {cfg.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return ErrToken
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrToken
	}
	return nil
}

func validConfig(ctx context.Context, cfg Config) bool {
	return ctx != nil && cfg.HTTPClient != nil && cfg.Present != nil &&
		secureIssuer(cfg.Issuer) && cfg.ClientID != "" && cfg.Audience != "" &&
		cfg.RedirectURI == oauthlogin.ExactRedirectURL && len(cfg.Scopes) > 0
}

func accessValidator(ctx context.Context, cfg Config, doc discovery) (func(context.Context, string) error, func(), error) {
	if cfg.ValidateAccessToken != nil {
		return cfg.ValidateAccessToken, func() {}, nil
	}
	validator, err := authoidc.NewValidator(ctx, authoidc.Config{Issuer: cfg.Issuer, JWKSURI: doc.JWKSURI, Audience: cfg.Audience, HTTPClient: cfg.HTTPClient})
	if err != nil {
		return nil, nil, err
	}
	return func(ctx context.Context, token string) error {
		_, err := validator.Validate(ctx, token)
		return err
	}, func() { _ = validator.Close() }, nil
}

func discover(ctx context.Context, client *http.Client, issuer string) (discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return discovery{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return discovery{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return discovery{}, ErrDiscovery
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return discovery{}, ErrDiscovery
	}
	var doc discovery
	if json.Unmarshal(body, &doc) != nil || !secureEndpoint(doc.AuthorizationEndpoint) || !secureEndpoint(doc.TokenEndpoint) || !secureEndpoint(doc.JWKSURI) {
		return discovery{}, ErrDiscovery
	}
	return doc, nil
}

func discoveryEndpointsConfined(issuer string, doc discovery) bool {
	for _, endpoint := range []string{doc.AuthorizationEndpoint, doc.TokenEndpoint, doc.JWKSURI} {
		if !sameOriginEndpoint(issuer, endpoint) {
			return false
		}
	}
	return doc.RevocationEndpoint == "" || sameOriginEndpoint(issuer, doc.RevocationEndpoint)
}

func sameOriginEndpoint(issuer, endpoint string) bool {
	base, baseErr := url.Parse(issuer)
	target, targetErr := url.Parse(endpoint)
	if baseErr != nil || targetErr != nil || !secureURL(base) || !secureURL(target) || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" {
		return false
	}
	return strings.EqualFold(base.Scheme, target.Scheme) && strings.EqualFold(base.Hostname(), target.Hostname()) && effectivePort(base) == effectivePort(target)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func secureIssuer(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && secureURL(u) && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func secureEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && secureURL(u) && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func secureURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

func randomValue(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func safeContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrAuthorization
}
