package brokerconfig_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
)

func TestLoadReadsTheFileAndItsKeys(t *testing.T) {
	t.Parallel()
	fallback := brokerconfigtest.NewKey(t)
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{AgentIssuerFallbackKeys: []*ecdsa.PrivateKey{fallback}, AS1Fallbacks: 2})
	got, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := identitybroker.Config{TrustDomain: brokerconfigtest.TrustDomain, Audience: brokerconfigtest.Audience, CredentialLifetime: time.Hour}
	if got.Config != want {
		t.Errorf("Config = %+v, want %+v", got.Config, want)
	}
	if !files.AgentIssuerKey.PublicKey.Equal(got.Keys.AgentIssuer.Public()) {
		t.Error("agent issuer key is not the file's")
	}
	if len(got.Keys.AgentIssuerFallbacks) != 1 || !fallback.PublicKey.Equal(got.Keys.AgentIssuerFallbacks[0]) {
		t.Errorf("agent issuer fallbacks = %d keys, want the file's one fallback", len(got.Keys.AgentIssuerFallbacks))
	}
	if !files.BrokerClientKey.PublicKey.Equal(got.Keys.BrokerClient.Public()) || len(got.Keys.BrokerClientKeySet) == 0 {
		t.Error("broker client key or key set is not the file's")
	}
	if got.BrokerClientKeySetFile != files.BrokerClientKeySetFile || got.AS1SigningKeyFile != files.AS1SigningKeyFile ||
		strings.Join(got.AS1FallbackKeyFiles, ",") != strings.Join(files.AS1FallbackKeyFiles, ",") {
		t.Errorf("paths = %q, %q, %q; want the file's", got.BrokerClientKeySetFile, got.AS1SigningKeyFile, got.AS1FallbackKeyFiles)
	}
	if len(got.Keys.AS1Signing) != 3 {
		t.Fatalf("AS-1 keys = %d, want the signing key and 2 fallbacks", len(got.Keys.AS1Signing))
	}
	for i, path := range append([]string{files.AS1SigningKeyFile}, files.AS1FallbackKeyFiles...) {
		if !publicKeyOf(t, path).Equal(got.Keys.AS1Signing[i]) {
			t.Errorf("AS-1 key %d is not %s's", i, path)
		}
	}
}

// publicKeyOf is the public key of the SEC 1 PEM private key at path.
func publicKeyOf(t *testing.T, path string) *ecdsa.PublicKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s is not PEM", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return &key.PublicKey
}

func TestLoadAcceptsTheAgentIssuerKeyAloneWithoutAHostedAS1(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := brokerconfigtest.WriteKey(t, filepath.Join(dir, "agent.pem"), brokerconfigtest.NewKey(t))
	path := writeDoc(t, map[string]any{
		"trust_domain": "agents.example.com", "audience": "https://broker.example.com",
		"keys": map[string]any{"agent_issuer": map[string]any{"key_file": key}},
	})
	got, err := brokerconfig.Load(path, brokerconfig.Host{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Keys.BrokerClient != nil || got.AS1SigningKeyFile != "" {
		t.Error("Load returned keys the file does not name")
	}
	if _, err := brokerconfig.Load(path, brokerconfig.Host{AS1: true}); err == nil || !strings.Contains(err.Error(), "required with a hosted AS-1") {
		t.Errorf("Load with a hosted AS-1 = %v, want the missing keys refused", err)
	}
}

func TestLoadAcceptsPKCS8AndRSAKeys(t *testing.T) {
	t.Parallel()
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
	der, err := x509.MarshalPKCS8PrivateKey(files.AgentIssuerKey)
	if err != nil {
		t.Fatal(err)
	}
	writeAgentIssuer := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	doc := readDoc(t, files.Path)
	if err := os.WriteFile(keyFile(doc, "agent_issuer"), writeAgentIssuer, 0o600); err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.AS1SigningKeyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !rsaKey.PublicKey.Equal(got.Keys.AS1Signing[0]) || !files.AgentIssuerKey.PublicKey.Equal(got.Keys.AgentIssuer.Public()) {
		t.Error("Load did not read the PKCS #8 and PKCS #1 keys")
	}
}

func TestLoadRefuses(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		edit func(t *testing.T, files brokerconfigtest.Files, doc map[string]any)
		want string
	}{
		"no trust domain": {edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) { delete(doc, "trust_domain") }, want: "trust_domain is required"},
		"an invalid trust domain": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				doc["trust_domain"] = "Agents.Example.com"
			}, want: "trust_domain:",
		},
		"no audience": {edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) { delete(doc, "audience") }, want: "audience is required"},
		"an audience with a space": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				doc["audience"] = "https://broker.example.com other"
			}, want: `audience "https://broker.example.com other" must be`,
		},
		"no agent issuer key": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) { delete(keys(doc), "agent_issuer") }, want: "keys.agent_issuer.key_file is required",
		},
		"an unknown field": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) { doc["gateway"] = "x" }, want: `unknown field "gateway"`,
		},
		"an unknown key role": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as2_signing"] = map[string]any{}
			}, want: `unknown field "as2_signing"`,
		},
		"a relative key path": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["agent_issuer"] = map[string]any{"key_file": "agent.pem"}
			},
			want: "not an absolute path",
		},
		"a missing key file": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["agent_issuer"] = map[string]any{"key_file": filepath.Join(t.TempDir(), "absent.pem")}
			},
			want: "keys.agent_issuer.key_file",
		},
		"a missing key set file": {
			edit: func(t *testing.T, files brokerconfigtest.Files, _ map[string]any) {
				if err := os.Remove(files.BrokerClientKeySetFile); err != nil {
					t.Fatal(err)
				}
			},
			want: "keys.broker_client.key_set_file",
		},
		"an AS-1 fallback without the signing key": {
			edit: func(_ *testing.T, files brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as1_signing"] = map[string]any{"fallback_key_files": []string{files.AS1SigningKeyFile}}
			},
			want: "fallback_key_files requires keys.as1_signing.key_file",
		},
		"an empty key set": {
			edit: func(t *testing.T, files brokerconfigtest.Files, _ map[string]any) {
				writeFile(t, files.BrokerClientKeySetFile, `{"keys":[]}`)
			},
			want: "holds no key",
		},
		"a key set without the broker client key": {
			edit: func(t *testing.T, files brokerconfigtest.Files, _ map[string]any) {
				brokerconfigtest.WriteKeySet(t, files.BrokerClientKeySetFile, brokerconfigtest.NewKey(t))
			},
			want: "does not hold the broker client key's public key",
		},
		"one key for two roles": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as1_signing"] = map[string]any{"key_file": keyFile(doc, "agent_issuer")}
			},
			want: "agent issuer key is also the AS-1 signing key",
		},
		"a P-384 broker client key": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalECPrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, keyFile(doc, "broker_client"), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})))
			},
			want: "P-256",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
			doc := readDoc(t, files.Path)
			tc.edit(t, files, doc)
			writeJSON(t, files.Path, doc)
			_, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want an error containing %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), files.Path) {
				t.Errorf("Load = %v, want it to name the file", err)
			}
		})
	}
}

func TestLoadRefusesDataAfterTheObject(t *testing.T) {
	t.Parallel()
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
	raw, err := os.ReadFile(files.Path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, files.Path, string(raw)+`{}`)
	if _, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true}); err == nil {
		t.Fatal("Load accepted a second JSON value")
	}
}

func TestLoadErrorsOmitKeyMaterial(t *testing.T) {
	t.Parallel()
	const material = "secret-shaped-key-material"
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
	doc := readDoc(t, files.Path)
	writeFile(t, keyFile(doc, "agent_issuer"), "-----BEGIN EC PRIVATE KEY-----\n"+material+"\n-----END EC PRIVATE KEY-----\n")
	_, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err == nil {
		t.Fatal("Load accepted a malformed key")
	}
	writeFile(t, keyFile(doc, "agent_issuer"), material)
	_, err2 := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err2 == nil {
		t.Fatal("Load accepted a non-PEM key")
	}
	// Valid PEM whose DER is no key: the key parsers see the material.
	encoded := base64.StdEncoding.EncodeToString([]byte(material))
	writeFile(t, keyFile(doc, "agent_issuer"), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte(material)})))
	_, err3 := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err3 == nil {
		t.Fatal("Load accepted PEM that holds no key")
	}
	for _, err := range []error{err, err2, err3} {
		if msg := err.Error(); strings.Contains(msg, material) || strings.Contains(msg, encoded) {
			t.Errorf("error %q contains the key file's contents", msg)
		}
		if !strings.Contains(err.Error(), "keys.agent_issuer.key_file") {
			t.Errorf("Load = %v, want it to name the field", err)
		}
	}
}

// The file's keys verify the broker's own outputs: the broker client signer
// built from the file's key computes the kid the key set holds.
func TestLoadedBrokerClientKeyMatchesItsKeySet(t *testing.T) {
	t.Parallel()
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
	got, err := brokerconfig.Load(files.Path, brokerconfig.Host{AS1: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	signer, err := identitybroker.NewClientAssertionSigner(got.Keys.BrokerClient, 0)
	if err != nil {
		t.Fatalf("NewClientAssertionSigner: %v", err)
	}
	var set identitybroker.JWKSet
	if err := json.Unmarshal(got.Keys.BrokerClientKeySet, &set); err != nil || len(set.Keys) != 1 || set.Keys[0] != signer.JWKS().Keys[0] {
		t.Errorf("key set = %+v, want the signer's key %+v", set, signer.JWKS())
	}
}

func keys(doc map[string]any) map[string]any { return doc["keys"].(map[string]any) }

func keyFile(doc map[string]any, role string) string {
	return keys(doc)[role].(map[string]any)["key_file"].(string)
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeDoc(t *testing.T, doc map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identity-broker.json")
	writeJSON(t, path, doc)
	return path
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(raw))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
