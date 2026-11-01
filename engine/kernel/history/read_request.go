package history

// ReadRequest bounds one fixed-endpoint read. Both nonzero boundaries must be
// accepted in the same history. Appends may proceed concurrently.
type ReadRequest struct {
	// After is exclusive; the all-zero reference means before the first commit.
	After CommitRef
	// Through is the accepted inclusive upper endpoint, never zero.
	Through CommitRef
	// MaxCommits is positive and must not exceed Policy.MaxPageCommits.
	MaxCommits int
}

// Validate reports whether the request respects an already valid policy.
// History and its Backend must also verify acceptance of both boundaries.
func (r ReadRequest) Validate(policy Policy) error {
	if r.After != (CommitRef{}) && r.After.Validate() != nil {
		return invalid("read after reference is invalid")
	}
	if err := r.Through.Validate(); err != nil {
		return invalid("read through reference is invalid")
	}
	if r.After.Sequence > r.Through.Sequence || (r.After.Sequence == r.Through.Sequence && r.After != r.Through) {
		return invalid("read boundaries are inconsistent")
	}
	if r.MaxCommits <= 0 || r.MaxCommits > policy.MaxPageCommits {
		return invalid("read commit limit is outside policy")
	}
	return nil
}
