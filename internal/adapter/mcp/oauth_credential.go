package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

const (
	oauthCredentialSchema    = "mecatl.mcp.oauth-credential" // #nosec G101 -- public envelope discriminator, not a credential.
	oauthCredentialVersion   = 1
	oauthCredentialKeyDomain = "mecatl/mcp/oauth-credential-key/v1" // #nosec G101 -- public hash domain, not a credential.
)

type oauthCredentialIdentity struct {
	Profile    string `json:"profile"`
	Principal  string `json:"principal"`
	Resource   string `json:"resource"`
	Issuer     string `json:"issuer"`
	ClientKind string `json:"client_kind"`
	ClientID   string `json:"client_id"`
}

type oauthTokenEnvelope struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Expiry       string `json:"expiry,omitempty"`
}

type oauthRefreshEnvelope struct {
	TokenURL    string   `json:"token_url"`
	AuthStyle   int      `json:"auth_style"`
	RedirectURL string   `json:"redirect_url"`
	Scopes      []string `json:"scopes"`
}

type oauthCredentialEnvelope struct {
	Schema   string                  `json:"schema"`
	Version  int                     `json:"version"`
	Identity oauthCredentialIdentity `json:"identity"`
	Token    oauthTokenEnvelope      `json:"token"`
	Refresh  oauthRefreshEnvelope    `json:"refresh"`
}

func canonicalOAuthResource(raw string) (string, error) {
	u, err := validateHTTPURL("OAuth resource", raw, false)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	hostname := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	}
	port := u.Port()
	if port == "" || scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(strings.Trim(hostname, "[]"), port)
	}
	escaped := u.EscapedPath()
	if escaped == "" {
		escaped = "/"
	}
	escaped = removeEscapedDotSegments(escaped)
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", errors.New("OAuth resource path is invalid")
	}
	clean := &url.URL{Scheme: scheme, Host: host, Path: decoded, RawPath: escaped, RawQuery: u.RawQuery, ForceQuery: u.ForceQuery}
	return clean.String(), nil
}

func removeEscapedDotSegments(path string) string {
	var output string
	for path != "" {
		switch {
		case strings.HasPrefix(path, "../"):
			path = path[3:]
		case strings.HasPrefix(path, "./"):
			path = path[2:]
		case strings.HasPrefix(path, "/./"):
			path = path[2:]
		case path == "/.":
			path = "/"
		case strings.HasPrefix(path, "/../"):
			path = path[3:]
			output = removeLastPathSegment(output)
		case path == "/..":
			path = "/"
			output = removeLastPathSegment(output)
		case path == "." || path == "..":
			path = ""
		default:
			end := strings.IndexByte(path[1:], '/')
			if path[0] != '/' {
				end = strings.IndexByte(path, '/')
			}
			if end < 0 {
				output += path
				path = ""
				continue
			}
			if path[0] == '/' {
				end++
			}
			output += path[:end]
			path = path[end:]
		}
	}
	return output
}

func removeLastPathSegment(path string) string {
	if end := strings.LastIndexByte(path, '/'); end >= 0 {
		return path[:end]
	}
	return ""
}

func validateOAuthIdentity(identity oauthCredentialIdentity) error {
	fields := []struct{ name, value string }{
		{"OAuth profile", identity.Profile}, {"OAuth principal", identity.Principal},
		{"OAuth resource", identity.Resource}, {"OAuth issuer", identity.Issuer},
		{"OAuth client kind", identity.ClientKind}, {"OAuth client ID", identity.ClientID},
	}
	for _, field := range fields {
		if err := validateSafeValue(field.name, field.value); err != nil {
			return err
		}
	}
	if identity.ClientKind != "preregistered" && identity.ClientKind != "cimd" {
		return errors.New("OAuth client kind is unsupported")
	}
	return nil
}

// OAuthCredentialRecordKey derives the opaque persistence key used by the OAuth
// controller for resource and options. Hosts constructing a single-record Reader
// must use this helper rather than duplicating the identity framing protocol.
func OAuthCredentialRecordKey(resource string, opts OAuthOptions) ([]byte, error) {
	if err := validateSafeValue("OAuth subject profile", opts.Subject.Profile); err != nil {
		return nil, err
	}
	if err := validateSafeValue("OAuth subject principal", opts.Subject.Principal); err != nil {
		return nil, err
	}
	canonical, err := canonicalOAuthResource(resource)
	if err != nil {
		return nil, err
	}
	registration, err := validateOAuthRegistration(opts.Client, opts.Issuer)
	if err != nil {
		return nil, err
	}
	return oauthCredentialKey(oauthCredentialIdentity{
		Profile: opts.Subject.Profile, Principal: opts.Subject.Principal,
		Resource: canonical, Issuer: opts.Issuer,
		ClientKind: registration.kind, ClientID: registration.clientID,
	})
}

func oauthCredentialKey(identity oauthCredentialIdentity) ([]byte, error) {
	if err := validateOAuthIdentity(identity); err != nil {
		return nil, err
	}
	fields := []string{identity.Profile, identity.Principal, identity.Resource, identity.Issuer, identity.ClientKind, identity.ClientID}
	framed := make([]byte, 0, len(oauthCredentialKeyDomain)+128)
	framed = append(framed, oauthCredentialKeyDomain...)
	var length [4]byte
	for _, field := range fields {
		if len(field) > math.MaxUint32 {
			return nil, errors.New("OAuth identity field is too large")
		}
		binary.BigEndian.PutUint32(length[:], uint32(len(field))) // #nosec G115 -- checked above.
		framed = append(framed, length[:]...)
		framed = append(framed, field...)
	}
	digest := sha256.Sum256(framed)
	return digest[:], nil
}

func oauthCredentialKeyHex(identity oauthCredentialIdentity) (string, error) {
	key, err := oauthCredentialKey(identity)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func newOAuthCredentialEnvelope(identity oauthCredentialIdentity, cfg *oauth2.Config, token *oauth2.Token) oauthCredentialEnvelope {
	expiry := ""
	if !token.Expiry.IsZero() {
		expiry = token.Expiry.UTC().Format(time.RFC3339Nano)
	}
	scopes := append([]string(nil), cfg.Scopes...)
	sort.Strings(scopes)
	scopes = compactStrings(scopes)
	return oauthCredentialEnvelope{
		Schema: oauthCredentialSchema, Version: oauthCredentialVersion, Identity: identity,
		Token:   oauthTokenEnvelope{AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, Expiry: expiry},
		Refresh: oauthRefreshEnvelope{TokenURL: cfg.Endpoint.TokenURL, AuthStyle: int(cfg.Endpoint.AuthStyle), RedirectURL: cfg.RedirectURL, Scopes: scopes},
	}
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func encodeOAuthCredential(envelope oauthCredentialEnvelope, expected oauthCredentialIdentity, requestRefresh bool, origins map[string]struct{}) ([]byte, error) {
	if err := validateOAuthEnvelope(envelope, expected, requestRefresh, origins); err != nil {
		return nil, err
	}
	value, err := json.Marshal(envelope)
	if err != nil || len(value) > credentialstore.MaxValueBytes {
		return nil, errors.New("OAuth credential envelope is invalid")
	}
	return value, nil
}

func decodeOAuthCredential(value []byte, expected oauthCredentialIdentity, requestRefresh bool, origins map[string]struct{}) (oauthCredentialEnvelope, error) {
	if len(value) > credentialstore.MaxValueBytes {
		return oauthCredentialEnvelope{}, errors.New("OAuth credential envelope is too large")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(value), credentialstore.MaxValueBytes+1))
	decoder.DisallowUnknownFields()
	var envelope oauthCredentialEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return oauthCredentialEnvelope{}, errors.New("OAuth credential envelope is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return oauthCredentialEnvelope{}, errors.New("OAuth credential envelope has trailing data")
	}
	if err := validateOAuthEnvelope(envelope, expected, requestRefresh, origins); err != nil {
		return oauthCredentialEnvelope{}, err
	}
	return envelope, nil
}

func validateOAuthEnvelope(envelope oauthCredentialEnvelope, expected oauthCredentialIdentity, requestRefresh bool, origins map[string]struct{}) error {
	if err := validateOAuthIdentity(expected); err != nil {
		return err
	}
	if envelope.Schema != oauthCredentialSchema || envelope.Version != oauthCredentialVersion {
		return errors.New("OAuth credential envelope schema is unsupported")
	}
	if envelope.Identity != expected {
		return errors.New("OAuth credential identity does not match")
	}
	if err := validateOAuthTokenEnvelope(envelope.Token, requestRefresh); err != nil {
		return err
	}
	return validateOAuthRefreshEnvelope(envelope.Refresh, expected.ClientKind, origins)
}

func validateOAuthTokenEnvelope(token oauthTokenEnvelope, requestRefresh bool) error {
	if err := validateSafeValue("OAuth credential access token", token.AccessToken); err != nil {
		return err
	}
	if err := validateSafeValue("OAuth credential token type", token.TokenType); err != nil {
		return err
	}
	if token.RefreshToken != "" {
		if err := validateSafeValue("OAuth credential refresh token", token.RefreshToken); err != nil {
			return err
		}
	}
	if requestRefresh && token.RefreshToken == "" {
		return errors.New("OAuth credential refresh token is required")
	}
	if token.Expiry != "" {
		if _, err := time.Parse(time.RFC3339Nano, token.Expiry); err != nil {
			return errors.New("OAuth credential expiry is invalid")
		}
	}
	return nil
}

func validateOAuthRefreshEnvelope(refresh oauthRefreshEnvelope, clientKind string, origins map[string]struct{}) error {
	tokenURL, err := validateHTTPURL("OAuth token URL", refresh.TokenURL, false)
	if err != nil {
		return err
	}
	if _, ok := origins[urlOrigin(tokenURL)]; !ok {
		return errors.New("OAuth token URL origin is not allowed")
	}
	if _, err := validateHTTPURL("OAuth redirect URL", refresh.RedirectURL, false); err != nil {
		return err
	}
	style := oauth2.AuthStyle(refresh.AuthStyle)
	if clientKind == "preregistered" && style != oauth2.AuthStyleInHeader {
		return errors.New("OAuth credential authentication style is invalid")
	}
	if clientKind == "cimd" && style != oauth2.AuthStyleAutoDetect && style != oauth2.AuthStyleInHeader && style != oauth2.AuthStyleInParams {
		return errors.New("OAuth credential authentication style is invalid")
	}
	if !sort.StringsAreSorted(refresh.Scopes) || len(compactStrings(append([]string(nil), refresh.Scopes...))) != len(refresh.Scopes) {
		return errors.New("OAuth credential scopes are not canonical")
	}
	for _, scope := range refresh.Scopes {
		if err := validateSafeValue("OAuth credential scope", scope); err != nil {
			return err
		}
	}
	return nil
}

func envelopeToken(envelope oauthCredentialEnvelope) (*oauth2.Token, error) {
	var expiry time.Time
	var err error
	if envelope.Token.Expiry != "" {
		expiry, err = time.Parse(time.RFC3339Nano, envelope.Token.Expiry)
		if err != nil {
			return nil, errors.New("OAuth credential expiry is invalid")
		}
	}
	return &oauth2.Token{AccessToken: envelope.Token.AccessToken, TokenType: envelope.Token.TokenType, RefreshToken: envelope.Token.RefreshToken, Expiry: expiry}, nil
}

func envelopeConfig(envelope oauthCredentialEnvelope, registration oauthRegistration) *oauth2.Config {
	return &oauth2.Config{ClientID: registration.clientID, ClientSecret: registration.clientSecret, Endpoint: oauth2.Endpoint{TokenURL: envelope.Refresh.TokenURL, AuthStyle: oauth2.AuthStyle(envelope.Refresh.AuthStyle)}, RedirectURL: envelope.Refresh.RedirectURL, Scopes: append([]string(nil), envelope.Refresh.Scopes...)}
}
