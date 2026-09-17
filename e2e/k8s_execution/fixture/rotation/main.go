//go:build kind_execution_e2e

// Command rotation creates synthetic execution-provider rotation candidates.
// It consumes only fixture material generated for the current qualification run.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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
	initial, out := os.Args[1], os.Args[2]
	must(os.MkdirAll(out, 0o700))
	now := time.Now().UTC()
	oldCA := read(filepath.Join(initial, "ca.crt"))
	oldGrant := readGrant(filepath.Join(initial, "grant-key.pem"))

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

	pub2, key2, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	der2, err := x509.MarshalPKCS8PrivateKey(key2)
	must(err)
	write(filepath.Join(out, "grant-k2.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der2}))

	writeManifest(filepath.Join(out, "invalid-same-generation.json"), manifest(1, "changed-policy.invalid", "k1", []keyEntry{key("k1", 1, "grant-k1.pem", oldGrant.Public().(ed25519.PublicKey), "active", now.Add(5*time.Hour))}, "tls.crt", "tls.key", "clients.pem"))
	keys := []keyEntry{
		key("k1", 1, "grant-k1.pem", oldGrant.Public().(ed25519.PublicKey), "active", now.Add(5*time.Hour)),
		key("k2", 2, "grant-k2.pem", pub2, "active", now.Add(5*time.Hour)),
	}
	writeManifest(filepath.Join(out, "bridge.json"), manifest(2, "mecatl-execution", "k2", keys, "tls.crt", "tls.key", "clients.pem"))
	keys[0].State = "revoked"
	writeManifest(filepath.Join(out, "final.json"), manifest(3, "mecatl-execution", "k2", keys, "tls.crt", "tls.key", "clients.pem"))
	writeManifest(filepath.Join(out, "restore-fixture-clients.json"), manifest(4, "mecatl-execution", "k2", keys, "tls.crt", "tls.key", "clients.pem"))
}

type keyEntry struct {
	ID, File, Fingerprint, State string
	Version                      uint64
	ActivateAt, VerifyUntil      time.Time
}

func key(id string, version uint64, file string, pub ed25519.PublicKey, state string, until time.Time) keyEntry {
	sum := sha256.Sum256(pub)
	return keyEntry{id, file, hex.EncodeToString(sum[:]), state, version, time.Now().UTC().Add(-time.Minute), until}
}

func manifest(generation uint64, audience, active string, keys []keyEntry, cert, privateKey, clients string) map[string]any {
	entries := make([]any, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, map[string]any{"id": k.ID, "version": k.Version, "file": k.File, "publicKeySHA256": k.Fingerprint, "activateAt": k.ActivateAt.Format(time.RFC3339), "verifyUntil": k.VerifyUntil.Format(time.RFC3339), "state": k.State})
	}
	return map[string]any{
		"version": 1, "generation": generation, "issuer": "https://mecatl.execution.test", "audience": audience, "activeKeyID": active, "grantTTL": "1m", "clockSkew": "5s", "keys": entries,
		"tls":     map[string]any{"certificateFile": cert, "privateKeyFile": privateKey, "clientCAFile": clients},
		"clients": []any{map[string]any{"uri": "spiffe://mecatl.test/client/mecak8s", "mayAttestOwner": true, "administrator": true}, map[string]any{"uri": "spiffe://mecatl.test/client/intruder", "mayAttestOwner": true, "administrator": false}},
	}
}

func writeManifest(path string, value any) {
	data, err := json.Marshal(value)
	must(err)
	write(path, append(data, '\n'))
}
func readGrant(path string) ed25519.PrivateKey {
	block, _ := pem.Decode(read(path))
	if block == nil {
		panic("invalid synthetic grant key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	must(err)
	out, ok := key.(ed25519.PrivateKey)
	if !ok {
		panic("unexpected synthetic grant key type")
	}
	return out
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
