package session

import (
	"errors"
	"math"
)

// ModelRequestID identifies one engine-visible invocation within a run. It is
// comparable and has no mutable storage. Zero is invalid.
type ModelRequestID struct {
	// RunID is the owning run, including history scope.
	RunID RunID
	// Ordinal is the one-based request ordinal within RunID.
	Ordinal uint64
}

// Validate checks local identity validity, not accepted allocation or membership.
func (v ModelRequestID) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return errors.New("invalid model request run ID")
	}
	if v.Ordinal == 0 {
		return errors.New("model request ordinal must be positive")
	}
	return nil
}

// Clone returns v, which contains no mutable storage.
func (v ModelRequestID) Clone() ModelRequestID { return v }

// Equal reports whether two run-scoped requests are identical.
func (v ModelRequestID) Equal(other ModelRequestID) bool { return v == other }

// Next returns the next request ordinal in the same run, or zero on invalid input
// or exhaustion. It does not allocate or reserve an accepted request.
func (v ModelRequestID) Next() (ModelRequestID, error) {
	if err := v.Validate(); err != nil {
		return ModelRequestID{}, err
	}
	if v.Ordinal == math.MaxUint64 {
		return ModelRequestID{}, ErrOrdinalExhausted
	}
	return ModelRequestID{RunID: v.RunID, Ordinal: v.Ordinal + 1}, nil
}

// RequestedCallID identifies one requested-call occurrence. Ordinal counts only
// calls in its response order, never content items. It is comparable; zero is invalid.
type RequestedCallID struct {
	// RequestID identifies the response that contains this call.
	RequestID ModelRequestID
	// Ordinal is the one-based call ordinal among requested calls in that response.
	Ordinal uint64
}

// Validate checks local identity validity, not response membership or call order.
func (v RequestedCallID) Validate() error {
	if err := v.RequestID.Validate(); err != nil {
		return errors.New("invalid requested call request ID")
	}
	if v.Ordinal == 0 {
		return errors.New("requested call ordinal must be positive")
	}
	return nil
}

// Clone returns v, which contains no mutable storage.
func (v RequestedCallID) Clone() RequestedCallID { return v }

// Equal reports whether two requested-call occurrences are identical.
func (v RequestedCallID) Equal(other RequestedCallID) bool { return v == other }
