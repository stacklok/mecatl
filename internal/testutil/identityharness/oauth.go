package identityharness

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"strings"
)

// OAuth protocol values shared by the stand-ins.
const (
	paramClientID          = "client_id"
	responseTypeCode       = "code"
	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	pkceS256               = "S256"
)

// writeJSON writes a JSON response that must not be cached (RFC 6749 §5.1).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeOAuthError writes an RFC 6749 §5.2 error. A nonempty reason starts
// error_description, as the design's error reasons require; it is never a
// token or claim value.
func writeOAuthError(w http.ResponseWriter, status int, code, reason string) {
	if status == 0 {
		status = http.StatusBadRequest
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="token"`)
	}
	body := map[string]string{"error": code}
	if reason != "" {
		body["error_description"] = reason
	}
	writeJSON(w, status, body)
}

// redirectWithError sends an authorization error back to a validated redirect
// URI (RFC 6749 §4.1.2.1).
func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, code string) {
	q := url.Values{"error": {code}}
	if state != "" {
		q.Set("state", state)
	}
	redirect(w, r, redirectURI, q)
}

// redirect sends the user agent to redirectURI with q. Every caller passes a
// redirect URI that exactly matches one the client registered.
func redirect(w http.ResponseWriter, r *http.Request, redirectURI string, q url.Values) {
	http.Redirect(w, r, withQuery(redirectURI, q), http.StatusFound) //nolint:gosec // G710: callers pass only exact-matched registered redirect URIs.
}

func withQuery(base string, q url.Values) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q.Encode()
}

// randomToken returns an unguessable opaque value with prefix.
func randomToken(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("identityharness: random: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// s256 is the PKCE S256 code challenge for verifier (RFC 7636 §4.2).
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// validVerifier reports whether v is an RFC 7636 §4.1 code verifier.
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		unreserved := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)
		if !unreserved {
			return false
		}
	}
	return true
}

func rsaJWK(kid string, key *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func ecJWK(kid string, key *ecdsa.PublicKey) map[string]string {
	// The uncompressed P-256 point is 0x04 || X || Y, each coordinate 32
	// bytes (RFC 7518 §6.2.1.2).
	point, err := key.Bytes()
	if err != nil {
		panic("identityharness: encode EC key: " + err.Error())
	}
	return map[string]string{
		"kty": "EC", "use": "sig", "alg": "ES256", "kid": kid, "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(point[1:33]),
		"y": base64.RawURLEncoding.EncodeToString(point[33:65]),
	}
}
