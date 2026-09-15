//go:build kind_execution_e2e

// Command pki generates synthetic, short-lived qualification identities.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
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
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "mecatl execution qualification CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(6 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	must(err)
	write(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	must(issue(dir, "provider", ca, caKey, []string{"mecatl-execution", "mecatl-execution.execution-qualification.svc", "mecatl-execution.execution-qualification.svc.cluster.local"}, "", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}))
	must(issue(dir, "mecak8s", ca, caKey, nil, "spiffe://mecatl.test/client/mecak8s", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	must(issue(dir, "intruder", ca, caKey, nil, "spiffe://mecatl.test/client/intruder", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	grantPub, grantKey, err := ed25519.GenerateKey(rand.Reader)
	_ = grantPub
	must(err)
	grantDER, err := x509.MarshalPKCS8PrivateKey(grantKey)
	must(err)
	write(filepath.Join(dir, "grant-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: grantDER}), 0o600)
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
	cert := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(6 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: usages, DNSNames: dns}
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
