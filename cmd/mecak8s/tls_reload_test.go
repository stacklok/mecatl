package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testKeyPair struct {
	cert []byte
	key  []byte
}

func TestTLSCertificateReloadLastValid(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	one := makeTestKeyPair(t, 1)
	two := makeTestKeyPair(t, 2)
	writeTestKeyPair(t, certPath, keyPath, one)

	tlsCfg, lifecycle, err := buildTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if len(tlsCfg.Certificates) != 0 {
		t.Fatal("TLS config retained a mutable static certificate")
	}
	if got := currentSerial(t, tlsCfg); got != 1 {
		t.Fatalf("initial serial = %d, want 1", got)
	}

	writeTestKeyPair(t, certPath, keyPath, two)
	awaitSerial(t, tlsCfg, 2)

	malformedChain := append(append([]byte{}, two.cert...), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("malformed DER")})...)
	if err := os.WriteFile(certPath, malformedChain, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * tlsReloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 2 {
		t.Fatalf("serial after malformed additional chain entry = %d, want last valid 2", got)
	}

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * tlsReloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 2 {
		t.Fatalf("serial after invalid rotation = %d, want last valid 2", got)
	}

	if err := os.WriteFile(certPath, one.cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, two.key, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * tlsReloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 2 {
		t.Fatalf("serial after mismatched rotation = %d, want last valid 2", got)
	}

	for i := 0; i < 12; i++ {
		writeTestKeyPair(t, certPath, keyPath, one)
	}
	awaitSerial(t, tlsCfg, 1)
}

func TestTLSReloadDiagnosticsUseBoundedReasonCodes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secret-path-marker")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	pair := makeTestKeyPair(t, 41)
	writeTestKeyPair(t, certPath, keyPath, pair)
	reloader, err := newCertificateReloader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reloader.Close()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	secret := "secret-pem-marker"
	if err := os.WriteFile(certPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	reloader.reload(certPath, keyPath)()
	text := logs.String()
	if !strings.Contains(text, `"reason":"invalid_candidate"`) {
		t.Fatalf("reload diagnostic missing reason code: %s", text)
	}
	for _, forbidden := range []string{secret, certPath, keyPath, "BEGIN CERTIFICATE"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("reload diagnostic leaked %q: %s", forbidden, text)
		}
	}
}

func TestTLSCertificateReloadRejectsInvalidChains(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	initial := makeTestKeyPair(t, 40)
	valid := makeTestChain(t, 41, false, false)
	unrelated := makeTestChain(t, 42, false, false)
	expiredIntermediate := makeTestChain(t, 43, false, true)
	expiredLeaf := makeTestChain(t, 44, true, false)
	writeTestKeyPair(t, certPath, keyPath, initial)
	reloader, err := newCertificateReloader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reloader.Close()

	writeTestKeyPair(t, certPath, keyPath, testKeyPair{cert: valid.pem(), key: valid.key})
	reloader.reload(certPath, keyPath)()
	if got := reloader.cert.Load().Leaf.SerialNumber.Int64(); got != 41 {
		t.Fatalf("valid chain serial = %d, want 41", got)
	}
	for _, tc := range []struct {
		name string
		pair testKeyPair
	}{
		{name: "unrelated", pair: testKeyPair{cert: concatPEM(valid.leaf, unrelated.intermediate, unrelated.root), key: valid.key}},
		{name: "reordered", pair: testKeyPair{cert: concatPEM(valid.leaf, valid.root, valid.intermediate), key: valid.key}},
		{name: "expired intermediate", pair: testKeyPair{cert: expiredIntermediate.pem(), key: expiredIntermediate.key}},
		{name: "expired leaf", pair: testKeyPair{cert: expiredLeaf.pem(), key: expiredLeaf.key}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeTestKeyPair(t, certPath, keyPath, tc.pair)
			reloader.reload(certPath, keyPath)()
			if got := reloader.cert.Load().Leaf.SerialNumber.Int64(); got != 41 {
				t.Fatalf("rejected chain displaced last valid certificate: serial=%d", got)
			}
		})
	}
}

func TestTLSCertificateReloadInitialValidationAndShutdown(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	one := makeTestKeyPair(t, 11)
	two := makeTestKeyPair(t, 12)
	if err := os.WriteFile(certPath, one.cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, two.key, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildTLSConfig(config{tlsCert: certPath, tlsKey: keyPath}); err == nil {
		t.Fatal("mismatched initial keypair succeeded")
	}

	writeTestKeyPair(t, certPath, keyPath, one)
	tlsCfg, lifecycle, err := buildTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestKeyPair(t, certPath, keyPath, two)
	time.Sleep(2 * tlsReloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 11 {
		t.Fatalf("serial after watcher shutdown = %d, want 11", got)
	}
}

func TestTLSHandshakeReloadsAcrossProjectedSecretSwap(t *testing.T) {
	root := t.TempDir()
	one := makeTestKeyPair(t, 21)
	two := makeTestKeyPair(t, 22)
	projectTLSVersion(t, root, "..v1", one)
	if err := os.Symlink("..v1", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "tls.crt"), filepath.Join(root, "tls.crt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "tls.key"), filepath.Join(root, "tls.key")); err != nil {
		t.Fatal(err)
	}

	tlsCfg, lifecycle, err := buildTLSConfig(config{
		tlsCert: filepath.Join(root, "tls.crt"),
		tlsKey:  filepath.Join(root, "tls.key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	serial, fingerprint := handshakeCertificate(t, tlsCfg)
	if serial != 21 {
		t.Fatalf("initial handshake serial = %d, want 21", serial)
	}

	projectTLSVersion(t, root, "..bad", testKeyPair{cert: two.cert, key: one.key})
	swapProjectedData(t, root, "..bad")
	time.Sleep(2 * tlsReloadDebounce)
	badSerial, badFingerprint := handshakeCertificate(t, tlsCfg)
	if badSerial != serial || badFingerprint != fingerprint {
		t.Fatal("invalid intermediate projection displaced the last valid certificate")
	}

	projectTLSVersion(t, root, "..v2", two)
	swapProjectedData(t, root, "..v2")
	awaitSerial(t, tlsCfg, 22)
	newSerial, newFingerprint := handshakeCertificate(t, tlsCfg)
	if newSerial != 22 || newFingerprint == fingerprint {
		t.Fatalf("reloaded handshake = serial %d fingerprint %x; want changed serial/fingerprint", newSerial, newFingerprint)
	}
}

func TestEstablishedTLSConnectionSurvivesCertificateRotation(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	one := makeTestKeyPair(t, 31)
	two := makeTestKeyPair(t, 32)
	writeTestKeyPair(t, certPath, keyPath, one)
	tlsCfg, lifecycle, err := buildTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, tlsCfg)
	defer tlsListener.Close()
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		for {
			conn, acceptErr := tlsListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	clientCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed test certificates
	established, err := tls.Dial("tcp", listener.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer established.Close()
	assertTLSEcho(t, established, "before rotation")
	initial := established.ConnectionState().PeerCertificates[0]

	writeTestKeyPair(t, certPath, keyPath, two)
	awaitSerial(t, tlsCfg, 32)
	assertTLSEcho(t, established, "after rotation")
	if got := established.ConnectionState().PeerCertificates[0].SerialNumber.Int64(); got != 31 {
		t.Fatalf("established connection peer serial changed to %d, want 31", got)
	}

	fresh, err := tls.Dial("tcp", listener.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	peer := fresh.ConnectionState().PeerCertificates[0]
	if peer.SerialNumber.Int64() != 32 || sha256.Sum256(peer.Raw) == sha256.Sum256(initial.Raw) {
		t.Fatalf("fresh connection did not observe rotated certificate: serial=%d", peer.SerialNumber.Int64())
	}
	_ = tlsListener.Close()
	<-serveDone
}

func assertTLSEcho(t *testing.T, conn net.Conn, text string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(text))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Fatalf("echo = %q, want %q", got, text)
	}
}

func currentSerial(t *testing.T, cfg *tls.Config) int64 {
	t.Helper()
	cert, err := cfg.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cert == nil || cert.Leaf == nil {
		t.Fatal("certificate is not fully parsed")
	}
	return cert.Leaf.SerialNumber.Int64()
}

func awaitSerial(t *testing.T, cfg *tls.Config, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if currentSerial(t, cfg) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for certificate serial %d; current %d", want, currentSerial(t, cfg))
}

func handshakeCertificate(t *testing.T, serverCfg *tls.Config) (int64, [32]byte) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		server := tls.Server(serverConn, serverCfg)
		done <- server.Handshake()
		_ = server.Close()
	}()
	client := tls.Client(clientConn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // self-signed test certificates
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	peer := client.ConnectionState().PeerCertificates[0]
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return peer.SerialNumber.Int64(), sha256.Sum256(peer.Raw)
}

type testChain struct {
	leaf         []byte
	intermediate []byte
	root         []byte
	key          []byte
}

func (c testChain) pem() []byte {
	return concatPEM(c.leaf, c.intermediate, c.root)
}

func concatPEM(certificates ...[]byte) []byte {
	var chain []byte
	for _, certificate := range certificates {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})...)
	}
	return chain
}

func makeTestChain(t *testing.T, serial int64, expiredLeaf, expiredIntermediate bool) testChain {
	t.Helper()
	now := time.Now()
	validity := func(expired bool) (time.Time, time.Time) {
		if expired {
			return now.Add(-2 * time.Hour), now.Add(-time.Hour)
		}
		return now.Add(-time.Hour), now.Add(time.Hour)
	}
	rootPublic, rootKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore, rootAfter := validity(false)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(serial + 2000), Subject: pkix.Name{CommonName: "root"}, NotBefore: rootBefore, NotAfter: rootAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPublic, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	intermediatePublic, intermediateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intermediateBefore, intermediateAfter := validity(expiredIntermediate)
	intermediateTemplate := &x509.Certificate{SerialNumber: big.NewInt(serial + 1000), Subject: pkix.Name{CommonName: "intermediate"}, NotBefore: intermediateBefore, NotAfter: intermediateAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootCert, intermediatePublic, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediateCert, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatal(err)
	}
	leafPublic, leafKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafBefore, leafAfter := validity(expiredLeaf)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: leafBefore, NotAfter: leafAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, intermediateCert, leafPublic, intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return testChain{leaf: leafDER, intermediate: intermediateDER, root: rootDER, key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}
}

func makeTestKeyPair(t *testing.T, serial int64) testKeyPair {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return testKeyPair{
		cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func writeTestKeyPair(t *testing.T, certPath, keyPath string, pair testKeyPair) {
	t.Helper()
	if err := os.WriteFile(certPath, pair.cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pair.key, 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectTLSVersion(t *testing.T, root, version string, pair testKeyPair) {
	t.Helper()
	dir := filepath.Join(root, version)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestKeyPair(t, filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), pair)
}

func swapProjectedData(t *testing.T, root, version string) {
	t.Helper()
	next := filepath.Join(root, "..data.next")
	if err := os.Symlink(version, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
}
