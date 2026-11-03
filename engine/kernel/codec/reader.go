package codec

import (
	"math"
	"unicode/utf8"

	"github.com/jcalabro/atmos/cbor"
)

// Reader strictly decodes one generated wire value with per-call accounting.
// Its zero value is invalid; use NewReader. It retains a borrowed view of input,
// so input backing storage must remain stable for the reader's full operation,
// through all reads and Done. Returned strings and bytes own storage. Copying forks
// its cursor/accounting; pass by pointer inside one operation. Errors are explicit,
// not sticky; reads return zero results on failure. Discard the reader and partial
// DTO after any error. No cursor rollback is promised. Reads and Done on a nil
// or zero Reader return ErrInvalidLimits.
// Generated callers own schema checks, key ordering, and exact child traversal.
type Reader struct {
	data   []byte
	pos    int
	budget Budget
}

// NewReader validates all global policy before checking complete input size. It
// neither scans nor clones input. Failure returns a zero Reader, never partially
// initialized state.
func NewReader(data []byte, limits Limits) (Reader, error) {
	budget, err := NewBudget(limits)
	if err != nil {
		return Reader{}, err
	}
	if len(data) > limits.MaxBytes {
		return Reader{}, ErrLimitExceeded
	}
	return Reader{data: data, budget: budget}, nil
}

// header uses only Atmos's qualified raw header primitive: minimal, definite
// headers without mutable dependency-global policies or typed integer narrowing.
func (r *Reader) header() (byte, uint64, error) {
	if r == nil || r.budget.limits.MaxBytes == 0 {
		return 0, 0, ErrInvalidLimits
	}
	major, n, next, err := cbor.ReadHeader(r.data, r.pos)
	if err != nil {
		return 0, 0, ErrInvalidData
	}
	r.pos = next
	return major, n, nil
}

// typedHeader rejects wrong major types, including null and unsupported values.
func (r *Reader) typedHeader(want byte) (uint64, error) {
	major, n, err := r.header()
	if err != nil {
		return 0, err
	}
	if major != want {
		return 0, ErrInvalidData
	}
	return n, nil
}

// Bool reads exactly canonical false/true and accounts one scalar at depth.
func (r *Reader) Bool(depth int) (bool, error) {
	if r == nil {
		return false, ErrInvalidLimits
	}
	if err := r.budget.CheckScalar(depth); err != nil {
		return false, err
	}
	if r.pos == len(r.data) || (r.data[r.pos] != 0xf4 && r.data[r.pos] != 0xf5) {
		return false, ErrInvalidData
	}
	v := r.data[r.pos] == 0xf5
	r.pos++
	return v, nil
}

// Int reads a minimal signed integer over the full int64 range. Generated callers
// must check narrower destination ranges before converting. It rejects unsigned
// values above MaxInt64 and negative arguments above MaxInt64, without overflow.
func (r *Reader) Int(depth int) (int64, error) {
	major, n, err := r.header()
	if err != nil {
		return 0, err
	}
	if (major != 0 && major != 1) || n > math.MaxInt64 {
		return 0, ErrInvalidData
	}
	if err := r.budget.CheckScalar(depth); err != nil {
		return 0, err
	}
	if major == 1 {
		return -1 - int64(n), nil // #nosec G115 -- bounded by MaxInt64 above.
	}
	return int64(n), nil // #nosec G115 -- bounded by MaxInt64 above.
}

// Uint reads a minimal unsigned integer over the full uint64 range. Generated
// callers check narrower destination ranges before converting; negatives fail.
func (r *Reader) Uint(depth int) (uint64, error) {
	n, err := r.typedHeader(0)
	if err != nil {
		return 0, err
	}
	if err := r.budget.CheckScalar(depth); err != nil {
		return 0, err
	}
	return n, nil
}

// length bounds an untrusted declaration before converting it to machine int.
// Field and global caps are positive. It does not inspect scalar contents.
func (r *Reader) length(major byte, fieldMax, globalMax int) (int, error) {
	if r == nil || fieldMax <= 0 || r.budget.limits.MaxBytes == 0 {
		return 0, ErrInvalidLimits
	}
	n, err := r.typedHeader(major)
	if err != nil {
		return 0, err
	}
	if n > uint64(fieldMax) || n > uint64(globalMax) { // #nosec G115 -- fieldMax is positive; globalMax comes from validated policy.
		return 0, ErrLimitExceeded
	}
	return int(n), nil // #nosec G115 -- n is bounded by positive int caps above.
}

// scalar admits a borrowed view only after declaration, remaining-input, and
// budget checks. exactLength is -1 for variable lengths, otherwise declarations
// must match it before content access. Only owning public reads expose contents.
func (r *Reader) scalar(major byte, depth, fieldMax, globalMax, exactLength int) ([]byte, error) {
	n, err := r.length(major, fieldMax, globalMax)
	if err != nil {
		return nil, err
	}
	if exactLength >= 0 && n != exactLength {
		return nil, ErrInvalidData
	}
	if n > len(r.data)-r.pos {
		return nil, ErrInvalidData
	}
	if err := r.budget.scalar(n, depth, fieldMax, globalMax); err != nil {
		return nil, err
	}
	v := r.data[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

// String bounds length and accounting before UTF-8 validation and an owning
// copy. Tiny results do not retain the whole input. fieldMax is positive; pass
// MaxStringBytes when untagged. This also reads and accounts map/schema keys.
func (r *Reader) String(depth, fieldMax int) (string, error) {
	if r == nil {
		return "", ErrInvalidLimits
	}
	v, err := r.scalar(3, depth, fieldMax, r.budget.limits.MaxStringBytes, -1)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(v) {
		return "", ErrInvalidData
	}
	return string(v), nil
}

// Bytes bounds length and accounting before copying opaque contents into owned,
// nonnil storage (including empty). fieldMax is positive; pass MaxByteStringBytes
// when untagged. Use FixedBytes for fixed-array destinations.
func (r *Reader) Bytes(depth, fieldMax int) ([]byte, error) {
	return r.bytes(depth, fieldMax, -1)
}

// FixedBytes reads an owning byte string of exactly length bytes. A mismatched
// declaration fails before any content copy or allocation. length must be
// nonnegative; field/global caps still apply, including for zero-length arrays.
func (r *Reader) FixedBytes(depth, fieldMax, length int) ([]byte, error) {
	if length < 0 {
		return nil, ErrInvalidLimits
	}
	return r.bytes(depth, fieldMax, length)
}

func (r *Reader) bytes(depth, fieldMax, exactLength int) ([]byte, error) {
	if r == nil {
		return nil, ErrInvalidLimits
	}
	v, err := r.scalar(2, depth, fieldMax, r.budget.limits.MaxByteStringBytes, exactLength)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// collection checks minimum remaining wire bytes and reserves aggregate
// declarations/nodes/headers before returning an allocation-safe machine count.
func (r *Reader) collection(major byte, depth, fieldMax, globalMax, nodesPerItem int) (int, error) {
	n, err := r.length(major, fieldMax, globalMax)
	if err != nil {
		return 0, err
	}
	if n > (len(r.data)-r.pos)/nodesPerItem {
		return 0, ErrInvalidData
	}
	if err := r.budget.collection(n, depth, fieldMax, globalMax, nodesPerItem); err != nil {
		return 0, err
	}
	return n, nil
}

// Array reads a definite minimal array header, reserving all declared children
// before typed allocation. fieldMax is positive; pass MaxArrayElements when
// untagged. Read exactly the returned count of elements at depth+1.
func (r *Reader) Array(depth, fieldMax int) (int, error) {
	if r == nil {
		return 0, ErrInvalidLimits
	}
	return r.collection(4, depth, fieldMax, r.budget.limits.MaxArrayElements, 1)
}

// Map reads a definite minimal map header, reserving pairs, key/value nodes, and
// their header estimates before typed allocation. fieldMax is positive; pass
// MaxMapPairs when untagged. Read exactly count keys and values at depth+1;
// generated code rejects unknown/missing/duplicate/unordered keys itself.
func (r *Reader) Map(depth, fieldMax int) (int, error) {
	if r == nil {
		return 0, ErrInvalidLimits
	}
	return r.collection(5, depth, fieldMax, r.budget.limits.MaxMapPairs, 2)
}

// Done requires exactly one fully traversed value and no trailing input. It does
// not prove schema validity or replace generated presence/order/range checks.
func (r *Reader) Done() error {
	if r == nil || r.budget.limits.MaxBytes == 0 {
		return ErrInvalidLimits
	}
	if r.pos != len(r.data) || r.budget.visited == 0 || r.budget.visited != r.budget.nodes {
		return ErrInvalidData
	}
	return nil
}
