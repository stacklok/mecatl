//go:build kind_execution_e2e

// Command rotation creates synthetic execution-provider rotation candidates.
// It consumes only fixture material generated for the current qualification run.
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
	if len(os.Args) != 3 {
		panic("usage: rotation INITIAL_PKI OUTPUT_DIRECTORY")
	}
	generate(os.Args[1], os.Args[2])
}

func generate(initial, out string) {
	must(os.MkdirAll(out, 0o700))
	now := time.Now().UTC()
	oldCA := read(filepath.Join(initial, "ca.crt"))

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "mecatl execution qualification rotation CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(6 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	must(err)
	newCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	write(filepath.Join(out, "new-ca.crt"), newCA)
	write(filepath.Join(out, "client-roots.pem"), append(append([]byte{}, oldCA...), newCA...))
	write(filepath.Join(out, "bridge-clients.pem"), append(append([]byte{}, oldCA...), newCA...))
	write(filepath.Join(out, "final-clients.pem"), newCA)
	issue(out, "provider-new", ca, caKey, []string{"mecatl-execution", "mecatl-execution.execution-qualification.svc", "mecatl-execution.execution-qualification.svc.cluster.local"}, "", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	issue(out, "mecak8s-new", ca, caKey, nil, "spiffe://mecatl.test/client/mecak8s", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	writeManifest(filepath.Join(out, "invalid-same-generation.json"), manifest(1, false, "tls.crt", "tls.key", "clients.pem"))
	writeManifest(filepath.Join(out, "bridge.json"), manifest(2, true, "tls.crt", "tls.key", "clients.pem"))
	writeManifest(filepath.Join(out, "final.json"), manifest(3, true, "tls.crt", "tls.key", "clients.pem"))
	writeManifest(filepath.Join(out, "restore-fixture-clients.json"), manifest(4, true, "tls.crt", "tls.key", "clients.pem"))
}

func manifest(generation uint64, enabled bool, cert, privateKey, clients string) map[string]any {
	templates := []string{"go", "incompatible-derivative", "operator-utility", "quota-cas", "quota-kube"}
	if !enabled {
		templates = nil
	}
	return map[string]any{
		"version": 1, "generation": generation,
		"tls": map[string]any{"certificateFile": cert, "privateKeyFile": privateKey, "clientCAFile": clients},
		"clients": []any{
			map[string]any{"uri": "spiffe://mecatl.test/client/mecak8s", "mayAttestOwner": enabled, "administrator": true, "executionTemplates": templates},
			map[string]any{"uri": "spiffe://mecatl.test/client/intruder", "mayAttestOwner": true, "administrator": false},
			map[string]any{"uri": "spiffe://mecatl.test/client/operations", "mayAttestOwner": true, "administrator": true, "administratorFor": []string{"spiffe://mecatl.test/client/mecak8s"}},
			map[string]any{"uri": "spiffe://mecatl.test/client/wrong-scope", "mayAttestOwner": true, "administrator": true, "administratorFor": []string{"spiffe://mecatl.test/client/intruder"}},
			map[string]any{"uri": "spiffe://mecatl.test/client/qualification", "mayAttestOwner": true, "administrator": true, "executionTemplates": []string{"go"}},
		},
	}
}

func writeManifest(path string, value any) {
	data, err := json.Marshal(value)
	must(err)
	write(path, append(data, '\n'))
}
func issue(dir, name string, ca *x509.Certificate, caKey *rsa.PrivateKey, dns []string, uri string, usages []x509.ExtKeyUsage) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	cert := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(6 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: usages, DNSNames: dns}
	if uri != "" {
		u, err := url.Parse(uri)
		must(err)
		cert.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	must(err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	must(err)
	write(filepath.Join(dir, name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	write(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}
func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	must(err)
	return n
}
func read(path string) []byte        { data, err := os.ReadFile(path); must(err); return data }
func write(path string, data []byte) { must(os.WriteFile(path, data, 0o600)) }
func must(err error) {
	if err != nil {
		panic(err)
	}
}
