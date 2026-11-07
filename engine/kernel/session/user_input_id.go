package session

import "errors"

// UserInputID identifies a user-input occurrence within one run, not its content.
// It is comparable, contains no mutable storage, and has an invalid zero value.
// Obtain IDs from session allocation or inspection, not numeric construction.
type UserInputID struct {
	// runID is the owning run, including its history scope.
	runID RunID
	// ordinal is the one-based input ordinal within that run.
	ordinal uint64
}

// RunID returns the enclosing run identity, or zero for zero.
func (v UserInputID) RunID() RunID { return v.runID }

// validateOrdinal checks the local occurrence ordinal.
func (v UserInputID) validateOrdinal() error {
	if v.ordinal == 0 {
		return errors.New("input ordinal must be positive")
	}
	return nil
}

// Validate checks local identity validity without mutation or history lookup.
// It does not establish acceptance or monotonic allocation.
func (v UserInputID) Validate() error {
	if err := v.runID.Validate(); err != nil {
		return errors.New("invalid input run ID")
	}
	return v.validateOrdinal()
}

// Equal reports whether both run-scoped occurrences are identical.
func (v UserInputID) Equal(other UserInputID) bool { return v == other }
