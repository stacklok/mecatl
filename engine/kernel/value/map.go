package value

import (
	"cmp"
	"math"
	"slices"

	"github.com/jcalabro/atmos/cbor"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// Map is an ordinary mutable authoring map, usable directly in persistence DTOs.
// Keys are exact, case-sensitive UTF-8 text without normalization. Nil and empty
// mean the same empty map, never null; values must be initialized at admission.
// Assigning/copying a Map shares storage. Operations require stable inputs.
type Map map[string]Value

// MarshalJSON encodes m as a native JSON object; nil and empty both encode as {}.
// It uses Value.MarshalJSON's traversal, cost, failure and stable-backing contract.
func (m Map) MarshalJSON() ([]byte, error) { return marshalJSON(Object(m)) }

// Admit validates m before detaching all mutable descendants and publishing an
// immutable MapView. Failure returns a zero view. Inputs must remain stable across
// validation and cloning. Repeated shared subtrees count and detach per occurrence.
func (m Map) Admit(limits codec.Limits) (MapView, error) {
	if err := codec.Check(m, limits); err != nil {
		return MapView{}, err
	}
	return BorrowMap(m.Clone()), nil
}

// CheckCBOR checks this map with shared accounting, without allocating or sorting.
// depth is wire depth; the fixed local depth starts at zero for this primitive.
// fieldMax is positive and caps only this map's immediate number of pairs.
func (m Map) CheckCBOR(b *codec.Budget, depth, fieldMax int) error {
	if b == nil || fieldMax <= 0 {
		return codec.ErrInvalidLimits
	}
	return m.check(b, depth, fieldMax, 0)
}

func (m Map) check(b *codec.Budget, depth, fieldMax, local int) error {
	if local > maxLocalDepth || (local == maxLocalDepth && len(m) != 0) {
		return ErrDepthExceeded
	}
	if err := b.CheckMap(len(m), depth, fieldMax); err != nil {
		return err
	}
	for key, child := range m {
		if err := b.CheckString(key, depth+1, math.MaxInt); err != nil {
			return err
		}
		if err := child.check(b, depth+1, math.MaxInt, local+1); err != nil {
			return err
		}
	}
	return nil
}

// compareKeys implements canonical CBOR text-key order: byte length, then bytes.
func compareKeys(a, b string) int {
	if n := cmp.Compare(len(a), len(b)); n != 0 {
		return n
	}
	return cmp.Compare(a, b)
}

// AppendCBOR appends a map already admitted by CheckCBOR. Inputs must stay stable
// across both calls. It sorts a temporary key slice, but performs no validation or
// accounting. Use codec.Encode for untrusted inputs.
func (m Map) AppendCBOR(dst []byte) []byte {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareKeys)
	dst = cbor.AppendMapHeader(dst, uint64(len(m)))
	for _, key := range keys {
		dst = cbor.AppendText(dst, key)
		dst = m[key].AppendCBOR(dst)
	}
	return dst
}

// ReadCBOR reads a text-key map transactionally using one shared Reader. Counts
// are checked before allocation; keys must be unique and canonically ordered.
// Failure preserves the receiver and its reachable storage; discard the reader.
// It never calls Done. depth is wire depth, independent of local root depth zero;
// fieldMax is positive and caps only this map's immediate pair count.
func (m *Map) ReadCBOR(r *codec.Reader, depth, fieldMax int) error {
	if m == nil {
		return codec.ErrInvalidData
	}
	if r == nil || fieldMax <= 0 {
		return codec.ErrInvalidLimits
	}
	out, err := readMap(r, depth, fieldMax, 0)
	if err != nil {
		return err
	}
	*m = out
	return nil
}

func readMap(r *codec.Reader, depth, fieldMax, local int) (Map, error) {
	if local > maxLocalDepth {
		return nil, ErrDepthExceeded
	}
	n, err := r.Map(depth, fieldMax)
	if err != nil {
		return nil, err
	}
	if local == maxLocalDepth && n != 0 {
		return nil, ErrDepthExceeded
	}
	out := make(Map, n)
	previous := ""
	for i := range n {
		key, err := r.String(depth+1, math.MaxInt)
		if err != nil {
			return nil, err
		}
		if i > 0 && compareKeys(previous, key) >= 0 {
			return nil, codec.ErrInvalidData
		}
		child, err := read(r, depth+1, math.MaxInt, local+1)
		if err != nil {
			return nil, err
		}
		out[key] = child
		previous = key
	}
	return out, nil
}

// Clone detaches mutable descendants without validating; nil remains nil. It
// panics with ErrDepthExceeded if traversal exceeds local depth 128, including
// cycles. Inputs must remain stable; validate untrusted breadth before cloning.
func (m Map) Clone() Map { return m.clone(0) }

func (m Map) clone(local int) Map {
	if local > maxLocalDepth || (local == maxLocalDepth && len(m) != 0) {
		panic(ErrDepthExceeded)
	}
	if m == nil {
		return nil
	}
	out := make(Map, len(m))
	for key, child := range m {
		out[key] = child.clone(local + 1)
	}
	return out
}

// Equal compares membership and semantic values; nil and empty maps compare
// equally. It does not validate. Inputs must remain stable. It panics with
// ErrDepthExceeded if visited branches exceed local depth 128 (including cycles);
// shallow mismatches can return false without visiting deeper invalid branches.
func (m Map) Equal(other Map) bool { return m.equal(other, 0) }

func (m Map) equal(other Map, local int) bool {
	if local > maxLocalDepth {
		panic(ErrDepthExceeded)
	}
	if len(m) != len(other) {
		return false
	}
	for key, child := range m {
		right, ok := other[key]
		if !ok || !child.equal(right, local+1) {
			return false
		}
	}
	return true
}
