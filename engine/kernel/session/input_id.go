package session

import (
	"errors"
	"math"
)

// ErrOrdinalExhausted means a valid occurrence has no representable successor.
var ErrOrdinalExhausted = errors.New("session occurrence ordinal exhausted")

// InputID identifies a user-input occurrence within one run, not its content.
// It is comparable, contains no mutable storage, and has an invalid zero value.
type InputID struct {
	// RunID is the owning run, including its history scope.
	RunID RunID
	// Ordinal is the one-based input ordinal within that run.
	Ordinal uint64
}

// validateOrdinal checks the local occurrence ordinal.
func (v InputID) validateOrdinal() error {
	if v.Ordinal == 0 {
		return errors.New("input ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or history lookup.
// It does not establish acceptance or monotonic allocation.
func (v InputID) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return errors.New("invalid input run ID")
	}
	return v.validateOrdinal()
}

// Clone returns v, which contains no mutable storage.
func (v InputID) Clone() InputID { return v }

// Equal reports whether both run-scoped occurrences are identical.
func (v InputID) Equal(other InputID) bool { return v == other }

// Next returns the next ordinal in the same run, or zero on invalid input or
// exhaustion. It neither allocates nor reserves an accepted occurrence.
func (v InputID) Next() (InputID, error) {
	if err := v.Validate(); err != nil {
		return InputID{}, err
	}
	if v.Ordinal == math.MaxUint64 {
		return InputID{}, ErrOrdinalExhausted
	}
	return InputID{RunID: v.RunID, Ordinal: v.Ordinal + 1}, nil
}
