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
//	    "agent_issuer": {"key_file": "/keys/agent-issuer/key.pem", "fallback_key_files": ["/keys/agent-issuer-old/public.pem"]},
//	    "broker_client": {"key_file": "/keys/broker-client/key.pem", "key_set_file": "/keys/broker-client-jwks/jwks.json"},
//	    "as1_signing": {"key_file": "/keys/as1/key.pem", "fallback_key_files": ["/keys/as1-old/key.pem"]}
//	  }
//	}
//
// Every path is absolute. Key files are PEM private keys: PKCS #1, SEC 1, or
// PKCS #8, the forms AS-1's signing key file takes. The agent issuer and
// broker client keys are ECDSA P-256; AS-1's signing keys may also be RSA. A
// fallback key is an earlier key whose signatures still verify during a
// rotation. The broker only verifies with an agent issuer fallback, so it may
// be, and preferably is, a PEM public key (PKIX, "PUBLIC KEY"); AS-1's
// fallbacks are private keys, as ToolHive loads them as signing keys. The
// broker client rotates through its key set file instead, which holds the old
// and new public keys. AS-1's signing key is configured only with a hosted
// AS-1.
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
// file must hold and may hold.
type Host = identitybroker.Host

// Load reads the configuration file at path and the keys it names, and
// validates them for a broker beside host. Errors name the file and the
// field, never key material.
func Load(path string, host Host) (File, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the file.
	if err != nil {
		// The file-system error names the path already.
		return File{}, fmt.Errorf("identity broker configuration: %w", err)
	}
	file, err := load(raw, host)
	if err != nil {
		return File{}, fmt.Errorf("identity broker configuration %s: %w", path, err)
	}
	return file, nil
}

func load(raw []byte, host Host) (File, error) {
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
	case (doc.Keys.BrokerClient.KeyFile == "") != (doc.Keys.BrokerClient.KeySetFile == ""):
		return File{}, errors.New("keys.broker_client.key_file and keys.broker_client.key_set_file are required together")
	case doc.Keys.AS1Signing.KeyFile == "" && len(doc.Keys.AS1Signing.FallbackKeyFiles) != 0:
		return File{}, errors.New("keys.as1_signing.fallback_key_files requires keys.as1_signing.key_file")
	case !host.AS1 && doc.Keys.AS1Signing.KeyFile != "":
		// Refused before the unused private key is read.
		return File{}, errors.New("keys.as1_signing requires a hosted AS-1, which this process does not run")
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
	if file.Keys, err = readKeys(doc); err != nil {
		return File{}, err
	}
	if err := file.Keys.Validate(host); err != nil {
		return File{}, err
	}
	return file, nil
}

// readKeys reads the keys doc names, which load has checked are named
// together where they must be.
func readKeys(doc document) (identitybroker.Keys, error) {
	var keys identitybroker.Keys
	var err error
	if keys.AgentIssuer, err = readKey("keys.agent_issuer.key_file", doc.Keys.AgentIssuer.KeyFile); err != nil {
		return keys, err
	}
	for _, name := range doc.Keys.AgentIssuer.FallbackKeyFiles {
		pub, err := readPublicKey("keys.agent_issuer.fallback_key_files", name)
		if err != nil {
			return keys, err
		}
		keys.AgentIssuerFallbacks = append(keys.AgentIssuerFallbacks, pub)
	}
	if client := doc.Keys.BrokerClient; client.KeyFile != "" {
		if keys.BrokerClient, err = readKey("keys.broker_client.key_file", client.KeyFile); err != nil {
			return keys, err
		}
		if keys.BrokerClientKeySet, err = readFile("keys.broker_client.key_set_file", client.KeySetFile); err != nil {
			return keys, err
		}
	}
	if doc.Keys.AS1Signing.KeyFile == "" {
		return keys, nil
	}
	// ToolHive loads AS-1's fallbacks as signing keys, so they are private
	// keys too.
	for i, name := range append([]string{doc.Keys.AS1Signing.KeyFile}, doc.Keys.AS1Signing.FallbackKeyFiles...) {
		field := "keys.as1_signing.fallback_key_files"
		if i == 0 {
			field = "keys.as1_signing.key_file"
		}
		key, err := readKey(field, name)
		if err != nil {
			return keys, err
		}
		keys.AS1Signing = append(keys.AS1Signing, key.Public())
	}
	return keys, nil
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
	return parseKey(field, path, raw)
}

// parseKey parses raw, the file at path, as readKey reads it.
func parseKey(field, path string, raw []byte) (crypto.Signer, error) {
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

// readPublicKey reads the PEM public key (PKIX, "PUBLIC KEY") at path, which
// field names, or the public key of the private key there, as readKey reads
// it.
func readPublicKey(field, path string) (crypto.PublicKey, error) {
	raw, err := readFile(field, path)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(raw); block != nil && block.Type == "PUBLIC KEY" {
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %s is not a PKIX public key", field, path)
		}
		return pub, nil
	}
	key, err := parseKey(field, path, raw)
	if err != nil {
		return nil, err
	}
	return key.Public(), nil
}
