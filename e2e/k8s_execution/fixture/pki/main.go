//go:build kind_execution_e2e

// Command pki generates synthetic, short-lived qualification identities.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: pki OUTPUT_DIRECTORY")
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		panic(err)
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	now := time.Now().UTC()
	// This isolated fixture CA is deliberately bounded but outlives retained
	// qualification clusters; the CA Issuer does not renew its own root.
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "mecatl execution qualification CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(14 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	must(err)
	write(filepath.Join(dir, "ca.key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)}), 0o600)
	write(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	must(issue(dir, "provider", ca, caKey, []string{"mecatl-execution", "mecatl-execution.execution-qualification.svc", "mecatl-execution.execution-qualification.svc.cluster.local"}, "", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}))
	must(issue(dir, "mecak8s", ca, caKey, nil, "spiffe://mecatl.test/client/mecak8s", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	must(issue(dir, "intruder", ca, caKey, nil, "spiffe://mecatl.test/client/intruder", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	for _, name := range []string{"operations", "wrong-scope"} {
		must(issue(dir, name, ca, caKey, nil, "spiffe://mecatl.test/client/"+name, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	}
	manifest, err := json.Marshal(map[string]any{
		"version": 1, "generation": 1,
		"tls": map[string]any{"certificateFile": "tls.crt", "privateKeyFile": "tls.key", "clientCAFile": "clients.pem"},
		"clients": []any{
			map[string]any{"uri": "spiffe://mecatl.test/client/mecak8s", "mayAttestOwner": true, "administrator": true, "executionTemplates": []string{"go", "operator-utility", "quota-cas", "quota-kube"}},
			map[string]any{"uri": "spiffe://mecatl.test/client/intruder", "mayAttestOwner": true, "administrator": false},
			map[string]any{"uri": "spiffe://mecatl.test/client/operations", "mayAttestOwner": true, "administrator": true, "administratorFor": []string{"spiffe://mecatl.test/client/mecak8s"}},
			map[string]any{"uri": "spiffe://mecatl.test/client/wrong-scope", "mayAttestOwner": true, "administrator": true, "administratorFor": []string{"spiffe://mecatl.test/client/intruder"}},
		},
	})
	must(err)
	write(filepath.Join(dir, "manifest.json"), append(manifest, '\n'), 0o600)
	must(issue(dir, "oidc", ca, caKey, []string{"oidc-issuer", "oidc-issuer.execution-qualification.svc", "oidc-issuer.execution-qualification.svc.cluster.local"}, "", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}))
	jwtKey, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	jwtDER, err := x509.MarshalPKCS8PrivateKey(jwtKey)
	must(err)
	write(filepath.Join(dir, "oidc-jwt-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: jwtDER}), 0o600)
}

func issue(dir, name string, ca *x509.Certificate, caKey *rsa.PrivateKey, dns []string, uri string, usages []x509.ExtKeyUsage) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	cert := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(7 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: usages, DNSNames: dns}
	if uri != "" {
		u, err := url.Parse(uri)
		if err != nil {
			return err
		}
		cert.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	write(filepath.Join(dir, name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	write(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	return nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	must(err)
	return n
}
func write(path string, data []byte, mode os.FileMode) { must(os.WriteFile(path, data, mode)) }
func must(err error) {
	if err != nil {
		panic(err)
	}
}
