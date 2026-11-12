package session

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// ErrInvalidPolicy identifies incomplete operational policy or an unusable Session.
var ErrInvalidPolicy = errors.New("invalid session policy or uninitialized session")

// Policy is copied at construction and has no defaults. All fields must be
// positive, and each soft bound must not exceed its hard counterpart. Soft bounds
// govern ordinary submissions only; a lone minimal exceptional bulk ending and
// replay use hard bounds. These operational limits do not change the persistence schema.
type Policy struct {
	// CommitLimits bounds canonical commit encoding, including opaque payloads.
	CommitLimits codec.Limits
	// PayloadLimits bounds each independently decoded lifecycle payload.
	PayloadLimits codec.Limits
	// MaxRecordsPerCommit bounds complete submission and external admission,
	// before governed decode allocations, not provisional builder construction.
	MaxRecordsPerCommit int
	// SoftMaxRetainedRecords bounds ordinary writes; cleanup may use the hard bound.
	SoftMaxRetainedRecords int
	// SoftMaxPayloadBytes bounds cumulative payload bytes after ordinary writes.
	SoftMaxPayloadBytes uint64
	// SoftMaxDecodedWork bounds conservative decoded work after ordinary writes.
	SoftMaxDecodedWork uint64
	// MaxRetainedRecords is the absolute cumulative bound, including replay.
	MaxRetainedRecords int
	// MaxPayloadBytes bounds cumulative canonical payload bytes, not envelopes.
	MaxPayloadBytes uint64
	// MaxDecodedWork bounds retained record count times the sum of per-payload
	// MaxBytes, MaxNodes and MaxCollectionElements ceilings. Each byte, node and
	// collection slot counts as one conservative admission unit; this is not
	// exact heap accounting. Admission precedes payload decode and candidate staging.
	MaxDecodedWork uint64
}

// Validate checks positive explicit limits without allocating or encoding data.
func (p Policy) Validate() error {
	if !positiveLimits(p.CommitLimits) || !positiveLimits(p.PayloadLimits) ||
		p.MaxRecordsPerCommit <= 0 || p.MaxRetainedRecords <= 0 ||
		p.MaxPayloadBytes == 0 || p.MaxDecodedWork == 0 ||
		p.SoftMaxRetainedRecords <= 0 || p.SoftMaxRetainedRecords > p.MaxRetainedRecords ||
		p.SoftMaxPayloadBytes == 0 || p.SoftMaxPayloadBytes > p.MaxPayloadBytes ||
		p.SoftMaxDecodedWork == 0 || p.SoftMaxDecodedWork > p.MaxDecodedWork {
		return ErrInvalidPolicy
	}
	return nil
}

// positiveLimits delegates allocation-free policy validation to the codec owner.
func positiveLimits(l codec.Limits) bool {
	_, err := codec.NewBudget(l)
	return err == nil
}
