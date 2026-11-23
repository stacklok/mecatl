package mcpbroker

import (
	"crypto/ecdsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	_, oldFile := writeSigningKey(t)
	_, newFile := writeSigningKey(t)
	token := gatewayToken(t, as1, broker, startTrustingAS2With(t, as1, broker.config, withSigningKey(oldFile)))

	during := startTrustingAS2With(t, as1, broker.config, withSigningKey(newFile, oldFile))
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
