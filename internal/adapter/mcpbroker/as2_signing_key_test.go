package mcpbroker

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
)

// The tests in this file are not parallel: AS-2's protected route reads an
// environment variable.

// writeSigningKey writes a new P-256 signing key and returns it and its path.
func writeSigningKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key := brokerconfigtest.NewKey(t)
	return key, brokerconfigtest.WriteKey(t, filepath.Join(t.TempDir(), "as2.pem"), key)
}

// withSigningKey has AS-2 sign with keyFile and keep fallbacks.
func withSigningKey(keyFile string, fallbacks ...string) func(*ToolHiveConfig) {
	return func(c *ToolHiveConfig) { c.SigningKeyFile, c.FallbackKeyFiles = keyFile, fallbacks }
}

// gatewayToken redeems a fresh ID-JAG from as1 at process's AS-2 for a
// gateway token (V).
func gatewayToken(t *testing.T, as1 trustedAS1, broker as2BrokerClient, process *Process) string {
	t.Helper()
	as2 := process.AS2
	status, body := postAS2Token(t, as2, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {as1.idJAG(t, as2)}, "resource": {as2.Resource},
		"client_assertion_type": {clientAssertionTyp}, "client_assertion": {broker.assertion(t, as2.Issuer)},
	})
	token, _ := body["access_token"].(string)
	if status != http.StatusOK || token == "" {
		t.Fatalf("ID-JAG redemption: status %d, body %v", status, body)
	}
	return token
}

// mountedProcess serves process's handlers.
func mountedProcess(t *testing.T, process *Process) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := process.Handlers.Mount(mux, "/callback"); err != nil {
		t.Fatalf("mount handlers: %v", err)
	}
	return mux
}

// vmcpInitialize sends an MCP initialize with token to process's vMCP, which
// checks the token with the embedded server's KeyProvider, and returns the
// status.
func vmcpInitialize(t *testing.T, process *Process, token string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, as2TestResource, strings.NewReader(mcpInitializeBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	mountedProcess(t, process).ServeHTTP(recorder, req)
	return recorder.Code
}

// jwks returns process's published key set.
func jwks(t *testing.T, process *Process) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, as2TestIssuer+"/.well-known/jwks.json", nil)
	recorder := httptest.NewRecorder()
	mountedProcess(t, process).ServeHTTP(recorder, req)
	body, _ := io.ReadAll(recorder.Body)
	if recorder.Code != http.StatusOK || len(body) == 0 {
		t.Fatalf("JWKS: status %d", recorder.Code)
	}
	return string(body)
}

func TestToolHiveSigningKeyRunConfig(t *testing.T) {
	got, err := ToolHiveConfig{}.signingKeyRunConfig()
	if err != nil || got != nil {
		t.Fatalf("without a key = %+v, %v; want none, so ToolHive generates one", got, err)
	}
	got, err = ToolHiveConfig{SigningKeyFile: "/keys/as2.pem", FallbackKeyFiles: []string{"/keys/as2-old.pem"}}.signingKeyRunConfig()
	want := &authserver.SigningKeyRunConfig{SigningKeyFile: "/keys/as2.pem", FallbackKeyFiles: []string{"/keys/as2-old.pem"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("with a key = %+v, %v; want %+v with no key directory", got, err, want)
	}
	for name, c := range map[string]ToolHiveConfig{
		"fallbacks without a key": {FallbackKeyFiles: []string{"/keys/as2-old.pem"}},
		"a relative key":          {SigningKeyFile: "as2.pem"},
		"a relative fallback":     {SigningKeyFile: "/keys/as2.pem", FallbackKeyFiles: []string{"as2-old.pem"}},
	} {
		if _, err := c.signingKeyRunConfig(); !errors.Is(err, ErrInvalidCatalogue) {
			t.Errorf("%s = %v, want ErrInvalidCatalogue", name, err)
		}
		c.CallbackURL, c.Profiles = as2TestCallbackURL, []ToolHiveProfile{staticProtectedProfile("private")}
		if process, err := NewToolHiveProcess(t.Context(), c); !errors.Is(err, ErrInvalidCatalogue) {
			if process != nil {
				_ = process.Close()
			}
			t.Errorf("NewToolHiveProcess with %s = %v, want ErrInvalidCatalogue", name, err)
		}
	}
}

func TestSigningPublicKeys(t *testing.T) {
	key, keyFile := writeSigningKey(t)
	old, oldFile := writeSigningKey(t)
	got, err := SigningPublicKeys(keyFile, []string{oldFile})
	if err != nil || len(got) != 2 || !key.PublicKey.Equal(got[0]) || !old.PublicKey.Equal(got[1]) {
		t.Fatalf("SigningPublicKeys = %d keys, %v; want the key's and the fallback's", len(got), err)
	}
	missing := filepath.Join(t.TempDir(), "absent.pem")
	if _, err := SigningPublicKeys(keyFile, []string{missing}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("SigningPublicKeys with a missing fallback = %v, want an error naming it", err)
	}
	// A fallback is a private key, as ToolHive loads it as a signing key.
	public := brokerconfigtest.WritePublicKey(t, filepath.Join(t.TempDir(), "public.pem"), old)
	if _, err := SigningPublicKeys(keyFile, []string{public}); err == nil {
		t.Fatal("SigningPublicKeys accepted a public key as a fallback")
	}
}

// writePKCS8Key writes key as a PKCS #8 PEM private key and returns its
// path.
func writePKCS8Key(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSigningPublicKeysRefusesKeysTheVMCPCannotVerify: the vMCP verifies
// only RSA and ECDSA signatures, and ToolHive signs only on P-256, P-384, or
// P-521, so an Ed25519 key or another curve is refused before startup goes
// on, as signing key or fallback; the curves that work are accepted.
func TestSigningPublicKeysRefusesKeysTheVMCPCannotVerify(t *testing.T) {
	_, keyFile := writeSigningKey(t)
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{"an Ed25519 key": writePKCS8Key(t, edKey), "a P-224 key": writePKCS8Key(t, p224)} {
		if _, err := SigningPublicKeys(file, nil); err == nil || !strings.Contains(err.Error(), file) {
			t.Errorf("SigningPublicKeys with %s = %v, want an error naming the file", name, err)
		}
		if _, err := SigningPublicKeys(keyFile, []string{file}); err == nil {
			t.Errorf("SigningPublicKeys with %s as a fallback = nil, want an error", name)
		}
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SigningPublicKeys(keyFile, []string{writePKCS8Key(t, rsaKey), writePKCS8Key(t, p384)}); err != nil {
		t.Fatalf("SigningPublicKeys with RSA and P-384 fallbacks = %v", err)
	}
}

// TestSigningPublicKeysRefusesAKeyListedTwice: the signing key repeated as
// a fallback, the usual slip when promoting a key, or a fallback listed
// twice, even under another file name, would publish one key ID twice.
func TestSigningPublicKeysRefusesAKeyListedTwice(t *testing.T) {
	key, keyFile := writeSigningKey(t)
	_, oldFile := writeSigningKey(t)
	copyFile := brokerconfigtest.WriteKey(t, filepath.Join(t.TempDir(), "copy.pem"), key)
	for name, fallbacks := range map[string][]string{
		"the signing key as a fallback": {oldFile, keyFile},
		"a copy of the signing key":     {copyFile},
		"a fallback twice":              {oldFile, oldFile},
	} {
		if _, err := SigningPublicKeys(keyFile, fallbacks); err == nil || !strings.Contains(err.Error(), "same key") {
			t.Errorf("SigningPublicKeys with %s = %v, want a same-key error", name, err)
		}
	}
}

// TestAS2SigningKeySurvivesARebuild: with an operator key, AS-2 publishes
// the same key set after a rebuild and the rebuilt vMCP accepts a gateway
// token signed before it; without one, each build generates its own key.
func TestAS2SigningKeySurvivesARebuild(t *testing.T) {
	as1 := startTrustedAS1(t)
	broker := newAS2BrokerClient(t)
	_, keyFile := writeSigningKey(t)
	first := startTrustingAS2With(t, as1, broker.config, withSigningKey(keyFile))
	token := gatewayToken(t, as1, broker, first)
	rebuilt := startTrustingAS2With(t, as1, broker.config, withSigningKey(keyFile))
	if jwks(t, first) != jwks(t, rebuilt) {
		t.Fatal("the rebuilt AS-2 publishes another key set")
	}
	if status := vmcpInitialize(t, rebuilt, token); status != http.StatusOK {
		t.Fatalf("the rebuilt vMCP with a gateway token from before: status %d, want 200", status)
	}

	generated := startTrustingAS2(t, as1, broker.config)
	regenerated := startTrustingAS2(t, as1, broker.config)
	if jwks(t, generated) == jwks(t, regenerated) {
		t.Fatal("two builds without a signing key published one key set")
	}
	if status := vmcpInitialize(t, regenerated, gatewayToken(t, as1, broker, generated)); status != http.StatusUnauthorized {
		t.Fatalf("a rebuilt vMCP without a signing key accepted an earlier gateway token: status %d, want 401", status)
	}
}

// TestAS2SigningKeyRotation: a gateway token signed with the previous key
// verifies at the vMCP while that key is a fallback, and not once it is
// dropped.
func TestAS2SigningKeyRotation(t *testing.T) {
	as1 := startTrustedAS1(t)
	broker := newAS2BrokerClient(t)
	old, oldFile := writeSigningKey(t)
	key, newFile := writeSigningKey(t)
	token := gatewayToken(t, as1, broker, startTrustingAS2With(t, as1, broker.config, withSigningKey(oldFile)))

	during := startTrustingAS2With(t, as1, broker.config, withSigningKey(newFile, oldFile))
	// AS-2 reports the keys it loaded, which composition checks against the
	// identity broker's keys after the build.
	if loaded := during.AS2.SigningPublicKeys(); len(loaded) != 2 || !key.PublicKey.Equal(loaded[0]) || !old.PublicKey.Equal(loaded[1]) {
		t.Fatalf("AS-2 reports %d loaded signing keys, want the new key then the fallback", len(loaded))
	}
	if status := vmcpInitialize(t, during, token); status != http.StatusOK {
		t.Fatalf("during the rotation, a gateway token signed with the fallback: status %d, want 200", status)
	}
	if status := vmcpInitialize(t, during, gatewayToken(t, as1, broker, during)); status != http.StatusOK {
		t.Fatalf("during the rotation, a gateway token signed with the new key: status %d, want 200", status)
	}
	after := startTrustingAS2With(t, as1, broker.config, withSigningKey(newFile))
	if status := vmcpInitialize(t, after, token); status != http.StatusUnauthorized {
		t.Fatalf("after the rotation, a gateway token signed with the dropped key: status %d, want 401", status)
	}
}
