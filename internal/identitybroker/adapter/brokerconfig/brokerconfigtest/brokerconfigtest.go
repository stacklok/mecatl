// Package brokerconfigtest writes an identity broker configuration file and
// its keys into a test's temporary directory, so tests that enable the
// identity broker start it the way a deployment does, from operator files.
// Production code never imports it.
package brokerconfigtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Default trust domain and audience of a written file.
const (
	TrustDomain = "agents.mecatl.test"
	Audience    = "https://identity-broker.mecatl.test"
)

// Options select what Write writes.
type Options struct {
	// TrustDomain replaces the TrustDomain constant when set.
	TrustDomain string
	// AgentIssuerKey is the agent issuer key to write; nil generates one.
	AgentIssuerKey *ecdsa.PrivateKey
	// AgentIssuerFallbackKeys are the agent issuer fallback keys to write,
	// such as a key that signed credentials before a rotation.
	AgentIssuerFallbackKeys []*ecdsa.PrivateKey
	// BrokerClientKey is the broker client key to write, such as one a test's
	// AS-1 already registered; nil generates one.
	BrokerClientKey *ecdsa.PrivateKey
	// AS1Fallbacks generates that many AS-1 fallback keys.
	AS1Fallbacks int
}

// Files are a written configuration file and its keys.
type Files struct {
	// Path is the configuration file.
	Path string
	// AgentIssuerKey and BrokerClientKey are the keys the file names.
	AgentIssuerKey, BrokerClientKey *ecdsa.PrivateKey
	// BrokerClientKeySetFile is the broker client's key set file.
	BrokerClientKeySetFile string
	// AS1SigningKeyFile and AS1FallbackKeyFiles are AS-1's key files.
	AS1SigningKeyFile   string
	AS1FallbackKeyFiles []string
}

// Write writes a configuration file with every key, one per role, each a new
// P-256 key unless opts supplies it, into a new temporary directory of t.
func Write(t testing.TB, opts Options) Files {
	t.Helper()
	dir := t.TempDir()
	files := Files{Path: filepath.Join(dir, "identity-broker.json"), AgentIssuerKey: opts.AgentIssuerKey, BrokerClientKey: opts.BrokerClientKey}
	if files.AgentIssuerKey == nil {
		files.AgentIssuerKey = NewKey(t)
	}
	if files.BrokerClientKey == nil {
		files.BrokerClientKey = NewKey(t)
	}
	agentIssuerFile := WriteKey(t, filepath.Join(dir, "agent-issuer.pem"), files.AgentIssuerKey)
	var agentFallbacks []string
	for _, key := range opts.AgentIssuerFallbackKeys {
		agentFallbacks = append(agentFallbacks, WriteKey(t, filepath.Join(t.TempDir(), "agent-issuer.pem"), key))
	}
	clientFile := WriteKey(t, filepath.Join(dir, "broker-client.pem"), files.BrokerClientKey)
	files.BrokerClientKeySetFile = WriteKeySet(t, filepath.Join(dir, "broker-client-jwks.json"), files.BrokerClientKey)
	files.AS1SigningKeyFile = WriteKey(t, filepath.Join(dir, "as1.pem"), NewKey(t))
	for range opts.AS1Fallbacks {
		files.AS1FallbackKeyFiles = append(files.AS1FallbackKeyFiles, WriteKey(t, filepath.Join(t.TempDir(), "as1.pem"), NewKey(t)))
	}
	trustDomain := opts.TrustDomain
	if trustDomain == "" {
		trustDomain = TrustDomain
	}
	type signingKey struct {
		KeyFile          string   `json:"key_file"`
		FallbackKeyFiles []string `json:"fallback_key_files,omitempty"`
	}
	doc := map[string]any{
		"trust_domain": trustDomain,
		"audience":     Audience,
		"keys": map[string]any{
			"agent_issuer":  signingKey{KeyFile: agentIssuerFile, FallbackKeyFiles: agentFallbacks},
			"broker_client": map[string]string{"key_file": clientFile, "key_set_file": files.BrokerClientKeySetFile},
			"as1_signing":   signingKey{KeyFile: files.AS1SigningKeyFile, FallbackKeyFiles: files.AS1FallbackKeyFiles},
		},
	}
	writeJSON(t, files.Path, doc)
	return files
}

// NewKey returns a new P-256 key.
func NewKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("brokerconfigtest: generate key: %v", err)
	}
	return key
}

// WriteKey writes key to path as a SEC 1 PEM private key, the form
// `openssl ecparam -genkey -noout` writes, and returns path.
func WriteKey(t testing.TB, path string, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("brokerconfigtest: encode key: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("brokerconfigtest: write key: %v", err)
	}
	return path
}

// WriteKeySet writes the public JWK Set of keys, each under its RFC 7638
// thumbprint kid as the broker client signer computes it, to path and returns
// path.
func WriteKeySet(t testing.TB, path string, keys ...*ecdsa.PrivateKey) string {
	t.Helper()
	var set identitybroker.JWKSet
	for _, key := range keys {
		signer, err := identitybroker.NewClientAssertionSigner(key, 0)
		if err != nil {
			t.Fatalf("brokerconfigtest: key set: %v", err)
		}
		set.Keys = append(set.Keys, signer.JWKS().Keys...)
	}
	writeJSON(t, path, set)
	return path
}

func writeJSON(t testing.TB, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("brokerconfigtest: encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("brokerconfigtest: write %s: %v", path, err)
	}
}
