package history

import "github.com/stacklok/mecatl/engine/kernel/history/format"

// Policy fixes the resource and retry bounds for one History. It is immutable for
// that instance: no field has a default, and all fields must be positive.
type Policy struct {
	// CommitLimits bounds canonical encoding and decoded DTO fields/counts.
	// Its byte cap also applies before backend read materialization.
	CommitLimits format.Limits
	// RetryWindow is the count of accepted commit ordinals eligible for idempotent retry.
	RetryWindow int64
	// MaxPageBytes caps total canonical bytes in every Read page independently
	// of commit limits; consumers cannot override it.
	MaxPageBytes int
	// MaxPageCommits bounds the number of returned commits, including their Go
	// allocation overhead, independently of commit limits.
	MaxPageCommits int
}

// Validate reports whether every configured policy bound is positive. It does
// not mutate the policy.
func (p Policy) Validate() error {
	if err := p.CommitLimits.Validate(); err != nil {
		return invalid("commit limits are invalid")
	}
	if p.RetryWindow <= 0 {
		return invalid("retry window must be positive")
	}
	if p.MaxPageBytes <= 0 {
		return invalid("maximum page bytes must be positive")
	}
	if p.MaxPageCommits <= 0 {
		return invalid("maximum page commits must be positive")
	}
	return nil
}
