package format

import (
	"encoding/base32"
	"errors"
)

// cidEncoding is lowercase, unpadded base32; the multibase prefix is separate.
var cidEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// CID identifies content using CIDv1, DAG-CBOR (0x71), and SHA-256. It is opaque
// and comparable, including as a map key. Its zero value is unset, not valid.
// A well-formed CID proves neither valid commit content nor history membership,
// acceptance, or authorization.
type CID struct {
	// The fixed profile has four single-byte varints and a 32-byte digest.
	binary [36]byte
}

// ParseCID accepts only canonical lowercase, unpadded base32 multibase text for
// the supported CID profile. It rejects alternate spellings without normalizing
// them and returns an unset CID on failure. It does not verify content bytes.
func ParseCID(s string) (CID, error) {
	if len(s) != 59 || s[0] != 'b' {
		return CID{}, errors.New("expected canonical base32 CID text")
	}
	var v CID
	n, err := cidEncoding.Decode(v.binary[:], []byte(s[1:]))
	if err != nil || n != len(v.binary) {
		return CID{}, errors.New("invalid base32 CID text")
	}
	if v.binary[0] != 1 || v.binary[1] != 0x71 || v.binary[2] != 0x12 || v.binary[3] != 32 {
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
