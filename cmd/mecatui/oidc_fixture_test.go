package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// oidcTestIssuer serves the discovery and JWKS endpoints required by OIDC test clients.
type oidcTestIssuer struct {
	Server *httptest.Server
	Key    *rsa.PrivateKey
	kid    string
}

func newOIDCTestIssuer(t *testing.T, kid string, tokenHandler http.HandlerFunc) *oidcTestIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &oidcTestIssuer{Key: key, kid: kid}
	issuer.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer.Server.URL, "authorization_endpoint": issuer.Server.URL + "/authorize",
				"token_endpoint": issuer.Server.URL + "/token", "jwks_uri": issuer.Server.URL + "/keys",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": issuer.kid,
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		case "/token":
			if tokenHandler != nil {
				tokenHandler(w, r)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Server.Close)
	return issuer
}

func (issuer *oidcTestIssuer) writeCA(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
}
