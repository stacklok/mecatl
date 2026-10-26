package identityharness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	as1KeyID          = "as1-key-1"
	as1UpstreamClient = "as1"
	as1CodeLifetime   = time.Minute
	as1IssuerPath     = "/as"
)

// AS1 stands in for AS-1, Mecatl's sign-in authorization server. It signs
// users in with the authorization code flow and PKCE (S256, required), sends
// them to the upstream IdP as an ordinary OIDC client asking for offline
// access, and issues the AS-1 access token design step (1) describes:
//
//	header: typ at+jwt, alg ES256, kid
//	claims: iss, sub, sub_id {format iss_sub, iss, sub}, aud, client_id, iat, exp, jti
//
// sub is AS-1's own subject for the company identity: stable across sign-ins,
// and never shared by two company identities. Clients also receive a refresh
// token they can use at the refresh_token grant.
//
// The issuer has a path (/as), as AS-1's final issuer URL does, so discovery
// is served both at {issuer}/.well-known/openid-configuration and at the
// RFC 8414 location.
type AS1 struct {
	server   *httptest.Server
	key      *ecdsa.PrivateKey
	foreign  *ecdsa.PrivateKey
	now      func() time.Time
	audience string
	lifetime time.Duration
	idp      *IdP
	secret   string
	faults   faults
	http     *http.Client

	brokerKey       *ecdsa.PrivateKey
	assertionMax    time.Duration
	sessionLifetime time.Duration

	mu       sync.Mutex
	clients  map[string]Client
	pending  map[string]as1Pending // by upstream state
	codes    map[string]as1Code
	refresh  map[string]as1Refresh
	subjects map[string]string // AS-1 subject by company identity
	upstream map[string]string // upstream refresh token by company subject

	brokerKeys    map[string]*ecdsa.PublicKey // by kid
	assertionJTIs map[string]time.Time        // used client assertion jti, kept until
	sessions      map[string]*as1Session
	sessionTokens map[string]*as1SessionToken
	conversions   map[string]as1Conversion // by converted access token jti
	refusals      map[string][]string      // refused open reasons by session
	echoes        []SessionDetails         // forced mismatched echoes
}

type as1Pending struct {
	clientID, redirectURI, state, challenge string
	nonce, verifier                         string
}

type as1Code struct {
	clientID, redirectURI, challenge, user string
	expires                                time.Time
}

type as1Refresh struct{ clientID, user string }

func newAS1(t testing.TB, opts Options, idp *IdP) *AS1 {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("identityharness: AS-1 key: %v", err)
	}
	foreign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("identityharness: foreign key: %v", err)
	}
	brokerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("identityharness: broker key: %v", err)
	}
	as := &AS1{
		key: key, foreign: foreign, now: opts.Now, audience: opts.Audience,
		lifetime: opts.AccessTokenLifetime, idp: idp, secret: randomToken("as1-secret-"),
		brokerKey: brokerKey, assertionMax: opts.ClientAssertionMaxLifetime,
		sessionLifetime: opts.SessionRefreshTokenLifetime,
		clients:         make(map[string]Client),
		pending:         make(map[string]as1Pending),
		codes:           make(map[string]as1Code),
		refresh:         make(map[string]as1Refresh),
		subjects:        make(map[string]string),
		upstream:        make(map[string]string),
		brokerKeys:      make(map[string]*ecdsa.PublicKey),
		assertionJTIs:   make(map[string]time.Time),
		sessions:        make(map[string]*as1Session),
		sessionTokens:   make(map[string]*as1SessionToken),
		conversions:     make(map[string]as1Conversion),
		refusals:        make(map[string][]string),
	}
	for _, c := range opts.Clients {
		as.clients[c.ID] = Client{ID: c.ID, RedirectURIs: slices.Clone(c.RedirectURIs)}
	}
	as.registerBrokerKeys(t, opts.BrokerKeys)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+as1IssuerPath+"/.well-known/openid-configuration", as.discovery)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server"+as1IssuerPath, as.discovery)
	mux.HandleFunc("GET "+as1IssuerPath+"/authorize", as.authorize)
	mux.HandleFunc("GET "+as1IssuerPath+"/callback", as.callback)
	mux.HandleFunc("POST "+as1IssuerPath+"/token", as.token)
	mux.HandleFunc("POST "+as1IssuerPath+"/revoke", as.revoke)
	mux.HandleFunc("GET "+as1IssuerPath+"/jwks", as.jwks)
	as.server = httptest.NewTLSServer(mux)
	t.Cleanup(as.server.Close)
	idp.RegisterClient(as1UpstreamClient, as.secret, as.Issuer()+"/callback")
	return as
}

// Issuer is AS-1's issuer URL, which mecak8s accepts (--oidc-issuer).
func (a *AS1) Issuer() string { return a.server.URL + as1IssuerPath }

// AuthorizationEndpoint is where clients start sign-in.
func (a *AS1) AuthorizationEndpoint() string { return a.Issuer() + "/authorize" }

// TokenEndpoint is AS-1's token endpoint.
func (a *AS1) TokenEndpoint() string { return a.Issuer() + "/token" }

// Audience is the Mecatl API identifier AS-1 access tokens carry.
func (a *AS1) Audience() string { return a.audience }

// Subject returns AS-1's subject for the company identity user, assigning one
// on first use. Two company identities never share a subject.
func (a *AS1) Subject(user string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subjectLocked(user)
}

func (a *AS1) subjectLocked(user string) string {
	if sub, ok := a.subjects[user]; ok {
		return sub
	}
	sum := sha256.Sum256([]byte(a.idp.Issuer() + "\x00" + user))
	sub := "u-" + hex.EncodeToString(sum[:8])
	a.subjects[user] = sub
	return sub
}

// HasUpstreamRefreshToken reports whether AS-1 holds an upstream refresh
// token for user, which its later upstream re-check needs.
func (a *AS1) HasUpstreamRefreshToken(user string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.upstream[user] != ""
}

// FailNextAuthorization makes the next authorization request redirect back
// with fault.Error instead of signing the user in.
func (a *AS1) FailNextAuthorization(fault Fault) { a.faults.push("authorize", fault) }

// FailNextToken makes the next token request from a registered client fail
// with fault.
func (a *AS1) FailNextToken(fault Fault) { a.faults.push("token", fault) }

// AccessTokenSpec describes an AS-1 access token for MintAccessToken. Fields
// left empty take the values a real sign-in produces, so a test changes only
// the defect it checks.
type AccessTokenSpec struct {
	// User is the company identity (IdP subject). Empty selects the IdP's
	// default user.
	User string
	// ClientID is the client_id claim. Empty selects the first registered
	// client.
	ClientID string
	// Audience replaces the Mecatl API identifier in aud.
	Audience string
	// Issuer replaces AS-1's issuer in iss.
	Issuer string
	// IssuedAt replaces the current time in iat; exp follows from it.
	IssuedAt time.Time
	// Lifetime replaces the configured lifetime. Negative issues an expired
	// token.
	Lifetime time.Duration
	// Type replaces at+jwt in the typ header.
	Type string
	// OmitType leaves typ out of the header.
	OmitType bool
	// OmitSubID leaves the sub_id claim out.
	OmitSubID bool
	// SubID, when non-nil, replaces the sub_id claim, for a malformed one.
	SubID any
	// ForeignKey signs with a key that is not in AS-1's JWKS, under AS-1's
	// kid.
	ForeignKey bool
}

// MintAccessToken issues an AS-1 access token without a sign-in, for tests
// that need a specific token or a specific defect.
func (a *AS1) MintAccessToken(spec AccessTokenSpec) string {
	if spec.User == "" {
		spec.User, _ = a.idp.user("")
	}
	if spec.ClientID == "" {
		spec.ClientID = a.firstClient()
	}
	token, err := a.signAccessToken(spec)
	if err != nil {
		panic("identityharness: mint access token: " + err.Error())
	}
	return token
}

func (a *AS1) firstClient() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.clients))
	for id := range a.clients {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (a *AS1) signAccessToken(spec AccessTokenSpec) (string, error) {
	iat := spec.IssuedAt
	if iat.IsZero() {
		iat = a.now()
	}
	lifetime := spec.Lifetime
	if lifetime == 0 {
		lifetime = a.lifetime
	}
	claims := jwt.MapClaims{
		"iss":       firstNonEmpty(spec.Issuer, a.Issuer()),
		"sub":       a.Subject(spec.User),
		"aud":       firstNonEmpty(spec.Audience, a.audience),
		"client_id": spec.ClientID,
		"iat":       iat.Unix(),
		"exp":       iat.Add(lifetime).Unix(),
		"jti":       randomToken("at-"),
	}
	switch {
	case spec.OmitSubID:
	case spec.SubID != nil:
		claims["sub_id"] = spec.SubID
	default:
		claims["sub_id"] = map[string]string{"format": "iss_sub", "iss": a.idp.Issuer(), "sub": spec.User}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = as1KeyID
	switch {
	case spec.OmitType:
		delete(token.Header, "typ")
	default:
		token.Header["typ"] = firstNonEmpty(spec.Type, "at+jwt")
	}
	key := a.key
	if spec.ForeignKey {
		key = a.foreign
	}
	return token.SignedString(key)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *AS1) discovery(w http.ResponseWriter, _ *http.Request) {
	iss := a.Issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                           iss,
		"authorization_endpoint":           iss + "/authorize",
		"token_endpoint":                   iss + "/token",
		"revocation_endpoint":              iss + "/revoke",
		"jwks_uri":                         iss + "/jwks",
		"response_types_supported":         []string{responseTypeCode},
		"grant_types_supported":            []string{grantAuthorizationCode, grantRefreshToken, grantTokenExchange},
		"code_challenge_methods_supported": []string{pkceS256},
		// none for the public clients, private_key_jwt for the broker.
		"token_endpoint_auth_methods_supported":            []string{"none", authPrivateKeyJWT},
		"token_endpoint_auth_signing_alg_values_supported": []string{"ES256"},
		"revocation_endpoint_auth_methods_supported":       []string{authPrivateKeyJWT},
		"authorization_details_types_supported":            []string{SessionDetailsType},
	})
}

func (a *AS1) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{ecJWK(as1KeyID, &a.key.PublicKey)}})
}

func (a *AS1) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI, state := q.Get(paramClientID), q.Get("redirect_uri"), q.Get("state")
	a.mu.Lock()
	client, ok := a.clients[clientID]
	a.mu.Unlock()
	// An unknown client or redirect URI is never redirected to (RFC 6749
	// §4.1.2.1).
	if !ok || !slices.Contains(client.RedirectURIs, redirectURI) {
		http.Error(w, "unknown client or redirect_uri", http.StatusBadRequest)
		return
	}
	if fault, ok := a.faults.pop("authorize"); ok {
		redirectWithError(w, r, redirectURI, state, fault.Error)
		return
	}
	if q.Get("response_type") != responseTypeCode {
		redirectWithError(w, r, redirectURI, state, "unsupported_response_type")
		return
	}
	// Public clients must use PKCE with S256 (RFC 9700 §2.1.1).
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != pkceS256 {
		redirectWithError(w, r, redirectURI, state, "invalid_request")
		return
	}
	if !a.validResource(q) {
		redirectWithError(w, r, redirectURI, state, errInvalidTarget)
		return
	}
	upstreamState := randomToken("as1-state-")
	pending := as1Pending{
		clientID: clientID, redirectURI: redirectURI, state: state, challenge: q.Get("code_challenge"),
		nonce: randomToken("as1-nonce-"), verifier: randomToken("as1-verifier-"),
	}
	a.mu.Lock()
	a.pending[upstreamState] = pending
	a.mu.Unlock()
	up := url.Values{
		"response_type":         {responseTypeCode},
		paramClientID:           {as1UpstreamClient},
		"redirect_uri":          {a.Issuer() + "/callback"},
		"scope":                 {"openid email offline_access"},
		"prompt":                {"consent"},
		"state":                 {upstreamState},
		"nonce":                 {pending.nonce},
		"code_challenge":        {s256(pending.verifier)},
		"code_challenge_method": {pkceS256},
	}
	if hint := q.Get("login_hint"); hint != "" {
		up.Set("login_hint", hint)
	}
	http.Redirect(w, r, withQuery(a.idp.Issuer()+"/authorize", up), http.StatusFound)
}

func (a *AS1) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	pending, ok := a.pending[q.Get("state")]
	delete(a.pending, q.Get("state")) // single use
	a.mu.Unlock()
	if !ok {
		http.Error(w, "unknown sign-in state", http.StatusBadRequest)
		return
	}
	if upstreamErr := q.Get("error"); upstreamErr != "" {
		code := "server_error"
		if upstreamErr == "access_denied" {
			code = "access_denied"
		}
		redirectWithError(w, r, pending.redirectURI, pending.state, code)
		return
	}
	user, upstreamRefresh, err := a.redeemUpstream(r, q.Get("code"), pending)
	if err != nil {
		redirectWithError(w, r, pending.redirectURI, pending.state, "server_error")
		return
	}
	code := randomToken("as1-code-")
	a.mu.Lock()
	a.subjectLocked(user)
	if upstreamRefresh != "" {
		a.upstream[user] = upstreamRefresh
	}
	a.codes[code] = as1Code{
		clientID: pending.clientID, redirectURI: pending.redirectURI, challenge: pending.challenge,
		user: user, expires: a.now().Add(as1CodeLifetime),
	}
	a.mu.Unlock()
	back := url.Values{"code": {code}}
	if pending.state != "" {
		back.Set("state", pending.state)
	}
	redirect(w, r, pending.redirectURI, back)
}

// redeemUpstream exchanges the IdP's code and verifies its ID token as an OIDC
// client would, fetching the IdP's keys over HTTP. It returns the company
// subject and any upstream refresh token.
func (a *AS1) redeemUpstream(r *http.Request, code string, pending as1Pending) (string, string, error) {
	form := url.Values{
		"grant_type":    {grantAuthorizationCode},
		"code":          {code},
		"redirect_uri":  {a.Issuer() + "/callback"},
		"code_verifier": {pending.verifier},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.idp.Issuer()+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(as1UpstreamClient, a.secret)
	var tokens struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := a.doJSON(req, &tokens); err != nil {
		return "", "", err
	}
	key, err := a.idpKey(r)
	if err != nil {
		return "", "", err
	}
	var claims struct {
		jwt.RegisteredClaims
		Nonce string `json:"nonce"`
	}
	_, err = jwt.ParseWithClaims(tokens.IDToken, &claims, func(*jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(a.idp.Issuer()),
		jwt.WithAudience(as1UpstreamClient), jwt.WithExpirationRequired(), jwt.WithTimeFunc(a.now))
	if err != nil {
		return "", "", fmt.Errorf("upstream ID token: %w", err)
	}
	if claims.Nonce != pending.nonce || claims.Subject == "" {
		return "", "", errors.New("upstream ID token: nonce or subject mismatch")
	}
	return claims.Subject, tokens.RefreshToken, nil
}

func (a *AS1) idpKey(r *http.Request) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.idp.Issuer()+"/jwks", nil)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct{ Kid, N, E string } `json:"keys"`
	}
	if err := a.doJSON(req, &set); err != nil {
		return nil, err
	}
	for _, k := range set.Keys {
		if k.Kid != idpKeyID {
			continue
		}
		n, nErr := base64.RawURLEncoding.DecodeString(k.N)
		e, eErr := base64.RawURLEncoding.DecodeString(k.E)
		if nErr != nil || eErr != nil {
			return nil, errors.New("upstream JWKS: malformed key")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return nil, errors.New("upstream JWKS: key not found")
}

func (a *AS1) doJSON(req *http.Request, out any) error {
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *AS1) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	clientID, confidential, ok := a.authenticateClient(r)
	if !ok {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	if confidential {
		a.brokerToken(w, r)
		return
	}
	if fault, ok := a.faults.pop("token"); ok {
		writeOAuthError(w, fault.Status, fault.Error, fault.Reason)
		return
	}
	if !a.validResource(r.PostForm) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidTarget, "")
		return
	}
	var user, refresh string
	switch r.PostForm.Get("grant_type") {
	case grantAuthorizationCode:
		var ok bool
		user, ok = a.redeemCode(clientID, r.PostForm)
		if !ok {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
			return
		}
		refresh = randomToken("as1-rt-")
		a.mu.Lock()
		a.refresh[refresh] = as1Refresh{clientID: clientID, user: user}
		a.mu.Unlock()
	case grantRefreshToken:
		// Refresh tokens rotate on every use, as they do at ToolHive's fosite
		// AS: the presented one stops working.
		presented := r.PostForm.Get("refresh_token")
		refresh = randomToken("as1-rt-")
		a.mu.Lock()
		record, ok := a.refresh[presented]
		if ok && record.clientID == clientID {
			delete(a.refresh, presented)
			a.refresh[refresh] = record
		}
		a.mu.Unlock()
		if !ok || record.clientID != clientID {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
			return
		}
		user = record.user
	case grantTokenExchange:
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client", "")
		return
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
		return
	}
	access, err := a.signAccessToken(AccessTokenSpec{User: user, ClientID: clientID})
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer",
		"expires_in": int(a.lifetime.Seconds()), "refresh_token": refresh,
	})
}

// brokerToken serves the authenticated broker, which may use only the
// token-exchange grant for session open. Session refresh tokens are stored
// apart from client refresh tokens and the broker holds none of the latter,
// so the refresh-token grant always fails with invalid_grant.
func (a *AS1) brokerToken(w http.ResponseWriter, r *http.Request) {
	switch r.PostForm.Get("grant_type") {
	case grantTokenExchange:
		a.openSession(w, r)
	case grantRefreshToken:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
	case grantAuthorizationCode:
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client", "")
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

const (
	paramResource = "resource"
	// errInvalidTarget is RFC 8707's error for an unacceptable resource.
	errInvalidTarget = "invalid_target"
)

// validResource reports whether an optional RFC 8707 resource names the Mecatl
// API, the only resource AS-1 issues tokens for (so aud stays the audience).
func (a *AS1) validResource(values url.Values) bool {
	resources, ok := values[paramResource]
	return !ok || len(resources) == 1 && resources[0] == a.audience
}

// redeemCode checks an authorization code grant (RFC 6749 §4.1.3, RFC 7636
// §4.6) and returns the signed-in company identity. Codes are single use.
func (a *AS1) redeemCode(clientID string, form url.Values) (string, bool) {
	a.mu.Lock()
	code, ok := a.codes[form.Get("code")]
	delete(a.codes, form.Get("code"))
	a.mu.Unlock()
	verifier := form.Get("code_verifier")
	if !ok || code.clientID != clientID || code.redirectURI != form.Get("redirect_uri") ||
		!a.now().Before(code.expires) || !validVerifier(verifier) || s256(verifier) != code.challenge {
		return "", false
	}
	return code.user, true
}
