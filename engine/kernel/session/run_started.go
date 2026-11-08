package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
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
// Failure returns nil. Target history is checked before scope omission;
// construction neither accepts nor allocates a run.
func newRunStartedRecord(scope historyid.HistoryID, v RunStarted, limits codec.Limits) (*materializedRecord, error) {
	if err := v.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	if scope != v.ID.historyID {
		return nil, ErrIdentity
	}
	fact := sessionformat.RunStarted{ID: v.ID.ordinal}
	payload, err := codec.Encode(fact, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindRunStarted, KindVersion: sessionformat.Version, Payload: payload}, limits)
}
