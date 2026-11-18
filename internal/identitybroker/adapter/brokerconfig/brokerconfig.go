// Package brokerconfig reads the identity broker's configuration file, which
// the broker owns: the agent trust domain, the broker audience, and the paths
// of its keys, one per role. The file holds no secret, so it fits a
// ConfigMap; the keys it names are mounted from Secrets. Load reads the file
// and the keys and has the broker core validate them, so the core stays free
// of file access. The package imports nothing from Mecatl but the broker core,
// so the broker can move into its own service with it.
//
// The file is JSON, decoded strictly, so an unknown field is refused and a
// later field is an addition rather than a change:
//
//	{
//	  "trust_domain": "agents.example.com",
//	  "audience": "https://broker.example.com",
//	  "keys": {
//	    "agent_issuer": {"key_file": "/keys/agent-issuer/key.pem", "fallback_key_files": ["/keys/agent-issuer-old/key.pem"]},
//	    "broker_client": {"key_file": "/keys/broker-client/key.pem", "key_set_file": "/keys/broker-client-jwks/jwks.json"},
//	    "as1_signing": {"key_file": "/keys/as1/key.pem", "fallback_key_files": ["/keys/as1-old/key.pem"]}
//	  }
//	}
//
// Every path is absolute. Key files are PEM private keys: PKCS #1, SEC 1, or
// PKCS #8, the forms AS-1's signing key file takes. The agent issuer and
// broker client keys are ECDSA P-256; AS-1's signing keys may also be RSA. A
// fallback key is an earlier key whose signatures still verify during a
// rotation; the broker client rotates through its key set file instead,
// which holds the old and new public keys.
package brokerconfig

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// credentialLifetime is the agent credential lifetime the file does not yet
// set.
//
// Shortcut: every deployment's agent credentials live an hour; removed by #2813 (the lifetime becomes a setting in this file).
const credentialLifetime = time.Hour

// File is the identity broker's configuration file, loaded and validated.
type File struct {
	// Config is the broker's configuration: the file's trust domain and
	// audience, and the interim credential lifetime, validated with
	// identitybroker.Config.Validate.
	Config identitybroker.Config
	// Keys are the keys the file names, validated with
	// identitybroker.Keys.Validate.
	Keys identitybroker.Keys
	// BrokerClientKeySetFile is the path of the broker client's public key
	// set, which AS-1 and AS-2 read; empty without a broker client key.
	BrokerClientKeySetFile string
	// AS1SigningKeyFile and AS1FallbackKeyFiles are the paths of AS-1's
	// signing key and its fallbacks, which AS-1 reads; empty without an AS-1
	// signing key.
	AS1SigningKeyFile   string
	AS1FallbackKeyFiles []string
}

// document is the file's JSON form.
type document struct {
	TrustDomain string `json:"trust_domain"`
	Audience    string `json:"audience"`
	Keys        struct {
		AgentIssuer  signingKey `json:"agent_issuer"`
		BrokerClient struct {
			KeyFile    string `json:"key_file"`
			KeySetFile string `json:"key_set_file"`
		} `json:"broker_client"`
		AS1Signing signingKey `json:"as1_signing"`
	} `json:"keys"`
}

// signingKey names a signing key and its fallbacks.
type signingKey struct {
	KeyFile          string   `json:"key_file"`
	FallbackKeyFiles []string `json:"fallback_key_files"`
}

// Host is what the process runs beside the broker, which decides what the
// file must hold.
type Host struct {
	// AS1 reports that the process hosts AS-1, which requires the broker
	// client key and AS-1's signing key.
	AS1 bool
}

// Load reads the configuration file at path and the keys it names, and
// validates them for a broker beside host. Errors name the file and the
// field, never key material.
func Load(path string, host Host) (File, error) {
	file, err := load(path, host)
	if err != nil {
		return File{}, fmt.Errorf("identity broker configuration %s: %w", path, err)
	}
	return file, nil
}

func load(path string, host Host) (File, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the file.
	if err != nil {
		return File{}, err
	}
	doc, err := decode(raw)
	if err != nil {
		return File{}, err
	}
	switch {
	case doc.TrustDomain == "":
		return File{}, errors.New("trust_domain is required")
	case doc.Audience == "":
		return File{}, errors.New("audience is required")
	case doc.Keys.AgentIssuer.KeyFile == "":
		return File{}, errors.New("keys.agent_issuer.key_file is required")
	case doc.Keys.AS1Signing.KeyFile == "" && len(doc.Keys.AS1Signing.FallbackKeyFiles) != 0:
		return File{}, errors.New("keys.as1_signing.fallback_key_files requires keys.as1_signing.key_file")
	}
	if err := identitybroker.ValidateTrustDomain(doc.TrustDomain); err != nil {
		return File{}, fmt.Errorf("trust_domain: %w", err)
	}
	file := File{
		Config:                 identitybroker.Config{TrustDomain: doc.TrustDomain, Audience: doc.Audience, CredentialLifetime: credentialLifetime},
		BrokerClientKeySetFile: doc.Keys.BrokerClient.KeySetFile,
		AS1SigningKeyFile:      doc.Keys.AS1Signing.KeyFile,
		AS1FallbackKeyFiles:    doc.Keys.AS1Signing.FallbackKeyFiles,
	}
	// The core's own errors name the trust domain and audience fields.
	if err := file.Config.Validate(); err != nil {
		return File{}, err
	}
	keys := &file.Keys
	if keys.AgentIssuer, err = readKey("keys.agent_issuer.key_file", doc.Keys.AgentIssuer.KeyFile); err != nil {
		return File{}, err
	}
	if keys.AgentIssuerFallbacks, err = readPublicKeys("keys.agent_issuer.fallback_key_files", doc.Keys.AgentIssuer.FallbackKeyFiles); err != nil {
		return File{}, err
	}
	if name := doc.Keys.BrokerClient.KeyFile; name != "" {
		if keys.BrokerClient, err = readKey("keys.broker_client.key_file", name); err != nil {
			return File{}, err
		}
	}
	if name := doc.Keys.BrokerClient.KeySetFile; name != "" {
		if keys.BrokerClientKeySet, err = readFile("keys.broker_client.key_set_file", name); err != nil {
			return File{}, err
		}
	}
	if name := doc.Keys.AS1Signing.KeyFile; name != "" {
		names := append([]string{name}, doc.Keys.AS1Signing.FallbackKeyFiles...)
		if keys.AS1Signing, err = readPublicKeys("keys.as1_signing", names); err != nil {
			return File{}, err
		}
	}
	if err := keys.Validate(host.AS1); err != nil {
		return File{}, err
	}
	return file, nil
}

// decode decodes raw strictly: one JSON object with no unknown field.
func decode(raw []byte) (document, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return document{}, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return document{}, errors.New("decode: data after the JSON object")
	}
	return doc, nil
}

// readFile reads the file at path, which field names and must be absolute.
func readFile(field, path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s: %q is not an absolute path", field, path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the file.
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return raw, nil
}

// readKey reads the PEM private key at path, which field names: PKCS #1,
// SEC 1, or PKCS #8, tried in the order AS-1's key loader tries them. Only
// the first PEM block is read.
func readKey(field, path string) (crypto.Signer, error) {
	raw, err := readFile(field, path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: %s is not PEM", field, path)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %s is not a PKCS #1, SEC 1, or PKCS #8 private key", field, path)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not a signing key", field, path)
	}
	return signer, nil
}

// readPublicKeys reads the private keys at paths, which field names, and
// returns their public keys.
func readPublicKeys(field string, paths []string) ([]crypto.PublicKey, error) {
	var keys []crypto.PublicKey
	for _, path := range paths {
		key, err := readKey(field, path)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key.Public())
	}
	return keys, nil
}
