package codec

import "github.com/stacklok/mecatl/engine/kernel/contentid"

const (
	cidTag        = 42
	cidLinkLength = 37
)

// CheckCID validates and accounts one fixed-profile DAG-CBOR CID link at depth.
// The link is tag 42 and its 37-byte byte-string child, so it consumes two nodes,
// 55 estimated bytes, and no collection-element declaration. The zero CID is
// invalid. Pointers are not nodes; generated callers pass the tag's wire depth.
func (b *Budget) CheckCID(value contentid.CID, depth int) error {
	if !validCID(value) {
		return ErrInvalidData
	}
	return b.checkCID(depth)
}

// checkCID accounts the fixed tag and its byte-string child. The reader calls it
// before inspecting wire contents so policy rejection precedes content access.
func (b *Budget) checkCID(depth int) error {
	if depth < 0 {
		return ErrInvalidData
	}
	if err := b.visit(depth); err != nil {
		return err
	}
	// depth is strictly below MaxDepth, so depth+1 cannot overflow and the
	// byte-string child remains within the configured wire nesting limit.
	if depth >= b.limits.MaxDepth {
		return ErrLimitExceeded
	}
	if err := b.reserveNodes(1); err != nil {
		return err
	}
	return b.scalar(cidLinkLength, depth+1, cidLinkLength, b.limits.MaxByteStringBytes)
}

// CID reads one canonical fixed-profile DAG-CBOR CID link at depth. It accepts
// only tag 42 followed by a minimally headed 37-byte string whose first byte is
// zero and whose remaining bytes pass contentid.ParseBinary. The returned CID
// owns its storage; failure returns an unset CID. Call Done after the enclosing
// generated value to reject trailing input.
func (r *Reader) CID(depth int) (contentid.CID, error) {
	if err := r.budget.checkCID(depth); err != nil {
		return contentid.CID{}, err
	}
	tag, err := r.typedHeader(6)
	if err != nil || tag != cidTag {
		return contentid.CID{}, ErrInvalidData
	}
	length, err := r.typedHeader(2)
	if err != nil || length != cidLinkLength {
		return contentid.CID{}, ErrInvalidData
	}
	if cidLinkLength > len(r.data)-r.pos {
		return contentid.CID{}, ErrInvalidData
	}
	if r.data[r.pos] != 0 {
		return contentid.CID{}, ErrInvalidData
	}
	value, err := contentid.ParseBinary(r.data[r.pos+1 : r.pos+cidLinkLength])
	if err != nil {
		return contentid.CID{}, ErrInvalidData
	}
	r.pos += cidLinkLength
	return value, nil
}

// AppendCID appends the canonical 41-byte fixed-profile DAG-CBOR CID link for
// value. An unset or invalid value returns ErrInvalidData and leaves dst
// unchanged. Callers must have accounted the value separately with CheckCID.
func AppendCID(dst []byte, value contentid.CID) ([]byte, error) {
	if !validCID(value) {
		return dst, ErrInvalidData
	}
	binary := value.Binary()
	dst = append(dst, 0xd8, cidTag, 0x58, cidLinkLength, 0)
	dst = append(dst, binary[:]...)
	return dst, nil
}

// validCID delegates fixed-profile validation to contentid's profile authority.
func validCID(value contentid.CID) bool {
	binary := value.Binary()
	_, err := contentid.ParseBinary(binary[:])
	return err == nil
}
