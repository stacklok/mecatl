// Package identitybroker is the in-process core of the agent identity broker.
//
// Service is the contract for the whole identity broker: Mecatl calls it, and
// only it, whether the broker runs in process or as its own service. Core is
// the broker side of that contract, with the authenticated workload on every
// call; NewInProcess joins the two through an Authenticator. Broker is the
// Core, and package conformance holds the contract tests every implementation
// passes.
//
// New validates explicitly supplied configuration, an ECDSA P-256 (ES256)
// signing capability, and already-loaded definition records, and returns a Broker
// holding an independently owned snapshot of them, and no broker sessions. The snapshot holds only
// definitions the broker loads itself, operator-loaded global definitions and
// system built-ins; repository definitions never enter it. The package performs no
// file, environment, or network access; source loading and transport belong
// in adapters that call it.
package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
)

// Errors returned by New and DefinitionRecord.Validate. Each returned error
// wraps exactly one of them, so a transport adapter can map failures with
// errors.Is.
var (
	ErrInvalidConfig         = errors.New("identitybroker: invalid configuration")
	ErrInvalidSigner         = errors.New("identitybroker: invalid signer")
	ErrInvalidDefinition     = errors.New("identitybroker: invalid definition record")
	ErrConflictingDefinition = errors.New("identitybroker: conflicting definition records")
)

// Broker is the identity broker core. Its startup state is immutable after
// New; its broker sessions are guarded by a mutex. Its methods are safe for
// concurrent use.
type Broker struct {
	config      Config
	signer      crypto.Signer
	definitions map[DefinitionRef]DefinitionRecord

	sessions *sessionTable
}

// New validates its inputs and returns a ready Broker, or an error wrapping one
// of the package's sentinel errors. Inputs are copied before validation, so
// later caller mutations cannot affect the broker. Records that repeat a
// reference with identical provenance collapse to one; records that repeat a
// reference with different provenance are rejected. An empty definition set
// is valid. New rejects the whole set if any record is invalid; a loader that
// skips bad definitions filters them first with DefinitionRecord.Validate.
//
// The signer must have an ECDSA P-256 public key, the ES256 algorithm that
// JWT-SVID permits. A raw *ecdsa.PrivateKey is copied; any other signer, such
// as a KMS handle, is retained as supplied.
func New(config Config, definitions []DefinitionRecord, signer crypto.Signer) (*Broker, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	signer, err := checkSigner(signer)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSigner, err)
	}
	snapshot, err := snapshotDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	return &Broker{config: config, signer: signer, definitions: snapshot, sessions: newSessionTable()}, nil
}

// Definition returns a copy of the loaded record for ref, and whether one
// exists. Mutating the returned record does not affect the broker.
func (b *Broker) Definition(ref DefinitionRef) (DefinitionRecord, bool) {
	rec, ok := b.definitions[ref]
	if !ok {
		return DefinitionRecord{}, false
	}
	return rec.clone(), true
}

func checkSigner(signer crypto.Signer) (crypto.Signer, error) {
	if signer == nil {
		return nil, errors.New("a signer is required")
	}
	// A raw key is caller-owned memory: copy it so caller mutation cannot alter it.
	if key, ok := signer.(*ecdsa.PrivateKey); ok {
		if key == nil {
			return nil, errors.New("a signer is required")
		}
		if key.Curve != elliptic.P256() {
			return nil, errors.New("only ECDSA P-256 (ES256) signers are supported")
		}
		return copyP256Key(key)
	}
	pub, err := publicKey(signer)
	if err != nil {
		return nil, err
	}
	if ecPub, ok := pub.(*ecdsa.PublicKey); !ok || ecPub == nil || ecPub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("only ECDSA P-256 (ES256) signers are supported, got public key type %T", pub)
	}
	return signer, nil
}

// copyP256Key returns an independent copy of key. The copy derives its public
// key from the private scalar, so a key whose stored public half disagrees is
// rejected rather than silently corrected. A key with missing fields makes the
// standard library panic; that becomes an error.
func copyP256Key(key *ecdsa.PrivateKey) (dup *ecdsa.PrivateKey, err error) {
	defer func() {
		if recover() != nil {
			dup, err = nil, errors.New("ecdsa private key is incomplete")
		}
	}()
	raw, err := key.Bytes()
	if err != nil {
		return nil, fmt.Errorf("ecdsa private key: %w", err)
	}
	dup, err = ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("ecdsa private key: %w", err)
	}
	if !dup.PublicKey.Equal(&key.PublicKey) {
		return nil, errors.New("ecdsa public key does not match the private key")
	}
	return dup, nil
}

// publicKey calls signer.Public once, turning a panic from a malformed signer,
// such as a typed nil pointer, into an error.
func publicKey(signer crypto.Signer) (pub crypto.PublicKey, err error) {
	defer func() {
		if recover() != nil {
			pub, err = nil, errors.New("signer panicked reporting its public key")
		}
	}()
	return signer.Public(), nil
}

func snapshotDefinitions(records []DefinitionRecord) (map[DefinitionRef]DefinitionRecord, error) {
	snapshot := make(map[DefinitionRef]DefinitionRecord, len(records))
	for i, rec := range records {
		rec = rec.clone()
		if err := rec.Validate(); err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		if existing, ok := snapshot[rec.Ref]; ok {
			if !existing.equal(rec) {
				return nil, fmt.Errorf("%w: record %d repeats %+v with different provenance", ErrConflictingDefinition, i, rec.Ref)
			}
			continue
		}
		snapshot[rec.Ref] = rec
	}
	return snapshot, nil
}
