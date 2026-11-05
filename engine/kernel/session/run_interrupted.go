package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RunInterrupted is the public fact of explicit terminal
// interruption of the identified active run, with no success outcome, external
// result, cancellation guarantee, or automatic successor. Zero is invalid.
// It contains no mutable storage. Reconstruction must not invent this fact.
type RunInterrupted struct {
	// ID is the run being interrupted, not a new run occurrence.
	ID RunID
}

// Validate checks the run identity, not whether that run is currently active.
func (v RunInterrupted) Validate() error { return v.ID.Validate() }

// Clone returns v, which contains no mutable storage.
func (v RunInterrupted) Clone() RunInterrupted { return v }

// Equal reports whether the interruption facts identify the same run.
func (v RunInterrupted) Equal(other RunInterrupted) bool { return v == other }

// NewRunInterruptedRecord encodes an owned v1 interruption record using explicit
// payload limits. Failure returns zero. It does not cancel or close an active run.
func NewRunInterruptedRecord(v RunInterrupted, limits codec.Limits) (Record, error) {
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	payload, err := codec.Encode(sessionformat.RunInterrupted{ID: v.ID.Ordinal}, limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.ID.HistoryID, runInterrupted: v, value: format.Record{Kind: sessionformat.KindRunInterrupted, KindVersion: sessionformat.Version, Payload: payload}}, nil
}
