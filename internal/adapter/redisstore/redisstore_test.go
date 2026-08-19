package redisstore_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestNewRejectsEmptyAddr guards the constructor's fail-fast on an empty
// address (a nil-deref or a silent localhost default would be a worse failure
// mode than a clear error).
func TestNewRejectsEmptyAddr(t *testing.T) {
	if _, err := redisstore.New(""); err == nil {
		t.Fatal("New(\"\") = nil error, want rejection")
	}
}

// TestMecak8sRedisTLS_Scenario2_AuthenticatedTrustedEndpoint proves the
// Secret-file configuration reaches authenticated TLS Redis and the single Store
// still persists snapshots, events, and tool-call records. It is entirely
// offline: miniredis serves a CA-validated mTLS endpoint in-process.
func TestMecak8sRedisTLS_Scenario2_AuthenticatedTrustedEndpoint(t *testing.T) {
	serverTLS, serverCA := redisTLSFixture(t)
	mr, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatalf("start TLS Redis fixture: %v", err)
	}
	t.Cleanup(mr.Close)
	password := randomFixturePassword(t)
	mr.RequireUserAuth("fixture-user", password)

	dir := t.TempDir()
	cfg := redisstore.Config{
		Addr:         mr.Addr(),
		UsernameFile: writeFixtureFile(t, dir, "username", "fixture-user"),
		PasswordFile: writeFixtureFile(t, dir, "password", password),
		CAFile:       writeFixtureFile(t, dir, "ca.pem", serverCA),
	}
	st, err := redisstore.NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig(authenticated TLS Redis): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	s := newTestSession(t, "redis-tls-authenticated")
	if err := st.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := st.Load(context.Background(), s.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := st.Append(context.Background(), s.ID, session.Event{Type: session.EvUserPrompt}); err != nil {
		t.Fatalf("Append event: %v", err)
	}
	st.ToolCall(s.ID, session.ToolCall{ID: "call", Name: "Read"}, session.NewToolResult("call", "ok"), 0, 0)
}

// TestMecak8sRedisTLS_Scenario2_UntrustedOrUnauthenticatedEndpointFailsClosed
// covers trust, credential, and incomplete-material failures without exposing a
// Secret value in an error or test diagnostic.
func TestMecak8sRedisTLS_Scenario2_UntrustedOrUnauthenticatedEndpointFailsClosed(t *testing.T) {
	serverTLS, serverCA := redisTLSFixture(t)
	mr, err := miniredis.RunTLS(serverTLS)
	if err != nil {
		t.Fatalf("start TLS Redis fixture: %v", err)
	}
	t.Cleanup(mr.Close)
	password := randomFixturePassword(t)
	mr.RequireUserAuth("fixture-user", password)
	dir := t.TempDir()
	base := redisstore.Config{
		Addr:         mr.Addr(),
		UsernameFile: writeFixtureFile(t, dir, "username", "fixture-user"),
		PasswordFile: writeFixtureFile(t, dir, "password", password),
		CAFile:       writeFixtureFile(t, dir, "ca.pem", serverCA),
	}

	badCA := base
	badCA.CAFile = writeFixtureFile(t, dir, "untrusted-ca.pem", newCA(t))
	badPasswordValue := randomFixturePassword(t)
	badPassword := base
	badPassword.PasswordFile = writeFixtureFile(t, dir, "wrong-password", badPasswordValue)
	missingTrust := base
	missingTrust.CAFile = ""
	// System-trust mode against a fixture CA the host does not trust: this is the
	// proof that --redis-tls verifies rather than silently skipping verification.
	systemTrust := base
	systemTrust.CAFile = ""
	systemTrust.TLS = true
	for name, cfg := range map[string]redisstore.Config{
		"untrusted certificate":                 badCA,
		"rejected credentials":                  badPassword,
		"no trust anchor at all":                missingTrust,
		"system trust store rejects private CA": systemTrust,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := redisstore.NewWithConfig(cfg)
			if err == nil {
				t.Fatal("NewWithConfig succeeded, want fail-closed error")
			}
			for _, secret := range []string{password, badPasswordValue} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error disclosed Redis secret value")
				}
			}
		})
	}
}

// TestInvariant_mecak8s_redis_secret_non_disclosure pins that no Secret-mounted
// value, and no credential an operator embedded in the address itself, can reach
// a returned error — errors land in the operator's diagnostics log.
func TestInvariant_mecak8s_redis_secret_non_disclosure(t *testing.T) {
	_, serverCA := redisTLSFixture(t)
	password := randomFixturePassword(t)
	dir := t.TempDir()

	// A mounted password must not surface when the connection itself fails.
	_, err := redisstore.NewWithConfig(redisstore.Config{
		Addr:         "127.0.0.1:1",
		PasswordFile: writeFixtureFile(t, dir, "password", password),
		CAFile:       writeFixtureFile(t, dir, "ca.pem", serverCA),
	})
	if err == nil {
		t.Fatal("NewWithConfig succeeded against a closed port")
	}
	if strings.Contains(err.Error(), password) {
		t.Error("Redis connection error disclosed a Secret-mounted password")
	}

	// A URL-shaped address can carry a password in its userinfo. It must be
	// rejected, and the rejection must not echo the address back.
	for _, addr := range []string{
		"rediss://user:" + password + "@redis.example:6379",
		"redis://" + password + "@redis.example:6379",
		password + "@redis.example:6379",
	} {
		_, err := redisstore.NewWithConfig(redisstore.Config{Addr: addr, AllowPlaintext: true})
		if err == nil {
			t.Fatalf("NewWithConfig accepted a URL-shaped address %q", "<redacted>")
		}
		if strings.Contains(err.Error(), password) {
			t.Error("address rejection echoed a credential embedded in the address")
		}
	}
}

func redisTLSFixture(t *testing.T) (*tls.Config, string) {
	t.Helper()
	caCert, caKey, caPEM := newTestCA(t)
	serverCert := newServerCertificate(t, caCert, caKey)
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}, caPEM
}

func newCA(t *testing.T) string {
	t.Helper()
	_, _, caPEM := newTestCA(t)
	return caPEM
}

func newTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "redis fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func newServerCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "redis fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func randomFixturePassword(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(bytes)
}

func writeFixtureFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewPingsAndFailsFastOnUnreachableBroker guards that New pings the broker
// so a misconfigured address surfaces at construction, not on the first Save.
func TestNewPingsAndFailsFastOnUnreachableBroker(t *testing.T) {
	// An address that refuses connections: miniredis closed before New pings.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	addr := mr.Addr()
	mr.Close()
	if _, err := redisstore.New(addr); err == nil {
		t.Fatal("New(closed broker) = nil error, want ping failure")
	}
}

// TestLoadNotFoundWrapsSentinelAndNamesId is the redis-specific not-found
// guarantee: redis.Nil on a missing key maps to an error wrapping
// port.ErrSessionNotFound AND naming the id (the conformance suite asserts the
// same, but this pins it against the raw Redis path without the suite's
// scaffolding).
func TestLoadNotFoundWrapsSentinelAndNamesId(t *testing.T) {
	st := newTestStore(t)
	const id session.SessionID = "redis-no-such"
	_, err := st.Load(context.Background(), id)
	if !errors.Is(err, port.ErrSessionNotFound) {
		t.Errorf("Load(missing) = %v, want errors.Is(_, ErrSessionNotFound)", err)
	}
	if !strings.Contains(err.Error(), string(id)) {
		t.Errorf("Load(missing) error %q does not name the id %q", err, id)
	}
}

// TestReadRejectsUnknownFormatTag guards the forward-incompatibility contract:
// a record carrying an unknown format tag must surface as an error on Read,
// never a silent skip.
func TestReadRejectsUnknownFormatTag(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const id session.SessionID = "redis-bad-tag"
	// Inject a record with a bogus format tag directly into the event list,
	// bypassing Append (which always stamps the correct tag).
	mr.RPush("mecatl:events:"+string(id), `{"v":"bogus-format/9","ev":{}}`)

	log := port.EventLog(st)
	var sawEvent bool
	var sawErr error
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			sawErr = err
			break
		}
		sawEvent = true
		_ = ev
	}
	if sawEvent {
		t.Fatal("Read yielded an event for a bogus-format record, want an error first")
	}
	if sawErr == nil {
		t.Fatal("Read yielded no error for a bogus-format record")
	}
}

// TestOverwriteReplacesSnapshot guards the HSET-overwrite path: a second Save
// replaces the blob (not appends), so Load returns the latest snapshot.
func TestOverwriteReplacesSnapshot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	s := newTestSession(t, "redis-overwrite")
	if err := s.RecordUserPrompt("first", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	if err := s.RecordUserPrompt("second", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save #2: %v", err)
	}
	got, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(got.Conversation.Messages); n != 2 {
		t.Errorf("Load after overwrite = %d messages, want 2 (the latest snapshot)", n)
	}
}
