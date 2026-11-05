package codec

import "math"

// Marshaler is the generated, compositional encoding contract for a value.
// CheckCBOR traverses one value using shared accounting, at the given wire depth
// and positive immediate field cap, then checks the owner's local invariants.
// AppendCBOR requires a successful check of the same stable value; it appends
// without validation, accounting, or independent admission. Neither method resets
// shared state. A nil Budget is invalid, not a request for default state.
// Whole-value helpers are intended for generated value roots, not nil pointers.
type Marshaler interface {
	CheckCBOR(b *Budget, depth, fieldMax int) error
	AppendCBOR(dst []byte) []byte
}

// Check validates all global policy once, then traverses value with one Budget.
// Input must remain stable for the operation. No output buffer is allocated.
func Check[T Marshaler](value T, limits Limits) error {
	b, err := NewBudget(limits)
	if err != nil {
		return err
	}
	return value.CheckCBOR(&b, 0, math.MaxInt)
}

// Encode checks the complete value before allocating a conservatively sized
// output buffer and appending once. AppendCBOR does not repeat checks or charge
// accounting. Input must remain stable across both passes. Failure returns nil.
func Encode[T Marshaler](value T, limits Limits) ([]byte, error) {
	b, err := NewBudget(limits)
	if err != nil {
		return nil, err
	}
	if err := value.CheckCBOR(&b, 0, math.MaxInt); err != nil {
		return nil, err
	}
	return value.AppendCBOR(make([]byte, 0, b.EstimatedBytes())), nil
}
