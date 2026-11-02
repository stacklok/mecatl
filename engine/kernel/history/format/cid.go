package format

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

// CID identifies content using CIDv1, DAG-CBOR (0x71), and SHA-256. It is opaque
// and comparable, including as a map key. Its zero value is unset, not valid.
// A well-formed CID proves neither valid commit content nor history membership,
// acceptance, or authorization.
type CID struct {
	value contentid.CID
}

// ParseCID accepts only canonical lowercase, unpadded base32 multibase text for
// the supported CID profile. It rejects alternate spellings without normalizing
// them and returns an unset CID on failure. It does not verify content bytes.
func ParseCID(s string) (CID, error) {
	v, err := contentid.Parse(s)
	return CID{value: v}, err
}

// String returns canonical lowercase, unpadded base32 multibase text, or an
// empty string for an unset CID.
func (v CID) String() string {
	return v.value.String()
}

// contentCID hashes authoritative bytes without reencoding or exposing digests.
func contentCID(data []byte) CID {
	return CID{value: contentid.Sum(data)}
}

// link requires canonical tag 42, the identity multibase prefix, and the exact
// fixed CID profile; fixed bytes also exclude nonminimal binary varints.
func (d *cborDecoder) link() CID {
	d.literal(0xd8)
	d.literal(0x2a)
	value := d.scalar(2, 37)
	if d.err != nil {
		return CID{}
	}
	if len(value) != 37 || value[0] != 0 {
		d.err = errors.New("invalid parent CID profile")
		return CID{}
	}
	v, err := contentid.ParseBinary(value[1:])
	if err != nil {
		d.err = errors.New("invalid parent CID profile")
		return CID{}
	}
	return CID{value: v}
}
