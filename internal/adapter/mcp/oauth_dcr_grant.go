package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"sort"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

const (
	oauthDCRGrantVersion   = 2
	oauthDCRGrantKeyDomain = "mecatl/mcp/oauth-dcr-credential-key/v1"
)

type oauthDCRTokenEnvelope struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Expiry      string `json:"expiry,omitempty"`
}

type oauthDCRAuthorizationEnvelope struct {
	TokenURL    string   `json:"token_url"`
	AuthStyle   int      `json:"auth_style"`
	RedirectURL string   `json:"redirect_url"`
	Scopes      []string `json:"scopes"`
}

type oauthDCRGrantEnvelope struct {
	Schema                 string                         `json:"schema"`
	Version                int                            `json:"version"`
	Identity               oauthCredentialIdentity        `json:"identity"`
	RegistrationGeneration string                         `json:"registration_generation"`
	State                  string                         `json:"state"`
	Token                  *oauthDCRTokenEnvelope         `json:"token,omitempty"`
	Authorization          *oauthDCRAuthorizationEnvelope `json:"authorization,omitempty"`
}

func oauthDCRCredentialKey(identity oauthCredentialIdentity, generation string) ([]byte, error) {
	if err := validateOAuthIdentity(identity); err != nil || identity.ClientKind != oauthDCRClientKind || !validDCRRandom(generation) {
		return nil, errors.New("OAuth DCR credential identity is invalid")
	}
	fields := []string{identity.Profile, identity.Principal, identity.Resource, identity.Issuer, oauthDCRClientKind, identity.ClientID, generation}
	framed := []byte(oauthDCRGrantKeyDomain)
	var size [4]byte
	for _, field := range fields {
		if len(field) > math.MaxUint32 {
			return nil, errors.New("OAuth DCR credential identity field is too large")
		}
		binary.BigEndian.PutUint32(size[:], uint32(len(field))) // #nosec G115 -- checked above.
		framed = append(framed, size[:]...)
		framed = append(framed, field...)
	}
	digest := sha256.Sum256(framed)
	return digest[:], nil
}

func newOAuthDCRResetGrant(identity oauthCredentialIdentity, generation string) oauthDCRGrantEnvelope {
	return oauthDCRGrantEnvelope{Schema: oauthCredentialSchema, Version: oauthDCRGrantVersion, Identity: identity, RegistrationGeneration: generation, State: "reset"}
}

func newOAuthDCRActiveGrant(identity oauthCredentialIdentity, generation string, cfg *oauth2.Config, token *oauth2.Token) oauthDCRGrantEnvelope {
	expiry := ""
	if !token.Expiry.IsZero() {
		expiry = token.Expiry.UTC().Format(time.RFC3339Nano)
	}
	return oauthDCRGrantEnvelope{
		Schema: oauthCredentialSchema, Version: oauthDCRGrantVersion, Identity: identity, RegistrationGeneration: generation, State: "active",
		Token:         &oauthDCRTokenEnvelope{AccessToken: token.AccessToken, TokenType: token.TokenType, Expiry: expiry},
		Authorization: &oauthDCRAuthorizationEnvelope{TokenURL: cfg.Endpoint.TokenURL, AuthStyle: int(oauth2.AuthStyleInParams), RedirectURL: cfg.RedirectURL, Scopes: []string{oauthDCRScope}},
	}
}

func encodeOAuthDCRGrant(grant oauthDCRGrantEnvelope, expected oauthCredentialIdentity, generation string, origins map[string]struct{}) ([]byte, error) {
	if err := validateOAuthDCRGrant(grant, expected, generation, origins); err != nil {
		return nil, err
	}
	value, err := json.Marshal(grant)
	if err != nil || len(value) > credentialstore.MaxValueBytes {
		return nil, errors.New("OAuth DCR grant is invalid")
	}
	return value, nil
}

func decodeOAuthDCRGrant(value []byte, expected oauthCredentialIdentity, generation string, origins map[string]struct{}) (oauthDCRGrantEnvelope, error) {
	if len(value) == 0 || len(value) > credentialstore.MaxValueBytes || !uniqueDCRJSONKeys(value) {
		return oauthDCRGrantEnvelope{}, errors.New("OAuth DCR grant is invalid")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(value), credentialstore.MaxValueBytes+1))
	decoder.DisallowUnknownFields()
	var grant oauthDCRGrantEnvelope
	if err := decoder.Decode(&grant); err != nil {
		return oauthDCRGrantEnvelope{}, errors.New("OAuth DCR grant is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return oauthDCRGrantEnvelope{}, errors.New("OAuth DCR grant has trailing data")
	}
	if err := validateOAuthDCRGrant(grant, expected, generation, origins); err != nil {
		return oauthDCRGrantEnvelope{}, err
	}
	return grant, nil
}

func validateOAuthDCRGrant(grant oauthDCRGrantEnvelope, expected oauthCredentialIdentity, generation string, origins map[string]struct{}) error { //nolint:gocyclo // strict persisted-state validation is intentionally linear.
	if grant.Schema != oauthCredentialSchema || grant.Version != oauthDCRGrantVersion || grant.Identity != expected || grant.RegistrationGeneration != generation || !validDCRRandom(generation) {
		return errors.New("OAuth DCR grant identity is invalid")
	}
	switch grant.State {
	case "reset":
		if grant.Token != nil || grant.Authorization != nil {
			return errors.New("OAuth DCR reset grant contains credential data")
		}
		return nil
	case "active":
		if grant.Token == nil || grant.Authorization == nil {
			return errors.New("OAuth DCR active grant is incomplete")
		}
	default:
		return errors.New("OAuth DCR grant state is invalid")
	}
	if validateSafeValue("OAuth DCR access token", grant.Token.AccessToken) != nil || validateSafeValue("OAuth DCR token type", grant.Token.TokenType) != nil {
		return errors.New("OAuth DCR token is invalid")
	}
	if grant.Token.Expiry != "" {
		if _, err := time.Parse(time.RFC3339Nano, grant.Token.Expiry); err != nil {
			return errors.New("OAuth DCR token expiry is invalid")
		}
	}
	auth := grant.Authorization
	tokenURL, err := validateHTTPURL("OAuth DCR token URL", auth.TokenURL, false)
	if err != nil || tokenURL.RawQuery != "" || tokenURL.Fragment != "" {
		return errors.New("OAuth DCR token authorization is invalid")
	}
	if _, ok := origins[urlOrigin(tokenURL)]; !ok || oauth2.AuthStyle(auth.AuthStyle) != oauth2.AuthStyleInParams {
		return errors.New("OAuth DCR token authorization is invalid")
	}
	redirect, err := url.Parse(auth.RedirectURL)
	if err != nil || redirect.Scheme != oauthHTTPURLScheme || redirect.User != nil || redirect.Hostname() != "127.0.0.1" || !validDCRRedirectPort(redirect.Port()) || !validDCRCallbackPath(redirect.Path) || redirect.RawQuery != "" || redirect.Fragment != "" {
		return errors.New("OAuth DCR redirect is invalid")
	}
	if !sort.StringsAreSorted(auth.Scopes) || len(auth.Scopes) != 1 || auth.Scopes[0] != oauthDCRScope {
		return errors.New("OAuth DCR scopes are invalid")
	}
	return nil
}

func oauthDCRGrantToken(grant oauthDCRGrantEnvelope) (*oauth2.Token, error) {
	if grant.State != "active" || grant.Token == nil {
		return nil, ErrOAuthLoginRequired
	}
	var expiry time.Time
	var err error
	if grant.Token.Expiry != "" {
		expiry, err = time.Parse(time.RFC3339Nano, grant.Token.Expiry)
		if err != nil {
			return nil, errors.New("OAuth DCR token expiry is invalid")
		}
	}
	return &oauth2.Token{AccessToken: grant.Token.AccessToken, TokenType: grant.Token.TokenType, Expiry: expiry}, nil
}
