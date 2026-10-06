//go:build kind_execution_e2e

// Command oidcissuer is a synthetic OIDC issuer for the offline Kind qualification.
package main

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	var certFile, keyFile, signingFile, issuer, audience string
	flag.StringVar(&certFile, "tls-cert", "", "TLS certificate")
	flag.StringVar(&keyFile, "tls-key", "", "TLS key")
	flag.StringVar(&signingFile, "signing-key", "", "RSA PKCS8 signing key")
	flag.StringVar(&issuer, "issuer", "", "exact issuer URL")
	flag.StringVar(&audience, "audience", "mecatl", "token audience")
	flag.Parse()
	if certFile == "" || keyFile == "" || signingFile == "" || issuer == "" {
		log.Fatal("all identity flags are required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	must(err)
	raw, err := os.ReadFile(signingFile)
	must(err)
	block, _ := pem.Decode(raw)
	if block == nil {
		log.Fatal("invalid signing key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	must(err)
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		log.Fatal("signing key is not RSA")
	}
	kidRaw := sha256.Sum256(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	kid := base64.RawURLEncoding.EncodeToString(kidRaw[:12])
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Query().Get("sub")
		if sub != "alice" && sub != "bob" {
			http.Error(w, "unknown fixture subject", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer, "aud": audience, "sub": sub, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, "signing failed", 500)
			return
		}
		writeJSON(w, map[string]string{"id_token": signed})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Addr: ":8443", Handler: mux, ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}}
	ln, err := tls.Listen("tcp", server.Addr, server.TLSConfig)
	must(err)
	must(server.Serve(ln))
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Print("response encoding failed")
	}
}
func must(err error) {
	if err != nil {
		log.Fatal(fmt.Errorf("fixture startup: %w", err))
	}
}
