package mcpbroker

import (
	"crypto"
	"net/http"
	"slices"
)

// AS2 is the embedded authorization server set up as the identity broker's
// AS-2: the broker client is registered there and the vMCP's resource is its
// endpoint, distinct from the issuer. The identity broker calls AS-2; this
// package only serves it.
type AS2 struct {
	// Issuer is AS-2's issuer: the client-assertion audience and an ID-JAG's
	// aud.
	Issuer string
	// TokenEndpoint is AS-2's token endpoint.
	TokenEndpoint string
	// Resource is the vMCP resource, an ID-JAG's resource and the audience of
	// the gateway tokens AS-2 issues.
	Resource string

	// signingKeys are the public keys of the signing key and fallbacks the
	// embedded authorization server loaded.
	signingKeys []crypto.PublicKey
	// handler serves AS-2's token endpoint and nothing else.
	handler http.Handler
}

// as2TokenPath is the path of AS-2's token endpoint on the mux.
const as2TokenPath = toolHiveBasePath + "/oauth/token"

// newAS2 is AS-2 at issuer for resource, its token endpoint served by
// embedded, the embedded authorization server's handler as mounted under
// toolHiveBasePath, signing with the keys whose public keys are signingKeys.
func newAS2(issuer, resource string, embedded http.Handler, signingKeys []crypto.PublicKey) *AS2 {
	return &AS2{Issuer: issuer, TokenEndpoint: as2TokenEndpoint(issuer), Resource: resource, signingKeys: signingKeys, handler: tokenEndpointOnly(embedded)}
}

// SigningPublicKeys returns the public keys of the signing key and fallbacks
// AS-2 loaded, which may differ from what an earlier read of the same files
// returned, so composition can refuse a key another role uses.
func (a *AS2) SigningPublicKeys() []crypto.PublicKey { return slices.Clone(a.signingKeys) }

// Handler serves AS-2's token endpoint, for composition to reach AS-2 in
// process while the identity broker shares this process. It answers only a
// POST to the token endpoint's path: any other path is 404 and any other
// method 405, so the embedded server's login and discovery routes are not
// reachable through it.
func (a *AS2) Handler() http.Handler { return a.handler }

func tokenEndpointOnly(embedded http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != as2TokenPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		embedded.ServeHTTP(w, r)
	})
}
