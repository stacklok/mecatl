package codec

const (
	cidTag        = 42
	cidLinkLength = 37
)

// CheckCIDLink accounts the fixed DAG-CBOR link framing at depth: tag 42 and
// its 37-byte byte-string child consume two nodes, 55 estimated bytes, and no
// collection declaration. It does not inspect identity bytes or validate a CID
// profile; the owner must do that separately after successful accounting.
func (b *Budget) CheckCIDLink(depth int) error {
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

// CIDLink reads raw identity bytes from one canonical DAG-CBOR link at depth.
// It accepts only tag 42, a minimal 37-byte string, and a zero prefix. Exact
// length is checked before copying into the allocation-free, owning result.
// The owner must validate the CID profile. Failure returns a zero array; only
// the root operation calls Done to reject trailing input.
func (r *Reader) CIDLink(depth int) ([36]byte, error) {
	if r == nil {
		return [36]byte{}, ErrInvalidLimits
	}
	if err := r.budget.CheckCIDLink(depth); err != nil {
		return [36]byte{}, err
	}
	tag, err := r.typedHeader(6)
	if err != nil || tag != cidTag {
		return [36]byte{}, ErrInvalidData
	}
	length, err := r.typedHeader(2)
	if err != nil || length != cidLinkLength {
		return [36]byte{}, ErrInvalidData
	}
	if cidLinkLength > len(r.data)-r.pos || r.data[r.pos] != 0 {
		return [36]byte{}, ErrInvalidData
	}
	var value [36]byte
	copy(value[:], r.data[r.pos+1:r.pos+cidLinkLength])
	r.pos += cidLinkLength
	return value, nil
}

// AppendCIDLink appends the canonical 41-byte DAG-CBOR link framing and raw
// identity bytes. It performs no admission or profile validation: the caller
// must have checked the same stable value and accounted it with CheckCIDLink.
func AppendCIDLink(dst []byte, value [36]byte) []byte {
	dst = append(dst, 0xd8, cidTag, 0x58, cidLinkLength, 0)
	return append(dst, value[:]...)
}
