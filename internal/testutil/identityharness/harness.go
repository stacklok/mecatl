// Package identityharness is what tests of the agent identity credential
// flow need offline around the real authorization servers: AS-1 and AS-2 are
// ToolHive's embedded authorization servers, run by the tests themselves
// (AS-1 through as1servertest), and the harness supplies the rest, following
// docs/drafts/agent-identity/credentials-and-lifecycle.md for every step it
// covers:
//
//   - the upstream company IdP, which AS-1 signs users in at as an ordinary
//     OIDC client (idp.go);
//   - a browser that keeps cookies and approves ToolHive's consent page
//     (browser.go);
//   - FaultTransport, which fails, drops, loses, duplicates, or intercepts
//     ID-JAG and redemption requests on the caller's side of any
//     authorization server (faulttransport.go);
//   - a gateway stand-in, a minimal MCP server that needs a bearer token and
//     can reject it or ask the user to connect a server, until #2537 and
//     #2812 (gateway.go);
//   - the loopback TLS certificate, CA, signing-key, and upstream-client
//     helpers a real AS-1 needs to run against the IdP (harness.go).
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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Values shared by the tests that use the harness.
const (
	// DefaultAudience is the Mecatl API identifier AS-1 access tokens carry in
	// aud. mecak8s must accept the same value (--oidc-audience).
	DefaultAudience = "https://mecatl.test/api"
	// DefaultClientID is the TUI's public client ID at AS-1.
	DefaultClientID = "mecatui"
	// DefaultRedirectURI is a redirect URI for DefaultClientID.
	DefaultRedirectURI = "http://127.0.0.1:8765/callback"
	// Alice and Bob are the company identities the upstream IdP knows by
	// default. Alice is the default user.
	Alice = "alice-204"
	Bob   = "bob-317"
)

// Options configures a Harness. It has no settings yet; the IdP knows Alice,
// its default user, and Bob.
type Options struct{}

// Harness is a running set of stand-ins. Create it with New.
type Harness struct {
	IdP *IdP

	certPEM []byte
}

// Owner is a company identity: the upstream IdP's issuer and subject, as in
// an AS-1 access token's sub_id.
type Owner struct {
	Issuer  string `json:"iss"`
	Subject string `json:"sub"`
}

// New starts the stand-ins and stops them when t ends.
func New(t testing.TB, _ Options) *Harness {
	t.Helper()
	idp := newIdP(t)
	// httptest serves every TLS server with the same loopback certificate, so
	// one client and one CA file cover every stand-in.
	return &Harness{
		IdP:     idp,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.server.Certificate().Raw}),
	}
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

// WriteCACert writes the PEM certificate that verifies every stand-in to a
// file in dir and returns its path, for a server's --oidc-ca-cert-file flag.
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

// ReadSigningKey reads the private key WriteSigningKey wrote to file.
func ReadSigningKey(t testing.TB, file string) *ecdsa.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(file)
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
	return key
}

// ResignAccessToken returns token, a JWT signed with the key WriteSigningKey
// wrote to keyFile, with its claims changed by edit and signed again with that
// key under the same header with alg ES256, so its issuer verifies it and
// judges it by the changed claims.
func ResignAccessToken(t testing.TB, keyFile, token string, edit func(jwt.MapClaims)) string {
	t.Helper()
	return ResignToken(t, ReadSigningKey(t, keyFile), token, edit, nil)
}

// ResignToken returns token, a JWT, with its claims changed by claims and its
// JOSE header by header, either of which may be nil, signed again with key.
// The header keeps token's other parameters, such as kid and typ, but its alg
// is ES256, the key's, unless header changes it.
func ResignToken(t testing.TB, key *ecdsa.PrivateKey, token string, claims func(jwt.MapClaims), header func(map[string]any)) string {
	t.Helper()
	edited := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser().ParseUnverified(token, edited)
	if err != nil {
		t.Fatalf("identityharness: parse token: %v", err)
	}
	if claims != nil {
		claims(edited)
	}
	resigned := jwt.NewWithClaims(jwt.SigningMethodES256, edited)
	resigned.Header = maps.Clone(parsed.Header)
	resigned.Header["alg"] = jwt.SigningMethodES256.Alg()
	if header != nil {
		header(resigned.Header)
	}
	signed, err := resigned.SignedString(key)
	if err != nil {
		t.Fatalf("identityharness: sign token: %v", err)
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
