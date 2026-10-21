// Package identityharness stands in, offline, for the external parties of the
// agent identity credential flow: the upstream company IdP and AS-1, Mecatl's
// sign-in authorization server. It follows
// docs/drafts/agent-identity/credentials-and-lifecycle.md for every step it
// covers, so identity broker tests and Mecatl composition tests share one
// stand-in instead of each inventing its own.
//
// The harness covers user sign-in: authorization code with PKCE at AS-1, which
// signs the user in at the upstream IdP as an ordinary OIDC client, and AS-1
// access tokens as design step (1) describes them. Later work adds the
// session-open exchange, ID-JAG issuance, refresh-token revocation, broker
// client authentication, and a gateway stand-in, each in its own files. AS-2 is
// deliberately absent: tests use the real embedded ToolHive authorization
// server for it.
//
// Everything runs on loopback TLS servers that close when the test ends. The
// harness is test code; production packages never import it.
package identityharness

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

// faults holds one-shot failures a test forces on a stand-in endpoint.
type faults struct {
	mu      sync.Mutex
	pending map[string][]Fault
}

// Fault is a failure a stand-in returns once instead of its normal response.
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
