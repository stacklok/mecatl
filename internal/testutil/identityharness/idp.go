package identityharness

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
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
	idpKeyID        = "idp-key-1"
	idpCodeLifetime = time.Minute
	idpIDTokenLife  = 5 * time.Minute
)

// IdP stands in for the upstream company IdP. AS-1 is an ordinary OIDC client
// of it (design step (1)): the IdP signs users in with the authorization code
// flow and returns an ID token naming the company identity. It issues a
// refresh token only when the client asks for offline access the way OIDC
// Core §11 requires: the offline_access scope together with prompt=consent.
//
// Sign-in is headless. The user is the one named by login_hint, or the
// default user when login_hint is absent; an unknown user is refused with
// access_denied.
type IdP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	now    func() time.Time
	faults faults

	mu      sync.Mutex
	users   []string
	clients map[string]idpClient
	codes   map[string]idpCode
	offline map[string]int // refresh tokens issued, by subject
}

type idpClient struct {
	secret      string
	redirectURI string
}

type idpCode struct {
	clientID, redirectURI, challenge, nonce, subject string
	offline                                          bool
	expires                                          time.Time
}

func newIdP(t testing.TB, opts Options) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("identityharness: IdP key: %v", err)
	}
	idp := &IdP{
		key: key, now: opts.Now,
		users:   slices.Clone(opts.Users),
		clients: make(map[string]idpClient),
		codes:   make(map[string]idpCode),
		offline: make(map[string]int),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", idp.discovery)
	mux.HandleFunc("GET /authorize", idp.authorize)
	mux.HandleFunc("POST /token", idp.token)
	mux.HandleFunc("GET /jwks", idp.jwks)
	idp.server = httptest.NewTLSServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// Issuer is the IdP's issuer, which AS-1 access tokens carry as sub_id.iss.
func (p *IdP) Issuer() string { return p.server.URL }

// AddUser lets the IdP sign in subject.
func (p *IdP) AddUser(subject string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Contains(p.users, subject) {
		p.users = append(p.users, subject)
	}
}

// RegisterClient registers a confidential OIDC client that authenticates to
// the token endpoint with client_secret_basic or client_secret_post. AS-1
// registers itself this way; a test hosting a real AS-1 registers that one.
func (p *IdP) RegisterClient(id, secret, redirectURI string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[id] = idpClient{secret: secret, redirectURI: redirectURI}
}

// FailNextAuthorization makes the next authorization request redirect back
// with fault.Error, such as access_denied, instead of signing the user in.
func (p *IdP) FailNextAuthorization(fault Fault) { p.faults.push("authorize", fault) }

// FailNextToken makes the next token request fail with fault.
func (p *IdP) FailNextToken(fault Fault) { p.faults.push("token", fault) }

// RefreshTokensIssued reports how many upstream refresh tokens the IdP issued
// for subject, which shows whether AS-1 asked for offline access.
func (p *IdP) RefreshTokensIssued(subject string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offline[subject]
}

func (p *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	base := p.server.URL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/authorize",
		"token_endpoint":                        base + "/token",
		"jwks_uri":                              base + "/jwks",
		"response_types_supported":              []string{responseTypeCode},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "email", "profile", "offline_access"},
		"grant_types_supported":                 []string{grantAuthorizationCode},
		"code_challenge_methods_supported":      []string{pkceS256},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

func (p *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{rsaJWK(idpKeyID, &p.key.PublicKey)}})
}

func (p *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI, state := q.Get(paramClientID), q.Get("redirect_uri"), q.Get("state")
	p.mu.Lock()
	client, ok := p.clients[clientID]
	p.mu.Unlock()
	// An unknown client or redirect URI is never redirected to (RFC 6749
	// §4.1.2.1).
	if !ok || redirectURI != client.redirectURI {
		http.Error(w, "unknown client or redirect_uri", http.StatusBadRequest)
		return
	}
	if fault, ok := p.faults.pop("authorize"); ok {
		redirectWithError(w, r, redirectURI, state, fault.Error)
		return
	}
	scopes := strings.Fields(q.Get("scope"))
	method := q.Get("code_challenge_method")
	switch {
	case q.Get("response_type") != responseTypeCode:
		redirectWithError(w, r, redirectURI, state, "unsupported_response_type")
		return
	case !slices.Contains(scopes, "openid"):
		redirectWithError(w, r, redirectURI, state, "invalid_scope")
		return
	case q.Get("code_challenge") != "" && method != pkceS256:
		redirectWithError(w, r, redirectURI, state, "invalid_request")
		return
	}
	subject, ok := p.user(q.Get("login_hint"))
	if !ok {
		redirectWithError(w, r, redirectURI, state, "access_denied")
		return
	}
	code := randomToken("idp-code-")
	p.mu.Lock()
	p.codes[code] = idpCode{
		clientID: clientID, redirectURI: redirectURI, challenge: q.Get("code_challenge"),
		nonce: q.Get("nonce"), subject: subject,
		offline: slices.Contains(scopes, "offline_access") && slices.Contains(strings.Fields(q.Get("prompt")), "consent"),
		expires: p.now().Add(idpCodeLifetime),
	}
	p.mu.Unlock()
	back := url.Values{"code": {code}}
	if state != "" {
		back.Set("state", state)
	}
	redirect(w, r, redirectURI, back)
}

func (p *IdP) user(hint string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if hint == "" {
		if len(p.users) == 0 {
			return "", false
		}
		return p.users[0], true
	}
	return hint, slices.Contains(p.users, hint)
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	clientID, ok := p.authenticateClient(r)
	if !ok {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	if fault, ok := p.faults.pop("token"); ok {
		writeOAuthError(w, fault.Status, fault.Error, fault.Reason)
		return
	}
	if r.PostForm.Get("grant_type") != grantAuthorizationCode {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
		return
	}
	p.mu.Lock()
	code, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code")) // single use
	p.mu.Unlock()
	if !found || code.clientID != clientID || code.redirectURI != r.PostForm.Get("redirect_uri") ||
		!p.now().Before(code.expires) || code.challenge != "" && s256(r.PostForm.Get("code_verifier")) != code.challenge {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "")
		return
	}
	now := p.now()
	claims := jwt.MapClaims{
		"iss": p.server.URL, "sub": code.subject, "aud": clientID,
		"iat": now.Unix(), "exp": now.Add(idpIDTokenLife).Unix(),
		"email": code.subject + "@example.com",
	}
	if code.nonce != "" {
		claims["nonce"] = code.nonce
	}
	idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	idToken.Header["kid"] = idpKeyID
	signed, err := idToken.SignedString(p.key)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	body := map[string]any{
		"access_token": randomToken("idp-at-"), "token_type": "Bearer",
		"expires_in": int(idpIDTokenLife.Seconds()), "id_token": signed,
	}
	if code.offline {
		body["refresh_token"] = randomToken("idp-rt-")
		p.mu.Lock()
		p.offline[code.subject]++
		p.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, body)
}

// authenticateClient checks client_secret_basic or client_secret_post.
func (p *IdP) authenticateClient(r *http.Request) (string, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get(paramClientID), r.PostForm.Get("client_secret")
	}
	p.mu.Lock()
	client, ok := p.clients[id]
	p.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(client.secret)) != 1 {
		return "", false
	}
	return id, true
}
