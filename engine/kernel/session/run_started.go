package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RunStarted is a run-start fact, with no requirement for input or authority to
// execute. It contains no mutable storage. Zero is invalid.
type RunStarted struct {
	// ID is the new history-relative run occurrence.
	ID RunID
}

// Validate checks identity, not accepted-state lifecycle legality.
func (v RunStarted) Validate() error { return v.ID.Validate() }

// Clone returns v, which contains no mutable storage.
func (v RunStarted) Clone() RunStarted { return v }

// Equal compares run identities.
func (v RunStarted) Equal(other RunStarted) bool { return v == other }

// newRunStartedRecord encodes an owned v1 run-start record using explicit limits.
// Failure returns zero. It neither accepts nor allocates a run.
func newRunStartedRecord(v RunStarted, limits codec.Limits) (proposedRecord, error) {
	if err := v.Validate(); err != nil {
		return proposedRecord{}, codec.ErrInvalidData
	}
	fact := sessionformat.RunStarted{ID: v.ID.ordinal}
	payload, err := codec.Encode(fact, limits)
	if err != nil {
		return proposedRecord{}, err
	}
	return decodeRecord(v.ID.historyID, format.Record{Kind: sessionformat.KindRunStarted, KindVersion: sessionformat.Version, Payload: payload}, limits)
}
