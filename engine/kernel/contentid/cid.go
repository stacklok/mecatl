package contentid

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
)

// ErrInvalidExpected means verification was requested with an unset CID.
var ErrInvalidExpected = errors.New("invalid expected content CID")

// ErrMismatch means the exact supplied bytes do not match the expected CID.
var ErrMismatch = errors.New("content CID mismatch")

// cidEncoding is lowercase, unpadded base32; the multibase prefix is separate.
var cidEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// CID is an opaque, comparable CIDv1/DAG-CBOR/SHA-256 identifier, usable as a
// map key. Its zero value is unset, not valid. It owns its fixed-size storage.
// A valid CID establishes identity only, not validity or authority of content.
// The DAG-CBOR profile label does not imply that the identified bytes are CBOR.
// Its CBOR representation is a DAG-CBOR tag-42 link, not text.
//
//cborgen:validate validateCBOR
type CID struct {
	binary [36]byte `cbor:",cid"` //nolint:revive // cborgen's fixed CID-link representation, not a standard CBOR tag option.
}

// validateCBOR reuses the binary parser's fixed-profile authority.
func (v CID) validateCBOR() error {
	_, err := ParseBinary(v.binary[:])
	return err
}

// Sum identifies the exact supplied bytes, without interpreting or normalizing
// them. It performs O(len(data)) work, does not clone, retain, or mutate data,
// and applies no size policy. The caller bounds data and keeps it stable.
func Sum(data []byte) CID {
	v := CID{binary: [36]byte{1, 0x71, 0x12, 32}}
	digest := sha256.Sum256(data)
	copy(v.binary[4:], digest[:])
	return v
}

// Verify compares the exact original data bytes with an explicitly supplied
// expected CID. An unset expected CID returns ErrInvalidExpected, never skips
// verification; different bytes return ErrMismatch. Errors support errors.Is
// and contain no input contents. Work and input ownership are as for Sum.
// Success proves identity, not schema validity, semantics, or authorization.
func Verify(data []byte, expected CID) error {
	if expected == (CID{}) {
		return ErrInvalidExpected
	}
	if Sum(data) != expected {
		return ErrMismatch
	}
	return nil
}

// Parse accepts only canonical lowercase, unpadded base32 multibase text for
// this fixed profile. Alternate spellings are rejected, not normalized. Failure
// returns an unset CID. Parsing does not verify any content bytes.
func Parse(s string) (CID, error) {
	if len(s) != 59 || s[0] != 'b' {
		return CID{}, errors.New("expected canonical base32 CID text")
	}
	var v CID
	n, err := cidEncoding.Decode(v.binary[:], []byte(s[1:]))
	if err != nil || n != len(v.binary) {
		return CID{}, errors.New("invalid base32 CID text")
	}
	if !validProfile(v.binary) {
		return CID{}, errors.New("expected CIDv1 with DAG-CBOR and SHA-256")
	}
	// Decode permits nonzero padding bits and ignores CR/LF. Round-tripping
	// rejects every spelling other than the canonical text for these bytes.
	if v.String() != s {
		return CID{}, errors.New("noncanonical CID text")
	}
	return v, nil
}

// String returns canonical lowercase, unpadded base32 multibase text, or an
// empty string for an unset CID.
func (v CID) String() string {
	if v == (CID{}) {
		return ""
	}
	return "b" + cidEncoding.EncodeToString(v.binary[:])
}

// Binary returns an independent fixed-size binary CID (four minimal one-byte
// varints followed by the digest), without a multibase prefix or CBOR framing.
// An unset CID returns all zero bytes, which ParseBinary rejects.
func (v CID) Binary() [36]byte {
	return v.binary
}

// ParseBinary accepts exactly the fixed binary CID profile, without a multibase
// prefix or CBOR framing. It rejects nonminimal varints and other profiles,
// copies data into owned storage, and returns an unset CID on failure.
// It does not verify content bytes. Input must remain stable during the call.
func ParseBinary(data []byte) (CID, error) {
	if len(data) != 36 {
		return CID{}, errors.New("expected 36-byte binary CID")
	}
	var v CID
	copy(v.binary[:], data)
	if !validProfile(v.binary) {
		return CID{}, errors.New("expected CIDv1 with DAG-CBOR and SHA-256")
	}
	return v, nil
}

// validProfile checks the four fixed minimal varints, not the opaque digest.
func validProfile(binary [36]byte) bool {
	return binary[0] == 1 && binary[1] == 0x71 && binary[2] == 0x12 && binary[3] == 32
}
