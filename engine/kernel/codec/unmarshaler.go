package codec

import "math"

// Unmarshaler is the generated, compositional decoding contract for a pointer.
// ReadCBOR reads one nested value at the given wire depth and positive immediate
// field cap using shared accounting. It reads into fresh temporary storage and
// assigns the receiver only after structural and local semantic success. Failure
// preserves the receiver and its reachable storage, but need not roll back reader
// state; discard the operation after error. Success owns decoded mutable storage.
// Nil receivers and Readers are invalid, not requests for default state. Nested
// methods never call Done or reset the Reader; only the root Decode calls Done.
type Unmarshaler interface {
	ReadCBOR(r *Reader, depth, fieldMax int) error
}

// Decode validates all global policy once and reads one complete value, rejecting
// trailing input. P is inferred as *T, so callers write Decode[T](data, limits).
// Every failure returns zero T, including errors after a successful nested read.
func Decode[T any, P interface {
	*T
	Unmarshaler
}](data []byte, limits Limits) (T, error) {
	var value T
	r, err := NewReader(data, limits)
	if err != nil {
		return value, err
	}
	if err := P(&value).ReadCBOR(&r, 0, math.MaxInt); err != nil {
		var zero T
		return zero, err
	}
	if err := r.Done(); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}
