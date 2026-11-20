package brokerconfig_test

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/fs"
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
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{AgentIssuerFallbackKeys: []*ecdsa.PrivateKey{fallback}, HostedAS1: true, AS1Fallbacks: 2})
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
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
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
		// field is the field the error names, when want does not hold it.
		field string
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
		"a relative AS-1 signing key path": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as1_signing"] = map[string]any{"key_file": "as1.pem"}
			},
			want: `keys.as1_signing.key_file: "as1.pem" is not an absolute path`,
		},
		"a missing AS-1 signing key file": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as1_signing"] = map[string]any{"key_file": filepath.Join(t.TempDir(), "absent.pem")}
			},
			want: "keys.as1_signing.key_file: open",
		},
		"a missing AS-1 fallback": {
			edit: func(t *testing.T, files brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["as1_signing"] = map[string]any{"key_file": files.AS1SigningKeyFile, "fallback_key_files": []string{filepath.Join(t.TempDir(), "absent.pem")}}
			},
			want: "keys.as1_signing.fallback_key_files: open",
		},
		// ToolHive loads AS-1's fallbacks as signing keys.
		"a public key as AS-1's fallback": {
			edit: func(t *testing.T, files brokerconfigtest.Files, doc map[string]any) {
				public := brokerconfigtest.WritePublicKey(t, filepath.Join(t.TempDir(), "as1-old.pem"), brokerconfigtest.NewKey(t))
				keys(doc)["as1_signing"] = map[string]any{"key_file": files.AS1SigningKeyFile, "fallback_key_files": []string{public}}
			},
			want: "keys.as1_signing.fallback_key_files:",
		},
		"a missing broker client key file": {
			edit: func(t *testing.T, files brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["broker_client"] = map[string]any{"key_file": filepath.Join(t.TempDir(), "absent.pem"), "key_set_file": files.BrokerClientKeySetFile}
			},
			want: "keys.broker_client.key_file: open",
		},
		"a broker client key without its key set": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				delete(keys(doc)["broker_client"].(map[string]any), "key_set_file")
			},
			want: "keys.broker_client.key_file and keys.broker_client.key_set_file are required together",
		},
		"a PKCS #8 X25519 key": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				key, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKCS8PrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, keyFile(doc, "agent_issuer"), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
			},
			field: "keys.agent_issuer.key_file",
			want:  "is not a signing key",
		},
		"a relative agent issuer fallback path": {
			edit: func(_ *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["agent_issuer"].(map[string]any)["fallback_key_files"] = []string{"old.pem"}
			},
			want: `keys.agent_issuer.fallback_key_files: "old.pem" is not an absolute path`,
		},
		"a missing agent issuer fallback": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				keys(doc)["agent_issuer"].(map[string]any)["fallback_key_files"] = []string{filepath.Join(t.TempDir(), "absent.pem")}
			},
			want: "keys.agent_issuer.fallback_key_files: open",
		},
		"an RSA agent issuer fallback": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "old.pem")
				writeFile(t, path, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
				keys(doc)["agent_issuer"].(map[string]any)["fallback_key_files"] = []string{path}
			},
			want: "agent issuer fallback key 0: public key type *rsa.PublicKey is not ECDSA P-256",
		},
		"a P-384 agent issuer fallback": {
			edit: func(t *testing.T, _ brokerconfigtest.Files, doc map[string]any) {
				key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalECPrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "old.pem")
				writeFile(t, path, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})))
				keys(doc)["agent_issuer"].(map[string]any)["fallback_key_files"] = []string{path}
			},
			want: "agent issuer fallback key 0",
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
			files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
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
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("Load = %v, want it to name the field %s", err, tc.field)
			}
		})
	}
}

// TestLoadRefusesAnAS1SigningKeyWithoutAHostedAS1: nothing would use the key,
// so it is a misconfiguration, such as a forgotten AS-1 issuer.
func TestLoadRefusesAnAS1SigningKeyWithoutAHostedAS1(t *testing.T) {
	t.Parallel()
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
	if _, err := brokerconfig.Load(files.Path, brokerconfig.Host{}); err == nil || !strings.Contains(err.Error(), "keys.as1_signing requires a hosted AS-1") {
		t.Fatalf("Load without a hosted AS-1 = %v, want the AS-1 signing key refused", err)
	}
}

// TestLoadNamesAMissingFileOnce: the file-system error names the path, so
// Load does not repeat it.
func TestLoadNamesAMissingFileOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "identity-broker.json")
	_, err := brokerconfig.Load(path, brokerconfig.Host{})
	if !errors.Is(err, fs.ErrNotExist) || strings.Count(err.Error(), path) != 1 {
		t.Fatalf("Load = %v, want fs.ErrNotExist naming %s once", err, path)
	}
}

// TestLoadAcceptsAgentIssuerFallbackKeyForms: an agent issuer fallback may
// be a PKIX public key, the recommended form, or a private key.
func TestLoadAcceptsAgentIssuerFallbackKeyForms(t *testing.T) {
	t.Parallel()
	public, private := brokerconfigtest.NewKey(t), brokerconfigtest.NewKey(t)
	dir := t.TempDir()
	path := writeDoc(t, map[string]any{
		"trust_domain": "agents.example.com", "audience": "https://broker.example.com",
		"keys": map[string]any{"agent_issuer": map[string]any{
			"key_file": brokerconfigtest.WriteKey(t, filepath.Join(dir, "agent.pem"), brokerconfigtest.NewKey(t)),
			"fallback_key_files": []string{
				brokerconfigtest.WritePublicKey(t, filepath.Join(dir, "public.pem"), public),
				brokerconfigtest.WriteKey(t, filepath.Join(dir, "private.pem"), private),
			},
		}},
	})
	got, err := brokerconfig.Load(path, brokerconfig.Host{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Keys.AgentIssuerFallbacks) != 2 || !public.PublicKey.Equal(got.Keys.AgentIssuerFallbacks[0]) || !private.PublicKey.Equal(got.Keys.AgentIssuerFallbacks[1]) {
		t.Fatalf("agent issuer fallbacks = %d keys, want the public key's and the private key's", len(got.Keys.AgentIssuerFallbacks))
	}
}

func TestLoadRefusesDataAfterTheObject(t *testing.T) {
	t.Parallel()
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
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
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
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
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
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
