package executionexecutor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testProviderURI = "spiffe://mecatl.test/provider/executor-client"

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "platform"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (ca testCA) issue(t *testing.T, uri string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	u, _ := url.Parse(uri)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		URIs:         []*url.URL{u},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// project mimics the kubelet atomic writer: files under a fresh ..data target.
func project(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	genDir, err := os.MkdirTemp(dir, "..gen")
	if err != nil {
		t.Fatal(err)
	}
	gen := filepath.Base(genDir)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, gen, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); err != nil {
			if err := os.Symlink(filepath.Join("..data", name), link); err != nil {
				t.Fatal(err)
			}
		}
	}
	tmp := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink(gen, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
}

func trustFiles(ca testCA, uri string) map[string][]byte {
	return map[string][]byte{"ca.crt": ca.pem, "client-uri": []byte(uri)}
}

func writeServerKeyPair(t *testing.T, dir string) {
	t.Helper()
	ca := newTestCA(t)
	cert := ca.issue(t, "urn:mecatl:execution-environment:env", x509.ExtKeyUsageServerAuth)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	files := map[string]*pem.Block{
		"tls.crt": {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		"tls.key": {Type: "PRIVATE KEY", Bytes: keyDER},
	}
	for name, block := range files {
		data := pem.EncodeToMemory(block)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func startTLSServer(t *testing.T, trustDir string) string {
	t.Helper()
	certDir := t.TempDir()
	writeServerKeyPair(t, certDir)
	cfg, err := ServerTLSConfig(certDir, trustDir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if conn.(*tls.Conn).Handshake() == nil {
					_, _ = conn.Write([]byte("ok"))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func callLocal(addr string, certs ...tls.Certificate) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // The server pin is the dialer's concern.
		Certificates:       certs,
	})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	reply, err := io.ReadAll(conn)
	if err == nil && string(reply) != "ok" {
		err = errors.New("server refused the caller")
	}
	return err
}

func TestServerTLSAdmitsOnlyTheProviderFromLocalhost(t *testing.T) {
	ca := newTestCA(t)
	trustDir := t.TempDir()
	project(t, trustDir, trustFiles(ca, testProviderURI))
	addr := startTLSServer(t, trustDir)

	provider := ca.issue(t, testProviderURI, x509.ExtKeyUsageClientAuth)
	if err := callLocal(addr, provider); err != nil {
		t.Fatalf("provider rejected: %v", err)
	}
	rejected := map[string][]tls.Certificate{
		"no certificate": nil,
		"foreign CA":     {newTestCA(t).issue(t, testProviderURI, x509.ExtKeyUsageClientAuth)},
		"wrong URI":      {ca.issue(t, "spiffe://mecatl.test/shell", x509.ExtKeyUsageClientAuth)},
		"server EKU":     {ca.issue(t, testProviderURI, x509.ExtKeyUsageServerAuth)},
	}
	for name, certs := range rejected {
		if err := callLocal(addr, certs...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestServerTLSReloadsRotatedProviderTrust(t *testing.T) {
	oldCA, newCA := newTestCA(t), newTestCA(t)
	trustDir := t.TempDir()
	project(t, trustDir, trustFiles(oldCA, testProviderURI))
	addr := startTLSServer(t, trustDir)
	oldClient := oldCA.issue(t, testProviderURI, x509.ExtKeyUsageClientAuth)
	newClient := newCA.issue(t, testProviderURI, x509.ExtKeyUsageClientAuth)
	if err := callLocal(addr, oldClient); err != nil {
		t.Fatal(err)
	}

	project(t, trustDir, trustFiles(newCA, testProviderURI))
	if err := callLocal(addr, newClient); err != nil {
		t.Fatalf("rotated CA rejected: %v", err)
	}
	if err := callLocal(addr, oldClient); err == nil {
		t.Fatal("retired CA accepted")
	}

	project(t, trustDir, trustFiles(newCA, "spiffe://mecatl.test/provider/next"))
	if err := callLocal(addr, newClient); err == nil {
		t.Fatal("previous client URI accepted")
	}
	next := newCA.issue(t, "spiffe://mecatl.test/provider/next", x509.ExtKeyUsageClientAuth)
	if err := callLocal(addr, next); err != nil {
		t.Fatalf("rotated client URI rejected: %v", err)
	}

	garbage := testCA{pem: []byte("garbage")}
	project(t, trustDir, trustFiles(garbage, "spiffe://mecatl.test/provider/next"))
	if err := callLocal(addr, next); err == nil {
		t.Fatal("client accepted with an unreadable trust bundle")
	}
}

func TestServerTLSConfigRequiresValidMounts(t *testing.T) {
	ca := newTestCA(t)
	certDir := t.TempDir()
	writeServerKeyPair(t, certDir)
	cases := map[string]map[string][]byte{
		"missing URI":       {"ca.crt": ca.pem},
		"missing CA":        {"client-uri": []byte(testProviderURI)},
		"non-canonical URI": trustFiles(ca, "SPIFFE://mecatl.test/provider"),
	}
	for name, files := range cases {
		trustDir := t.TempDir()
		project(t, trustDir, files)
		if _, err := ServerTLSConfig(certDir, trustDir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	trustDir := t.TempDir()
	project(t, trustDir, trustFiles(ca, testProviderURI))
	if _, err := ServerTLSConfig(t.TempDir(), trustDir); err == nil {
		t.Error("missing server key pair accepted")
	}
}
