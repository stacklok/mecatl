package value

import (
	"errors"
	"math"
	"strconv"

	"github.com/jcalabro/atmos/cbor"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// ErrDepthExceeded identifies traversal beyond the fixed local depth ceiling of
// 128. Fallible operations return it; Clone and Equal panic with it.
var ErrDepthExceeded = errors.New("structured value depth exceeds 128")

const maxLocalDepth = 128

// Kind is the closed in-memory vocabulary of structured values. Its numeric values
// are not a persistence or wire contract; CBOR encoding is selected by each case.
// InvalidKind is not null.
type Kind uint8

const (
	// InvalidKind identifies an uninitialized Value or View.
	InvalidKind Kind = iota
	// NullKind identifies explicit null.
	NullKind
	// BoolKind identifies a boolean.
	BoolKind
	// StringKind identifies UTF-8 text.
	StringKind
	// IntegerKind identifies an exact integer in [-2^63, 2^64-1].
	IntegerKind
	// ArrayKind identifies an ordered array, including empty.
	ArrayKind
	// MapKind identifies a string-keyed map, including empty.
	MapKind
)

// String returns a readable kind name. Unknown values include their numeric value
// for safe diagnostics and are not accepted as structured values.
func (k Kind) String() string {
	switch k {
	case InvalidKind:
		return "invalid"
	case NullKind:
		return "null"
	case BoolKind:
		return "bool"
	case StringKind:
		return "string"
	case IntegerKind:
		return "integer"
	case ArrayKind:
		return "array"
	case MapKind:
		return "map"
	default:
		return "Kind(" + strconv.FormatUint(uint64(k), 10) + ")"
	}
}

// Value is a mutable-authoring handle with private native-value representation.
// Zero is invalid at admission, but clones and compares ordinarily. Constructors
// may retain mutable collection backing; copying a Value does not detach it.
// Positive signed and unsigned integers have identical semantic and wire values.
// Native CBOR has no width/signedness envelope, byte strings, floats or tags here.
type Value struct {
	kind   Kind
	flag   bool   // Boolean value, or negative integer sign.
	number uint64 // Nonnegative integer, or CBOR negative argument (-1 - value).
	text   string
	array  []Value
	object Map
}

// Null constructs explicit null, distinct from zero Value and empty collections.
func Null() Value { return Value{kind: NullKind} }

// Bool constructs a boolean.
func Bool(v bool) Value { return Value{kind: BoolKind, flag: v} }

// String constructs text; UTF-8 and size are checked at admission/encoding.
func String(v string) Value { return Value{kind: StringKind, text: v} }

// Int64 constructs an exact integer, normalizing nonnegative values to Uint64.
func Int64(v int64) Value {
	if v >= 0 {
		return Uint64(uint64(v))
	}
	return Value{kind: IntegerKind, flag: true, number: uint64(-(v + 1))} // #nosec G115 -- v < 0 bounds -(v+1) to [0, MaxInt64].
}

// Uint64 constructs an exact nonnegative integer.
func Uint64(v uint64) Value { return Value{kind: IntegerKind, number: v} }

// Array retains v without copying. Nil means an explicit empty array, not null.
// Keep v stable during operations and for the lifetime of borrowed views.
func Array(v []Value) Value { return Value{kind: ArrayKind, array: v} }

// Object retains v without copying. Nil means an explicit empty map, not null.
// Keep v stable during operations and for the lifetime of borrowed views.
func Object(v Map) Value { return Value{kind: MapKind, object: v} }

// MarshalJSON encodes native JSON, sorting map keys lexically and using standard
// library string escaping. Zero, invalid UTF-8 and traversal beyond local depth
// 128 (including cycles) return an error and nil bytes. Keep all backing stable.
// Work is linear in visited data plus per-map key sorting; storage is output,
// temporary key/string buffers and a depth-bounded stack, not a cloned value tree.
// No operational codec admission limits are applied.
func (v Value) MarshalJSON() ([]byte, error) { return marshalJSON(Borrow(v)) }

// Admit validates v under limits before cloning mutable descendants. It publishes
// only an immutable view; failure returns a zero View. Inputs must remain stable
// across both passes. Shared subtrees are detached per occurrence, not preserved.
func (v Value) Admit(limits codec.Limits) (View, error) {
	if err := codec.Check(v, limits); err != nil {
		return View{}, err
	}
	return Borrow(v.Clone()), nil
}

// CheckCBOR checks one native value using shared accounting. depth is enclosing
// wire depth; the independent local ceiling starts at zero for this primitive.
// fieldMax must be positive and caps only the immediate string/array/map, never
// descendants. No sorting, copying or output allocation occurs. Zero is invalid.
func (v Value) CheckCBOR(b *codec.Budget, depth, fieldMax int) error {
	if b == nil || fieldMax <= 0 {
		return codec.ErrInvalidLimits
	}
	return v.check(b, depth, fieldMax, 0)
}

// check tracks local value depth separately from the shared wire depth. Container
// declarations and local child depth are checked before any child traversal.
func (v Value) check(b *codec.Budget, depth, fieldMax, local int) error {
	if local > maxLocalDepth {
		return ErrDepthExceeded
	}
	switch v.kind {
	case NullKind, BoolKind, IntegerKind:
		return b.CheckScalar(depth)
	case StringKind:
		return b.CheckString(v.text, depth, fieldMax)
	case ArrayKind:
		if local == maxLocalDepth && len(v.array) != 0 {
			return ErrDepthExceeded
		}
		if err := b.CheckArray(len(v.array), depth, fieldMax); err != nil {
			return err
		}
		for _, child := range v.array {
			if err := child.check(b, depth+1, math.MaxInt, local+1); err != nil {
				return err
			}
		}
		return nil
	case MapKind:
		return v.object.check(b, depth, fieldMax, local)
	default:
		return codec.ErrInvalidData
	}
}

// AppendCBOR appends a value already admitted by CheckCBOR, without repeating
// validation/accounting. The same input must remain stable between both calls.
// Use codec.Encode at trust boundaries; unchecked cyclic/invalid inputs violate
// this method's precondition. Map ordering may allocate a temporary key slice.
func (v Value) AppendCBOR(dst []byte) []byte {
	switch v.kind {
	case NullKind:
		return append(dst, 0xf6)
	case BoolKind:
		return cbor.AppendBool(dst, v.flag)
	case StringKind:
		return cbor.AppendText(dst, v.text)
	case IntegerKind:
		if v.flag {
			return cbor.AppendInt(dst, -1-int64(v.number)) // #nosec G115 -- private negative argument is bounded by Int64 and Reader.Int to MaxInt64.
		}
		return cbor.AppendUint(dst, v.number)
	case ArrayKind:
		dst = cbor.AppendArrayHeader(dst, uint64(len(v.array)))
		for _, child := range v.array {
			dst = child.AppendCBOR(dst)
		}
		return dst
	case MapKind:
		return v.object.AppendCBOR(dst)
	default:
		panic(codec.ErrInvalidData)
	}
}

// ReadCBOR reads a native value into fresh storage with shared reader accounting.
// It publishes atomically on success; on failure receiver storage is unchanged.
// Discard the reader after errors. It never calls Done. depth is wire depth;
// local depth starts at zero independently. fieldMax caps only this value.
func (v *Value) ReadCBOR(r *codec.Reader, depth, fieldMax int) error {
	if v == nil {
		return codec.ErrInvalidData
	}
	if r == nil || fieldMax <= 0 {
		return codec.ErrInvalidLimits
	}
	out, err := read(r, depth, fieldMax, 0)
	if err != nil {
		return err
	}
	*v = out
	return nil
}

// read composes only checked Reader primitives; declared counts and local depth
// are admitted before collection allocation. It never publishes partial storage.
func read(r *codec.Reader, depth, fieldMax, local int) (Value, error) {
	if local > maxLocalDepth {
		return Value{}, ErrDepthExceeded
	}
	kind, err := r.PeekKind()
	if err != nil {
		return Value{}, err
	}
	var out Value
	switch kind {
	case codec.NullKind:
		out = Null()
		err = r.Null(depth)
	case codec.BoolKind:
		var x bool
		x, err = r.Bool(depth)
		out = Bool(x)
	case codec.IntKind:
		var x int64
		x, err = r.Int(depth)
		out = Int64(x)
	case codec.UintKind:
		var x uint64
		x, err = r.Uint(depth)
		out = Uint64(x)
	case codec.StringKind:
		var x string
		x, err = r.String(depth, fieldMax)
		out = String(x)
	case codec.ArrayKind:
		var n int
		n, err = r.Array(depth, fieldMax)
		if err != nil {
			return Value{}, err
		}
		if local == maxLocalDepth && n != 0 {
			return Value{}, ErrDepthExceeded
		}
		children := make([]Value, n)
		for i := range children {
			children[i], err = read(r, depth+1, math.MaxInt, local+1)
			if err != nil {
				return Value{}, err
			}
		}
		out = Array(children)
	case codec.MapKind:
		var x Map
		x, err = readMap(r, depth, fieldMax, local)
		out = Object(x)
	default:
		return Value{}, codec.ErrInvalidData
	}
	if err != nil {
		return Value{}, err
	}
	return out, nil
}

// Clone detaches all mutable descendants without validating. Zero stays zero;
// nil collections stay nil. Inputs must remain stable. It panics with
// ErrDepthExceeded when traversing beyond local depth 128 (including cycles).
// Breadth is not resource-limited; validate untrusted inputs before cloning.
func (v Value) Clone() Value { return v.clone(0) }

func (v Value) clone(local int) Value {
	if local > maxLocalDepth {
		panic(ErrDepthExceeded)
	}
	switch v.kind {
	case ArrayKind:
		if local == maxLocalDepth && len(v.array) != 0 {
			panic(ErrDepthExceeded)
		}
		if v.array != nil {
			children := make([]Value, len(v.array))
			for i, child := range v.array {
				children[i] = child.clone(local + 1)
			}
			v.array = children
		}
	case MapKind:
		v.object = v.object.clone(local)
	}
	return v
}

// Equal compares semantic values without validation; zero equals zero, nil and
// empty collections compare equally, and array order matters. Inputs must remain
// stable. It panics with ErrDepthExceeded if visited branches cross local depth
// 128, including cycles. Shallow mismatches may short-circuit without inspecting
// invalid/deeper branches. It does not validate unvisited branches or limit breadth.
func (v Value) Equal(other Value) bool { return v.equal(other, 0) }

func (v Value) equal(other Value, local int) bool {
	if local > maxLocalDepth {
		panic(ErrDepthExceeded)
	}
	if v.kind != other.kind {
		return false
	}
	switch v.kind {
	case BoolKind:
		return v.flag == other.flag
	case IntegerKind:
		return v.flag == other.flag && v.number == other.number
	case StringKind:
		return v.text == other.text
	case ArrayKind:
		if len(v.array) != len(other.array) {
			return false
		}
		for i, child := range v.array {
			if !child.equal(other.array[i], local+1) {
				return false
			}
		}
		return true
	case MapKind:
		return v.object.equal(other.object, local)
	default:
		return true // Includes ordinary zero-value equality, not admission.
	}
}
