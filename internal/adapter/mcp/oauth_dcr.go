package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

const (
	oauthDCRRegistrationSchema    = "mecatl.mcp.oauth-dcr-registration"
	oauthDCRRegistrationVersion   = 1
	oauthDCRRegistrationKeyDomain = "mecatl/mcp/oauth-dcr-registration-key/v1"
	oauthDCRRedirectPolicy        = "ipv4-loopback-variable-port/v1"
	oauthDCRCallbackPrefix        = "/oauth/callback/"
	oauthDCRClientKind            = "dcr"
	oauthDCRScope                 = "openid"
	oauthDCRStatePending          = "pending"
	oauthDCRStateReady            = "ready"
	oauthHTTPURLScheme            = "http"
)

// ErrOAuthDCRRecoveryRequired reports durable DCR state that requires an explicit operator recovery action.
var ErrOAuthDCRRecoveryRequired = errors.New("OAuth DCR recovery required")

// ValidateDCRAuthorizationURL checks the final SDK authorization request before
// the host presents it. The SDK may union challenge scopes after ScopeFilter runs.
func ValidateDCRAuthorizationURL(authorizationURL, resource string) error {
	canonical, err := canonicalOAuthResource(resource)
	if err != nil {
		return errors.New("OAuth DCR authorization resource is invalid")
	}
	u, err := url.Parse(authorizationURL)
	if err != nil {
		return errors.New("OAuth DCR authorization URL is invalid")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("OAuth DCR authorization URL is invalid")
	}
	if scopes := query["scope"]; len(scopes) != 1 || scopes[0] != oauthDCRScope {
		return errors.New("OAuth DCR authorization scope is invalid")
	}
	if resources := query["resource"]; len(resources) != 1 || resources[0] != canonical {
		return errors.New("OAuth DCR authorization resource is invalid")
	}
	return nil
}

type oauthDCRIdentity struct {
	Profile   string `json:"profile"`
	Principal string `json:"principal"`
	Resource  string `json:"resource"`
	Issuer    string `json:"issuer"`
}

type oauthDCRMetadata struct {
	Issuer                  string   `json:"issuer"`
	Resource                string   `json:"resource"`
	RedirectPolicy          string   `json:"redirect_policy"`
	RedirectPath            string   `json:"redirect_path"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scopes                  []string `json:"scopes"`
}

type oauthDCRRegistration struct {
	ClientID              string `json:"client_id"`
	RegisteredRedirectURI string `json:"registered_redirect_uri"`
	ClientIDIssuedAt      *int64 `json:"client_id_issued_at,omitempty"`
}

type oauthDCRPreviousAttempt struct {
	Generation       string `json:"generation"`
	AttemptStartedAt string `json:"attempt_started_at"`
	Reason           string `json:"reason"`
}

type oauthDCRRecord struct {
	Schema              string                   `json:"schema"`
	Version             int                      `json:"version"`
	Identity            oauthDCRIdentity         `json:"identity"`
	Generation          string                   `json:"generation"`
	State               string                   `json:"state"`
	AttemptStartedAt    string                   `json:"attempt_started_at"`
	Metadata            oauthDCRMetadata         `json:"metadata"`
	MetadataFingerprint string                   `json:"metadata_fingerprint"`
	PreviousAttempt     *oauthDCRPreviousAttempt `json:"previous_attempt,omitempty"`
	Registration        *oauthDCRRegistration    `json:"registration,omitempty"`
}

type oauthDCRResolved struct {
	issuer     string
	clientID   string
	generation string
	path       string
}

type oauthDCRTicket struct {
	mu                   sync.Mutex
	used                 bool
	key                  []byte
	version              credentialstore.Version
	record               oauthDCRRecord
	registrationEndpoint string
}

func (t *oauthDCRTicket) consume() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.used {
		return false
	}
	t.used = true
	return true
}

// PrepareOAuthDCRLogin validates discovery and prepares one durable registration
// attempt. It never launches a browser or sends the registration request.
func PrepareOAuthDCRLogin(ctx context.Context, resource string, opts OAuthOptions, action OAuthDCRLoginAction) (OAuthOptions, string, error) { //nolint:gocyclo // explicit CAS states and recovery actions stay visible.
	if ctx == nil {
		return OAuthOptions{}, "", errors.New("OAuth DCR preparation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return OAuthOptions{}, "", err
	}
	if action > OAuthDCRLoginRetryRegistration || opts.Client.DCR == nil || opts.Client.Preregistered != nil || opts.Client.ClientIDMetadataDocumentURL != "" || !validDCRRequestedScopes(opts) {
		return OAuthOptions{}, "", errors.New("OAuth DCR login configuration is invalid")
	}
	if opts.CredentialStore == nil || opts.CredentialReader != nil {
		return OAuthOptions{}, "", errors.New("OAuth DCR requires a mutable credential store")
	}
	caps := opts.CredentialStore.Capabilities()
	if !opts.allowLoopbackForTest && (!caps.Persistent || !caps.CrossProcessCAS) {
		return OAuthOptions{}, "", errors.New("OAuth DCR requires persistent cross-process credential CAS")
	}
	canonical, err := canonicalOAuthResource(resource)
	if err != nil {
		return OAuthOptions{}, "", err
	}
	identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: canonical, Issuer: opts.Issuer}
	if err := validateDCRIdentity(identity); err != nil {
		return OAuthOptions{}, "", err
	}
	client, transport, err := newOAuthHTTPClient(canonical, opts)
	if err != nil {
		return OAuthOptions{}, "", err
	}
	defer transport.base.CloseIdleConnections()
	meta, endpoint, err := discoverDCRMetadata(ctx, canonical, opts, client)
	if err != nil {
		return OAuthOptions{}, "", err
	}
	key, err := oauthDCRRegistrationKey(identity)
	if err != nil {
		return OAuthOptions{}, "", err
	}
	record, getErr := opts.CredentialStore.Get(ctx, key)
	if getErr == nil {
		stored, decodeErr := decodeOAuthDCRRecord(record.Value, identity)
		if decodeErr != nil {
			return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
		}
		if action == OAuthDCRLoginReuse {
			meta.RedirectPath = stored.Metadata.RedirectPath
			if stored.State != oauthDCRStateReady || stored.MetadataFingerprint != fingerprintDCRMetadata(meta) || !equalDCRMetadata(stored.Metadata, meta) {
				return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
			}
			if err := prepareDCRGrantForExplicitLogin(ctx, opts.CredentialStore, stored, transport.issuerOrigin); err != nil {
				return OAuthOptions{}, "", err
			}
			return withResolvedDCR(opts, stored), stored.Metadata.RedirectPath, nil
		}
		if action == OAuthDCRLoginResetRegistration && stored.State != oauthDCRStateReady || action == OAuthDCRLoginRetryRegistration && stored.State != oauthDCRStatePending {
			return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
		}
		if action == OAuthDCRLoginResetRegistration {
			grantIdentity := oauthCredentialIdentity{
				Profile: identity.Profile, Principal: identity.Principal, Resource: identity.Resource,
				Issuer: identity.Issuer, ClientKind: oauthDCRClientKind, ClientID: stored.Registration.ClientID,
			}
			grantKey, keyErr := oauthDCRCredentialKey(grantIdentity, stored.Generation)
			if keyErr != nil {
				return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
			}
			grantRecord, grantErr := opts.CredentialStore.Get(ctx, grantKey)
			if grantErr == nil {
				issuerOrigins := map[string]struct{}{transport.issuerOrigin: {}}
				if _, decodeErr := decodeOAuthDCRGrant(grantRecord.Value, grantIdentity, stored.Generation, issuerOrigins); decodeErr != nil {
					return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
				}
			} else if !errors.Is(grantErr, credentialstore.ErrNotFound) {
				return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
			}
		}
		reason := "explicit_reset"
		if action == OAuthDCRLoginRetryRegistration {
			reason = "explicit_retry"
		}
		pending, pendingErr := newOAuthDCRPending(identity, meta, stored, reason)
		if pendingErr != nil {
			return OAuthOptions{}, "", pendingErr
		}
		value, encodeErr := encodeOAuthDCRRecord(pending, identity)
		if encodeErr != nil {
			return OAuthOptions{}, "", encodeErr
		}
		updated, putErr := opts.CredentialStore.Put(ctx, key, value, &record.Version)
		if putErr != nil {
			return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
		}
		opts.dcrTicket = &oauthDCRTicket{key: append([]byte(nil), key...), version: updated.Version, record: pending, registrationEndpoint: endpoint}
		return opts, pending.Metadata.RedirectPath, nil
	}
	if !errors.Is(getErr, credentialstore.ErrNotFound) || action != OAuthDCRLoginReuse {
		return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
	}
	pending, err := newOAuthDCRPending(identity, meta, oauthDCRRecord{}, "")
	if err != nil {
		return OAuthOptions{}, "", err
	}
	value, err := encodeOAuthDCRRecord(pending, identity)
	if err != nil {
		return OAuthOptions{}, "", err
	}
	created, err := opts.CredentialStore.Put(ctx, key, value, nil)
	if errors.Is(err, credentialstore.ErrConflict) {
		winner, winnerErr := opts.CredentialStore.Get(ctx, key)
		if winnerErr != nil {
			return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
		}
		stored, decodeErr := decodeOAuthDCRRecord(winner.Value, identity)
		if decodeErr == nil {
			meta.RedirectPath = stored.Metadata.RedirectPath
		}
		if decodeErr == nil && stored.State == oauthDCRStateReady && stored.MetadataFingerprint == fingerprintDCRMetadata(meta) && equalDCRMetadata(stored.Metadata, meta) {
			return withResolvedDCR(opts, stored), stored.Metadata.RedirectPath, nil
		}
		return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
	}
	if err != nil {
		return OAuthOptions{}, "", ErrOAuthDCRRecoveryRequired
	}
	opts.dcrTicket = &oauthDCRTicket{key: append([]byte(nil), key...), version: created.Version, record: pending, registrationEndpoint: endpoint}
	return opts, pending.Metadata.RedirectPath, nil
}

func prepareDCRGrantForExplicitLogin(ctx context.Context, store credentialstore.Store, registration oauthDCRRecord, issuerOrigin string) error {
	identity := oauthCredentialIdentity{
		Profile: registration.Identity.Profile, Principal: registration.Identity.Principal,
		Resource: registration.Identity.Resource, Issuer: registration.Identity.Issuer,
		ClientKind: oauthDCRClientKind, ClientID: registration.Registration.ClientID,
	}
	key, err := oauthDCRCredentialKey(identity, registration.Generation)
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	record, err := store.Get(ctx, key)
	if errors.Is(err, credentialstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	grant, err := decodeOAuthDCRGrant(record.Value, identity, registration.Generation, map[string]struct{}{issuerOrigin: {}})
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	if grant.State == "reset" {
		return nil
	}
	token, err := oauthDCRGrantToken(grant)
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	if token.Valid() {
		return nil
	}
	reset := newOAuthDCRResetGrant(identity, registration.Generation)
	value, err := encodeOAuthDCRGrant(reset, identity, registration.Generation, map[string]struct{}{issuerOrigin: {}})
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	if _, err := store.Put(ctx, key, value, &record.Version); err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	return nil
}

func newOAuthDCRPending(identity oauthDCRIdentity, meta oauthDCRMetadata, previous oauthDCRRecord, reason string) (oauthDCRRecord, error) {
	generation, err := randomDCRValue()
	if err != nil {
		return oauthDCRRecord{}, errors.New("generate OAuth DCR registration identity: failed")
	}
	pathValue, err := randomDCRValue()
	if err != nil {
		return oauthDCRRecord{}, errors.New("generate OAuth DCR callback path: failed")
	}
	meta.RedirectPath = oauthDCRCallbackPrefix + pathValue
	pending := oauthDCRRecord{
		Schema: oauthDCRRegistrationSchema, Version: oauthDCRRegistrationVersion, Identity: identity,
		Generation: generation, State: oauthDCRStatePending, AttemptStartedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: meta,
	}
	if reason != "" {
		pending.PreviousAttempt = &oauthDCRPreviousAttempt{Generation: previous.Generation, AttemptStartedAt: previous.AttemptStartedAt, Reason: reason}
	}
	pending.MetadataFingerprint = fingerprintDCRMetadata(meta)
	return pending, nil
}

func discoverDCRMetadata(ctx context.Context, resource string, opts OAuthOptions, client *http.Client) (oauthDCRMetadata, string, error) { //nolint:gocyclo // fail-closed metadata matrix is intentionally explicit.
	resourceURL, _ := url.Parse(resource)
	prmURL := urlOrigin(resourceURL) + "/.well-known/oauth-protected-resource" + resourceURL.EscapedPath()
	prm, err := oauthex.GetProtectedResourceMetadata(ctx, prmURL, resource, client)
	if err != nil {
		return oauthDCRMetadata{}, "", errors.New("OAuth DCR protected-resource metadata request failed")
	}
	if prm == nil || prm.Resource != resource || len(prm.AuthorizationServers) != 1 || prm.AuthorizationServers[0] != opts.Issuer || !slices.Contains(prm.ScopesSupported, oauthDCRScope) {
		return oauthDCRMetadata{}, "", errors.New("OAuth DCR protected-resource metadata is invalid")
	}
	issuerURL, err := validateHTTPURL("OAuth DCR issuer", opts.Issuer, !opts.allowLoopbackForTest)
	if err != nil {
		return oauthDCRMetadata{}, "", err
	}
	metadataURL := urlOrigin(issuerURL) + "/.well-known/oauth-authorization-server"
	if issuerURL.EscapedPath() != "" && issuerURL.EscapedPath() != "/" {
		metadataURL += issuerURL.EscapedPath()
	}
	as, err := oauthex.GetAuthServerMeta(ctx, metadataURL, opts.Issuer, client)
	if err != nil {
		return oauthDCRMetadata{}, "", errors.New("OAuth DCR authorization-server metadata request failed")
	}
	if as == nil || as.Issuer != opts.Issuer {
		return oauthDCRMetadata{}, "", errors.New("OAuth DCR authorization-server metadata is invalid")
	}
	for field, raw := range map[string]string{"registration": as.RegistrationEndpoint, "authorization": as.AuthorizationEndpoint, "token": as.TokenEndpoint} {
		u, endpointErr := validateHTTPURL("OAuth DCR "+field+" endpoint", raw, !opts.allowLoopbackForTest)
		if endpointErr != nil || u.RawQuery != "" || u.Fragment != "" || urlOrigin(u) != urlOrigin(issuerURL) {
			return oauthDCRMetadata{}, "", errors.New("OAuth DCR endpoint is invalid")
		}
	}
	if !slices.Contains(as.CodeChallengeMethodsSupported, "S256") || !slices.Contains(as.ResponseTypesSupported, "code") || !slices.Contains(as.GrantTypesSupported, "authorization_code") || !slices.Contains(as.TokenEndpointAuthMethodsSupported, "none") || !slices.Contains(as.ScopesSupported, oauthDCRScope) {
		return oauthDCRMetadata{}, "", errors.New("OAuth DCR public authorization-code metadata is unsupported")
	}
	return oauthDCRMetadata{Issuer: opts.Issuer, Resource: resource, RedirectPolicy: oauthDCRRedirectPolicy, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scopes: []string{oauthDCRScope}}, as.RegistrationEndpoint, nil
}

type dcrRegistrationResponseCapture struct {
	base     http.RoundTripper
	issuedAt *int64
	invalid  bool
}

func (c *dcrRegistrationResponseCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, credentialstore.MaxValueBytes+1))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil || len(body) > credentialstore.MaxValueBytes {
		c.invalid = true
		return resp, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		c.invalid = true
		return resp, nil
	}
	raw, present := fields["client_id_issued_at"]
	if !present {
		return resp, nil
	}
	var issuedAt int64
	if json.Unmarshal(raw, &issuedAt) != nil {
		c.invalid = true
		return resp, nil
	}
	c.issuedAt = &issuedAt
	return resp, nil
}

func resolvePreparedDCR(ctx context.Context, resource string, opts OAuthOptions, client *http.Client) (OAuthOptions, error) { //nolint:gocyclo // registration publication keeps each failure state explicit.
	if opts.Client.DCR == nil || opts.dcr != nil {
		return opts, nil
	}
	ticket := opts.dcrTicket
	if ticket == nil {
		if opts.CredentialStore == nil {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		canonical, err := canonicalOAuthResource(resource)
		if err != nil {
			return OAuthOptions{}, err
		}
		identity := oauthDCRIdentity{Profile: opts.Subject.Profile, Principal: opts.Subject.Principal, Resource: canonical, Issuer: opts.Issuer}
		meta, _, err := discoverDCRMetadata(ctx, canonical, opts, client)
		if err != nil {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		key, err := oauthDCRRegistrationKey(identity)
		if err != nil {
			return OAuthOptions{}, err
		}
		record, err := opts.CredentialStore.Get(ctx, key)
		if errors.Is(err, credentialstore.ErrNotFound) {
			return OAuthOptions{}, ErrOAuthLoginRequired
		}
		if err != nil {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		stored, err := decodeOAuthDCRRecord(record.Value, identity)
		if err != nil || stored.State != oauthDCRStateReady {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		meta.RedirectPath = stored.Metadata.RedirectPath
		if stored.MetadataFingerprint != fingerprintDCRMetadata(meta) || !equalDCRMetadata(stored.Metadata, meta) {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		opts = withResolvedDCR(opts, stored)
		opts.RedirectURL = stored.Registration.RegisteredRedirectURI
		return opts, nil
	}
	if !ticket.consume() || opts.CredentialStore == nil {
		return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
	}
	redirect, err := url.Parse(opts.RedirectURL)
	if err != nil || redirect.Scheme != oauthHTTPURLScheme || redirect.User != nil || redirect.Hostname() != "127.0.0.1" || !validDCRRedirectPort(redirect.Port()) || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.Path != ticket.record.Metadata.RedirectPath {
		return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
	}
	request := &oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{opts.RedirectURL}, TokenEndpointAuthMethod: "none", GrantTypes: append([]string(nil), ticket.record.Metadata.GrantTypes...),
		ResponseTypes: []string{"code"}, ClientName: "mecatl", Scope: strings.Join(ticket.record.Metadata.Scopes, " "),
	}
	registerClient := *client
	registerClient.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrOAuthDCRRecoveryRequired }
	transport := registerClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	capture := &dcrRegistrationResponseCapture{base: transport}
	registerClient.Transport = capture
	response, err := oauthex.RegisterClient(ctx, ticket.registrationEndpoint, request, &registerClient)
	if err != nil || capture.invalid || capture.issuedAt != nil && *capture.issuedAt < 0 || !validDCRRegistrationResponse(response, request) {
		if winner, ok := adoptDCRReady(ctx, opts.CredentialStore, ticket); ok {
			return withResolvedDCR(opts, winner), nil
		}
		return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
	}
	ready := ticket.record
	ready.State = oauthDCRStateReady
	ready.Registration = &oauthDCRRegistration{ClientID: response.ClientID, RegisteredRedirectURI: opts.RedirectURL}
	if capture.issuedAt != nil {
		issued := *capture.issuedAt
		ready.Registration.ClientIDIssuedAt = &issued
	}
	value, encodeErr := encodeOAuthDCRRecord(ready, ticket.record.Identity)
	if encodeErr != nil {
		return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
	}
	if _, err = opts.CredentialStore.Put(ctx, ticket.key, value, &ticket.version); err != nil {
		winner, ok := adoptDCRReady(ctx, opts.CredentialStore, ticket)
		if !ok {
			return OAuthOptions{}, ErrOAuthDCRRecoveryRequired
		}
		ready = winner
	}
	return withResolvedDCR(opts, ready), nil
}

func adoptDCRReady(ctx context.Context, store credentialstore.Store, ticket *oauthDCRTicket) (oauthDCRRecord, bool) {
	winner, err := store.Get(ctx, ticket.key)
	if err != nil {
		return oauthDCRRecord{}, false
	}
	stored, err := decodeOAuthDCRRecord(winner.Value, ticket.record.Identity)
	if err != nil || stored.State != oauthDCRStateReady || stored.Generation != ticket.record.Generation || stored.MetadataFingerprint != ticket.record.MetadataFingerprint || !equalDCRMetadata(stored.Metadata, ticket.record.Metadata) {
		return oauthDCRRecord{}, false
	}
	return stored, true
}

func validDCRRegistrationResponse(response *oauthex.ClientRegistrationResponse, request *oauthex.ClientRegistrationMetadata) bool {
	if response == nil || validateSafeValue("OAuth DCR client ID", response.ClientID) != nil || response.ClientSecret != "" || response.TokenEndpointAuthMethod != "none" {
		return false
	}
	if !response.ClientIDIssuedAt.IsZero() && response.ClientIDIssuedAt.Unix() < 0 {
		return false
	}
	if !slices.Equal(response.RedirectURIs, request.RedirectURIs) {
		return false
	}
	if len(response.GrantTypes) != 0 && !sameStrings(response.GrantTypes, request.GrantTypes) || len(response.ResponseTypes) != 0 && !sameStrings(response.ResponseTypes, request.ResponseTypes) {
		return false
	}
	if response.Scope != "" && !sameStrings(strings.Fields(response.Scope), strings.Fields(request.Scope)) {
		return false
	}
	return true
}

func sameStrings(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(compactStrings(a), compactStrings(b))
}

func withResolvedDCR(opts OAuthOptions, record oauthDCRRecord) OAuthOptions {
	opts.dcrTicket = nil
	opts.dcr = &oauthDCRResolved{issuer: record.Identity.Issuer, clientID: record.Registration.ClientID, generation: record.Generation, path: record.Metadata.RedirectPath}
	return opts
}

func validateDCRIdentity(identity oauthDCRIdentity) error {
	for _, field := range []struct{ name, value string }{{"OAuth profile", identity.Profile}, {"OAuth principal", identity.Principal}, {"OAuth resource", identity.Resource}, {"OAuth issuer", identity.Issuer}} {
		if err := validateSafeValue(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func oauthDCRRegistrationKey(identity oauthDCRIdentity) ([]byte, error) {
	if err := validateDCRIdentity(identity); err != nil {
		return nil, err
	}
	fields := []string{identity.Profile, identity.Principal, identity.Resource, identity.Issuer}
	framed := []byte(oauthDCRRegistrationKeyDomain)
	var size [4]byte
	for _, field := range fields {
		if len(field) > math.MaxUint32 {
			return nil, errors.New("OAuth DCR identity field is too large")
		}
		binary.BigEndian.PutUint32(size[:], uint32(len(field))) // #nosec G115 -- checked above.
		framed = append(framed, size[:]...)
		framed = append(framed, field...)
	}
	digest := sha256.Sum256(framed)
	return digest[:], nil
}

func encodeOAuthDCRRecord(record oauthDCRRecord, expected oauthDCRIdentity) ([]byte, error) {
	if err := validateOAuthDCRRecord(record, expected); err != nil {
		return nil, err
	}
	value, err := json.Marshal(record)
	if err != nil || len(value) > credentialstore.MaxValueBytes {
		return nil, errors.New("OAuth DCR registration record is invalid")
	}
	return value, nil
}

func decodeOAuthDCRRecord(value []byte, expected oauthDCRIdentity) (oauthDCRRecord, error) {
	if len(value) == 0 || len(value) > credentialstore.MaxValueBytes || !uniqueDCRJSONKeys(value) {
		return oauthDCRRecord{}, errors.New("OAuth DCR registration record is invalid")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(value), credentialstore.MaxValueBytes+1))
	decoder.DisallowUnknownFields()
	var record oauthDCRRecord
	if err := decoder.Decode(&record); err != nil {
		return oauthDCRRecord{}, errors.New("OAuth DCR registration record is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return oauthDCRRecord{}, errors.New("OAuth DCR registration record is invalid")
	}
	if err := validateOAuthDCRRecord(record, expected); err != nil {
		return oauthDCRRecord{}, err
	}
	return record, nil
}

func uniqueDCRJSONKeys(value []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || !uniqueDCRJSONValue(decoder, token) {
		return false
	}
	_, err = decoder.Token()
	return errors.Is(err, io.EOF)
}

func uniqueDCRJSONValue(decoder *json.Decoder, token json.Token) bool {
	delim, composite := token.(json.Delim)
	if !composite {
		return true
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return false
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil || !uniqueDCRJSONValue(decoder, valueToken) {
				return false
			}
		}
		closeToken, err := decoder.Token()
		return err == nil && closeToken == json.Delim('}')
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil || !uniqueDCRJSONValue(decoder, valueToken) {
				return false
			}
		}
		closeToken, err := decoder.Token()
		return err == nil && closeToken == json.Delim(']')
	default:
		return false
	}
}

func validateOAuthDCRRecord(record oauthDCRRecord, expected oauthDCRIdentity) error { //nolint:gocyclo // strict persisted-state validation is intentionally linear.
	if record.Schema != oauthDCRRegistrationSchema || record.Version != oauthDCRRegistrationVersion || record.Identity != expected || validateDCRIdentity(record.Identity) != nil {
		return errors.New("OAuth DCR registration record identity is invalid")
	}
	if !validDCRRandom(record.Generation) || record.AttemptStartedAt == "" {
		return errors.New("OAuth DCR registration attempt is invalid")
	}
	parsedAttempt, err := time.Parse(time.RFC3339Nano, record.AttemptStartedAt)
	if err != nil || !strings.HasSuffix(record.AttemptStartedAt, "Z") || parsedAttempt.Format(time.RFC3339Nano) != record.AttemptStartedAt {
		return errors.New("OAuth DCR registration attempt is invalid")
	}
	if record.MetadataFingerprint != fingerprintDCRMetadata(record.Metadata) || !validDCRMetadata(record.Metadata, expected) {
		return errors.New("OAuth DCR registration binding is invalid")
	}
	if record.PreviousAttempt != nil {
		previous := record.PreviousAttempt
		if !validDCRRandom(previous.Generation) || previous.AttemptStartedAt == "" || previous.Reason != "explicit_retry" && previous.Reason != "explicit_reset" {
			return errors.New("OAuth DCR previous registration attempt is invalid")
		}
		parsedPrevious, err := time.Parse(time.RFC3339Nano, previous.AttemptStartedAt)
		if err != nil || !strings.HasSuffix(previous.AttemptStartedAt, "Z") || parsedPrevious.Format(time.RFC3339Nano) != previous.AttemptStartedAt {
			return errors.New("OAuth DCR previous registration attempt is invalid")
		}
	}
	switch record.State {
	case oauthDCRStatePending:
		if record.Registration != nil {
			return errors.New("OAuth DCR pending record contains registration")
		}
	case oauthDCRStateReady:
		if record.Registration == nil || validateSafeValue("OAuth DCR client ID", record.Registration.ClientID) != nil || record.Registration.RegisteredRedirectURI == "" {
			return errors.New("OAuth DCR ready record is invalid")
		}
		u, err := url.Parse(record.Registration.RegisteredRedirectURI)
		if err != nil || u.Scheme != oauthHTTPURLScheme || u.User != nil || u.Hostname() != "127.0.0.1" || !validDCRRedirectPort(u.Port()) || u.Path != record.Metadata.RedirectPath || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("OAuth DCR registered redirect is invalid")
		}
		if record.Registration.ClientIDIssuedAt != nil && *record.Registration.ClientIDIssuedAt < 0 {
			return errors.New("OAuth DCR issuance time is invalid")
		}
	default:
		return errors.New("OAuth DCR registration state is invalid")
	}
	return nil
}

func validDCRMetadata(meta oauthDCRMetadata, identity oauthDCRIdentity) bool {
	return meta.Issuer == identity.Issuer && meta.Resource == identity.Resource && meta.RedirectPolicy == oauthDCRRedirectPolicy && validDCRCallbackPath(meta.RedirectPath) && meta.TokenEndpointAuthMethod == "none" && slices.Equal(meta.GrantTypes, []string{"authorization_code"}) && slices.Equal(meta.ResponseTypes, []string{"code"}) && slices.Equal(meta.Scopes, []string{oauthDCRScope})
}

func validDCRRequestedScopes(opts OAuthOptions) bool {
	return !opts.RequestRefreshToken && len(opts.AllowedScopes) == 1 && opts.AllowedScopes[0] == oauthDCRScope
}

func validDCRRedirectPort(port string) bool {
	value, err := strconv.ParseUint(port, 10, 16)
	return err == nil && value != 0
}

func equalDCRMetadata(a, b oauthDCRMetadata) bool {
	return a.Issuer == b.Issuer && a.Resource == b.Resource && a.RedirectPolicy == b.RedirectPolicy && a.RedirectPath == b.RedirectPath && a.TokenEndpointAuthMethod == b.TokenEndpointAuthMethod && slices.Equal(a.GrantTypes, b.GrantTypes) && slices.Equal(a.ResponseTypes, b.ResponseTypes) && slices.Equal(a.Scopes, b.Scopes)
}

func fingerprintDCRMetadata(meta oauthDCRMetadata) string {
	value, _ := json.Marshal(meta)
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func randomDCRValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func validDCRRandom(value string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
}

func validDCRCallbackPath(value string) bool {
	return strings.HasPrefix(value, oauthDCRCallbackPrefix) && validDCRRandom(strings.TrimPrefix(value, oauthDCRCallbackPrefix))
}
