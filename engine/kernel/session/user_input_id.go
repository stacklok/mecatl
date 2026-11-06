package session

import (
	"errors"
	"math"
)

// ErrOrdinalExhausted means a valid occurrence has no representable successor.
var ErrOrdinalExhausted = errors.New("session occurrence ordinal exhausted")

// UserInputID identifies a user-input occurrence within one run, not its content.
// It is comparable, contains no mutable storage, and has an invalid zero value.
type UserInputID struct {
	// RunID is the owning run, including its history scope.
	RunID RunID
	// Ordinal is the one-based input ordinal within that run.
	Ordinal uint64
}

// validateOrdinal checks the local occurrence ordinal.
func (v UserInputID) validateOrdinal() error {
	if v.Ordinal == 0 {
		return errors.New("input ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or history lookup.
// It does not establish acceptance or monotonic allocation.
func (v UserInputID) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return errors.New("invalid input run ID")
	}
	return v.validateOrdinal()
}

// Clone returns v, which contains no mutable storage.
func (v UserInputID) Clone() UserInputID { return v }

// Equal reports whether both run-scoped occurrences are identical.
func (v UserInputID) Equal(other UserInputID) bool { return v == other }

// Next returns the next ordinal in the same run, or zero on invalid input or
// exhaustion. It neither allocates nor reserves an accepted occurrence.
func (v UserInputID) Next() (UserInputID, error) {
	if err := v.Validate(); err != nil {
		return UserInputID{}, err
	}
	if v.Ordinal == math.MaxUint64 {
		return UserInputID{}, ErrOrdinalExhausted
	}
	return UserInputID{RunID: v.RunID, Ordinal: v.Ordinal + 1}, nil
}
