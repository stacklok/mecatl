package tlsreload

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
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

	tlsCfg, lifecycle, err := testTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
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
	time.Sleep(2 * reloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 2 {
		t.Fatalf("serial after malformed additional chain entry = %d, want last valid 2", got)
	}

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * reloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 2 {
		t.Fatalf("serial after invalid rotation = %d, want last valid 2", got)
	}

	if err := os.WriteFile(certPath, one.cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, two.key, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * reloadDebounce)
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
	diagnostics := &capturedDiagnostics{}
	reloader, err := New(certPath, keyPath, diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer reloader.Close()

	secret := "secret-pem-marker"
	if err := os.WriteFile(certPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	reloader.reload()
	text := diagnostics.String()
	if !strings.Contains(text, "invalid_candidate") {
		t.Fatalf("reload diagnostic missing reason code: %s", text)
	}
	for _, forbidden := range []string{secret, certPath, keyPath, "BEGIN CERTIFICATE", "localhost", "serial"} {
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
	reloader.reload()
	if got := reloader.certificate.Load().certificate.Leaf.SerialNumber.Int64(); got != 41 {
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
			reloader.reload()
			if got := reloader.certificate.Load().certificate.Leaf.SerialNumber.Int64(); got != 41 {
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
	if _, _, err := testTLSConfig(config{tlsCert: certPath, tlsKey: keyPath}); err == nil {
		t.Fatal("mismatched initial keypair succeeded")
	}

	writeTestKeyPair(t, certPath, keyPath, one)
	tlsCfg, lifecycle, err := testTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
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
	time.Sleep(2 * reloadDebounce)
	if got := currentSerial(t, tlsCfg); got != 11 {
		t.Fatalf("serial after watcher shutdown = %d, want 11", got)
	}
}

func TestTLSCertificateRejectsNonRegularAndOversizedFiles(t *testing.T) {
	pair := makeTestKeyPair(t, 13)
	for _, tc := range []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "directory", path: func(t *testing.T) string { return t.TempDir() }},
		{name: "device", path: func(*testing.T) string { return "/dev/null" }},
		{name: "fifo", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "tls.crt")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "oversized", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "tls.crt")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxTLSFileSize + 1); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certPath := tc.path(t)
			keyPath := filepath.Join(t.TempDir(), "tls.key")
			if err := os.WriteFile(keyPath, pair.key, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadServerCertificate(certPath, keyPath, time.Now())
			if err == nil || err.Error() != "TLS keypair load failed" || strings.Contains(err.Error(), certPath) {
				t.Fatalf("load error = %v", err)
			}
		})
	}
}

func TestTLSCertificateReloadRejectsFIFOAndOversizedWithoutBlockingClose(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	pair := makeTestKeyPair(t, 14)
	writeTestKeyPair(t, certPath, keyPath, pair)
	r, err := New(certPath, keyPath, port.NopDiagnostics{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(certPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxTLSFileSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	r.reload()
	if got := r.certificate.Load().certificate.Leaf.SerialNumber.Int64(); got != 14 {
		t.Fatalf("oversized reload displaced certificate: serial=%d", got)
	}
	writeTestKeyPair(t, certPath, keyPath, pair)

	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(certPath, 0o600); err != nil {
		t.Fatal(err)
	}
	r.reload()
	if got := r.certificate.Load().certificate.Leaf.SerialNumber.Int64(); got != 14 {
		t.Fatalf("FIFO reload displaced certificate: serial=%d", got)
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on FIFO reload callback")
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

	tlsCfg, lifecycle, err := testTLSConfig(config{
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
	time.Sleep(2 * reloadDebounce)
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
	tlsCfg, lifecycle, err := testTLSConfig(config{tlsCert: certPath, tlsKey: keyPath})
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

type config struct {
	tlsCert string
	tlsKey  string
}

func testTLSConfig(cfg config) (*tls.Config, *Reloader, error) {
	r, err := New(cfg.tlsCert, cfg.tlsKey, port.NopDiagnostics{})
	if err != nil {
		return nil, nil, err
	}
	return &tls.Config{GetCertificate: r.GetCertificate, MinVersion: tls.VersionTLS12}, r, nil
}

func newCertificateReloader(certFile, keyFile string) (*Reloader, error) {
	return New(certFile, keyFile, port.NopDiagnostics{})
}

type diagnosticRecord struct {
	message string
	args    []any
}

type capturedDiagnostics struct {
	mu      sync.Mutex
	records []diagnosticRecord
}

func (d *capturedDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, diagnosticRecord{message: message, args: append([]any(nil), args...)})
}

func (d *capturedDiagnostics) With(...any) port.Diagnostics { return d }

func (d *capturedDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprint(d.records)
}

func (d *capturedDiagnostics) reasonCount(reason string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, record := range d.records {
		for i := 0; i+1 < len(record.args); i += 2 {
			if record.args[i] == "reason" && record.args[i+1] == reason {
				count++
			}
		}
	}
	return count
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

type fakeTicker struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
}

func (t *fakeTicker) Chan() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()                  { t.once.Do(func() { close(t.stopped) }) }
func (t *fakeTicker) Tick(now time.Time)     { t.ch <- now }

func TestInitialCertificateRejectsExpiredAndInvalidParseableChain(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name string
		pair testKeyPair
	}{
		{name: "expired", pair: makeTestKeyPairUntil(t, now.Add(-time.Hour))},
		{name: "invalid parseable chain", pair: func() testKeyPair {
			valid := makeTestChain(t, 81, false, false)
			unrelated := makeTestChain(t, 82, false, false)
			return testKeyPair{cert: concatPEM(valid.leaf, unrelated.intermediate, unrelated.root), key: valid.key}
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "tls.crt")
			keyPath := filepath.Join(dir, "tls.key")
			writeTestKeyPair(t, certPath, keyPath, tc.pair)
			if _, err := New(certPath, keyPath, port.NopDiagnostics{}); err == nil {
				t.Fatal("invalid initial certificate succeeded")
			}
		})
	}
}

func TestConstructorErrorsAreStableAndPathFree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mounted-credential-marker")
	if _, err := New(filepath.Join(marker, "tls.crt"), filepath.Join(marker, "tls.key"), port.NopDiagnostics{}); err == nil || err.Error() != "TLS keypair load failed" || strings.Contains(err.Error(), marker) {
		t.Fatalf("missing-keypair error = %v", err)
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPair(t, 83))
	deps := defaultDependencies()
	deps.watcher = func([]string, time.Duration, time.Duration, func(), func(error)) (*filewatch.Watcher, error) {
		return nil, fmt.Errorf("cannot watch %s", marker)
	}
	if _, err := newWithDependencies(certPath, keyPath, port.NopDiagnostics{}, deps); err == nil || err.Error() != "TLS keypair watcher setup failed" || strings.Contains(err.Error(), marker) {
		t.Fatalf("unwatchable-parent setup error = %v", err)
	}
}

func TestExpiryObserverCannotPublishStaleGenerationWarning(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	clock := &fakeClock{now: now}
	fakeTick := newFakeTicker()
	diagnostics := &capturedDiagnostics{}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPairUntil(t, now.Add(time.Hour)))
	deps := defaultDependencies()
	deps.now = clock.Now
	deps.newTicker = func(time.Duration) ticker { return fakeTick }
	r, err := newWithDependencies(certPath, keyPath, diagnostics, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.beforeObserveLock = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	done := make(chan struct{})
	go func() {
		r.observeCurrent()
		close(done)
	}()
	<-entered
	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPairUntil(t, now.Add(2*time.Hour)))
	candidate, err := loadServerCertificate(certPath, keyPath, now)
	if err != nil {
		t.Fatal(err)
	}
	r.publish(candidate)
	close(release)
	<-done
	if got := diagnostics.reasonCount("expiring"); got != 2 {
		t.Fatalf("expiry warnings = %d, want one per published generation", got)
	}
	if got := r.warned; got != r.certificate.Load().generation {
		t.Fatalf("warned generation = %d, current = %d", got, r.certificate.Load().generation)
	}
}

func TestExpiryObserverWarnsOnceAndResetsForPublishedGeneration(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	clock := &fakeClock{now: now}
	fakeTick := newFakeTicker()
	diagnostics := &capturedDiagnostics{}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPairUntil(t, now.Add(31*24*time.Hour)))
	deps := defaultDependencies()
	deps.now = clock.Now
	deps.newTicker = func(time.Duration) ticker { return fakeTick }
	r, err := newWithDependencies(certPath, keyPath, diagnostics, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := diagnostics.reasonCount("expiring"); got != 0 {
		t.Fatalf("initial expiring warnings = %d, want 0", got)
	}

	clock.Set(now.Add(2 * 24 * time.Hour))
	fakeTick.Tick(clock.Now())
	awaitReasonCount(t, diagnostics, "expiring", 1)
	fakeTick.Tick(clock.Now())
	if got := diagnostics.reasonCount("expiring"); got != 1 {
		t.Fatalf("same-generation expiring warnings = %d, want 1", got)
	}

	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPairUntil(t, clock.Now().Add(time.Hour)))
	r.reload()
	if got := diagnostics.reasonCount("expiring"); got != 2 {
		t.Fatalf("new-generation expiring warnings = %d, want 2", got)
	}
}

func TestExpiryObserverReportsExpiredAndCloseJoins(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	clock := &fakeClock{now: now}
	fakeTick := newFakeTicker()
	diagnostics := &capturedDiagnostics{}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	writeTestKeyPair(t, certPath, keyPath, makeTestKeyPairUntil(t, now.Add(31*24*time.Hour)))
	deps := defaultDependencies()
	deps.now = clock.Now
	deps.newTicker = func(time.Duration) ticker { return fakeTick }
	r, err := newWithDependencies(certPath, keyPath, diagnostics, deps)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(now.Add(32 * 24 * time.Hour))
	fakeTick.Tick(clock.Now())
	awaitReasonCount(t, diagnostics, "expired", 1)
	if cert, getErr := r.GetCertificate(nil); getErr != nil || cert == nil {
		t.Fatalf("expired observation disabled last-valid certificate: cert=%v err=%v", cert, getErr)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fakeTick.stopped:
	default:
		t.Fatal("Close returned before observer fakeTick stopped")
	}
}

func awaitReasonCount(t *testing.T, diagnostics *capturedDiagnostics, reason string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if diagnostics.reasonCount(reason) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reason %q count = %d, want %d", reason, diagnostics.reasonCount(reason), want)
}

func makeTestKeyPairUntil(t *testing.T, notAfter time.Time) testKeyPair {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(notAfter.UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    notAfter.Add(-60 * 24 * time.Hour),
		NotAfter:     notAfter,
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
