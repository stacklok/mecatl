// Package identityharness stands in, offline, for the external parties of the
// agent identity credential flow: the upstream company IdP and AS-1, Mecatl's
// sign-in authorization server. It follows
// docs/drafts/agent-identity/credentials-and-lifecycle.md for every step it
// covers, so identity broker tests and Mecatl composition tests share one
// stand-in instead of each inventing its own.
//
// The harness covers:
//
//   - user sign-in: authorization code with PKCE at AS-1, which signs the
//     user in at the upstream IdP as an ordinary OIDC client, and AS-1 access
//     tokens as design step (1) describes them (as1.go, signin.go);
//   - broker client authentication, design step (5): the confidential client
//     BrokerClientID with private_key_jwt, checking the registered key, iss
//     and sub, aud equal to AS-1's issuer, exp and iat with
//     ClientAssertionClockSkew allowed, exp - iat, and a jti used once and
//     kept until exp plus the skew; a client secret is refused (clientauth.go);
//   - AS-1's conversion of a sign-in at session open, design step (3), kept
//     only as the grant fixture for ID-JAG tests until ToolHive's AS-1 issues
//     ID-JAGs (stacklok/toolhive#6898, stacklok/toolhive#6899). Session-open
//     behaviour itself, including refusals and faults, is tested against the
//     real AS-1 (as1servertest). The RFC 8693 exchange turns an AS-1 access
//     token into a non-rotating refresh token of a generic grant
//     (stacklok/toolhive#6897) that names the client and the user, never a
//     Mecatl session or owner, and issues only ID-JAGs. The request carries
//     one exchange-grant entry whose optional expires_at AS-1 caps at its
//     maximum and echoes. Each access-token jti converts once; a refused
//     access token gets subject_token_invalid, not the real AS-1's reasons.
//     The grant's refresh token is refused at the refresh-token grant
//     (sessionopen.go);
//   - RFC 7009 revocation of grant refresh tokens, until
//     stacklok/toolhive#6966 and stacklok/toolhive#6972 (revoke.go);
//   - ScriptedBroker, a hand-driven caller holding the broker's credentials
//     for exercising AS-1 directly (broker.go). Production code under test
//     uses its own client, not ScriptedBroker;
//   - one-shot IdP faults for sign-in (idp.go);
//   - a browser for tests hosting the real AS-1, which keeps cookies and
//     approves ToolHive's consent page (browser.go).
//
// Keeping one live refresh token per session, checking the owner, and
// refusing to reopen a closed session are the broker's job, so AS-1 does
// none of them. Later work adds ID-JAG issuance (with the reasons revoked,
// parent_revoked, and not_issuable on use), child grants, client
// refresh-token revocation, and a gateway stand-in, each in its own files.
// Until ID-JAG issuance exists, Grant reports what a later use would get.
// AS-2 is deliberately absent: tests use the real embedded ToolHive
// authorization server for it.
//
// Where the design is silent the harness guesses, marked "Guess:" in the code:
//
//   - Revocation is RFC 7009 at {issuer}/revoke, callable only by the
//     authenticated broker. Revoking a grant's refresh token revokes the
//     grant; any other value changes nothing and answers 200.
//   - A client assertion with an explicit typ other than
//     client-authentication+jwt or JWT (golang-jwt's default) is refused; an
//     untyped one is accepted; only ES256 is accepted. Because the harness
//     accepts JWT, the production broker's own tests must check the typ it
//     emits.
//
// Everything runs on loopback TLS servers that close when the test ends. The
// harness is test code; production packages never import it.
package identityharness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Defaults used when Options leaves a field empty.
const (
	// DefaultAudience is the Mecatl API identifier AS-1 access tokens carry in
	// aud. mecak8s must accept the same value (--oidc-audience).
	DefaultAudience = "https://mecatl.test/api"
	// DefaultClientID is the public client registered when Options.Clients is
	// empty.
	DefaultClientID = "mecatui"
	// DefaultRedirectURI is DefaultClientID's only redirect URI.
	DefaultRedirectURI = "http://127.0.0.1:8765/callback"
	// DefaultAccessTokenLifetime is the AS-1 access token lifetime.
	DefaultAccessTokenLifetime = 15 * time.Minute
	// Alice and Bob are the company identities the upstream IdP knows by
	// default. Alice is the default user.
	Alice = "alice-204"
	Bob   = "bob-317"
)

// Client is a public OAuth client registered at AS-1, such as the TUI or
// Studio. Redirect URIs match exactly.
type Client struct {
	ID           string
	RedirectURIs []string
}

// Options configures a Harness. The zero value is usable.
type Options struct {
	// Audience is the Mecatl API identifier. Empty selects DefaultAudience.
	Audience string
	// Clients are AS-1's public clients. Empty registers DefaultClientID with
	// DefaultRedirectURI.
	Clients []Client
	// Users are the company identities (IdP subjects) the upstream IdP can
	// sign in. The first is the default user. Empty selects Alice and Bob.
	Users []string
	// AccessTokenLifetime is the AS-1 access token lifetime. Zero selects
	// DefaultAccessTokenLifetime.
	AccessTokenLifetime time.Duration
	// Now is the clock for every issued token and code. Nil selects time.Now.
	Now func() time.Time
	// BrokerKeys are public keys registered for BrokerClientID in addition
	// to the harness's own broker key, DefaultBrokerKeyID. Each needs a
	// unique, nonempty KeyID other than DefaultBrokerKeyID and a P-256 key.
	BrokerKeys []BrokerKey
	// ClientAssertionMaxLifetime is the largest exp - iat AS-1 accepts in a
	// client assertion. Zero selects DefaultClientAssertionMaxLifetime.
	ClientAssertionMaxLifetime time.Duration
	// GrantMaxLifetime is AS-1's maximum lifetime for a grant that issues
	// ID-JAGs: the expiry when an exchange asks for none, and the cap on one
	// it asks for. Zero selects DefaultGrantMaxLifetime.
	GrantMaxLifetime time.Duration
}

// Harness is a running set of stand-ins. Create it with New.
type Harness struct {
	IdP *IdP
	AS1 *AS1

	certPEM []byte
}

// New starts the stand-ins and stops them when t ends.
func New(t testing.TB, opts Options) *Harness {
	t.Helper()
	if opts.Audience == "" {
		opts.Audience = DefaultAudience
	}
	if len(opts.Clients) == 0 {
		opts.Clients = []Client{{ID: DefaultClientID, RedirectURIs: []string{DefaultRedirectURI}}}
	}
	if len(opts.Users) == 0 {
		opts.Users = []string{Alice, Bob}
	}
	if opts.AccessTokenLifetime <= 0 {
		opts.AccessTokenLifetime = DefaultAccessTokenLifetime
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ClientAssertionMaxLifetime <= 0 {
		opts.ClientAssertionMaxLifetime = DefaultClientAssertionMaxLifetime
	}
	if opts.GrantMaxLifetime <= 0 {
		opts.GrantMaxLifetime = DefaultGrantMaxLifetime
	}

	idp := newIdP(t, opts)
	as1 := newAS1(t, opts, idp)
	// httptest serves every TLS server with the same loopback certificate, so
	// one client and one CA file cover both stand-ins.
	h := &Harness{
		IdP:     idp,
		AS1:     as1,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.server.Certificate().Raw}),
	}
	as1.http = h.HTTPClient()
	return h
}

// HTTPClient returns a client that trusts the stand-ins' TLS certificate and
// does not follow redirects.
func (h *Harness) HTTPClient() *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(h.certPEM)
	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

// Owner returns the company identity of user at the upstream IdP, as an
// access token's sub_id carries it.
func (h *Harness) Owner(user string) Owner { return Owner{Issuer: h.IdP.Issuer(), Subject: user} }

// CACertPEM returns the PEM certificate that verifies every stand-in.
func (h *Harness) CACertPEM() []byte { return append([]byte(nil), h.certPEM...) }

// WriteCACert writes CACertPEM to a file in dir and returns its path, for a
// server's --oidc-ca-cert-file flag.
func (h *Harness) WriteCACert(t testing.TB, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "identityharness-ca.pem")
	if err := os.WriteFile(path, h.certPEM, 0o600); err != nil {
		t.Fatalf("identityharness: write CA certificate: %v", err)
	}
	return path
}

// WriteSigningKey writes a new P-256 private key in PEM to a file in dir and
// returns its path, for a real AS-1's signing key file.
func (*Harness) WriteSigningKey(t testing.TB, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("identityharness: signing key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("identityharness: marshal signing key: %v", err)
	}
	path := filepath.Join(dir, "as1-signing-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("identityharness: write signing key: %v", err)
	}
	return path
}

// ResignAccessToken returns token, a JWT signed with the key WriteSigningKey
// wrote to keyFile, with its claims changed by edit and signed again with that
// key under the same header, so its issuer verifies it and judges it by the
// changed claims.
func ResignAccessToken(t testing.TB, keyFile, token string, edit func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		t.Fatalf("identityharness: parse access token: %v", err)
	}
	edit(claims)
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("identityharness: read signing key: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("identityharness: the signing key file holds no PEM block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("identityharness: parse signing key: %v", err)
	}
	resigned := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	resigned.Header = maps.Clone(parsed.Header)
	signed, err := resigned.SignedString(key)
	if err != nil {
		t.Fatalf("identityharness: sign access token: %v", err)
	}
	return signed
}

// RegisterAS1Upstream registers a real AS-1 at as1Issuer as the upstream
// client clientID at the IdP with a fresh secret, and writes the secret to a
// file in dir, returning its path for AS-1's upstream client secret file.
// Registering clientID again replaces its secret.
func (h *Harness) RegisterAS1Upstream(t testing.TB, dir, clientID, as1Issuer string) (secretFile string) {
	t.Helper()
	secret := rand.Text()
	secretFile = filepath.Join(dir, "upstream-client-secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatalf("identityharness: write upstream client secret: %v", err)
	}
	h.IdP.RegisterClient(clientID, secret, as1Issuer+"/oauth/callback")
	return secretFile
}

// WriteServerKeyPair writes the stand-ins' loopback TLS certificate and its
// private key to files in dir and returns their paths, so a server under test
// (such as mecak8s with --tls-cert and --tls-key) is trusted wherever the
// stand-ins are, including by HTTPClient.
func (h *Harness) WriteServerKeyPair(t testing.TB, dir string) (certFile, keyFile string) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(h.IdP.server.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatalf("identityharness: marshal TLS key: %v", err)
	}
	certFile, keyFile = filepath.Join(dir, "identityharness-tls.crt"), filepath.Join(dir, "identityharness-tls.key")
	if err := os.WriteFile(certFile, h.certPEM, 0o600); err != nil {
		t.Fatalf("identityharness: write TLS certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("identityharness: write TLS key: %v", err)
	}
	return certFile, keyFile
}

// faults holds one-shot failures a test forces on an IdP endpoint.
type faults struct {
	mu      sync.Mutex
	pending map[string][]Fault
}

// Fault is a failure the IdP returns once instead of its normal response.
type Fault struct {
	// Status is the HTTP status for a token endpoint fault. Zero selects 400.
	Status int
	// Error is the OAuth error code, such as access_denied or invalid_grant.
	Error string
	// Reason starts error_description, as the design's error reasons do.
	Reason string
}

func (f *faults) push(endpoint string, fault Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		f.pending = make(map[string][]Fault)
	}
	f.pending[endpoint] = append(f.pending[endpoint], fault)
}

func (f *faults) pop(endpoint string) (Fault, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.pending[endpoint]
	if len(queue) == 0 {
		return Fault{}, false
	}
	f.pending[endpoint] = queue[1:]
	return queue[0], true
}
