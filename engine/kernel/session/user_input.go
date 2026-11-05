package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserInput is one authored submission owned by an active run. Zero ID is invalid.
// Empty Parts contributes no content; nil and empty are equivalent. Assignment
// shares storage; Clone detaches it.
type UserInput struct {
	ID    InputID
	Parts []ContentPart
}

// Validate checks full identity and content, not active-run admission.
func (v UserInput) Validate() error {
	if err := v.ID.Validate(); err != nil {
		return err
	}
	return validateParts(v.Parts)
}

// Clone returns independently owned parts.
func (v UserInput) Clone() UserInput {
	v.Parts = partsFromFormat(v.toFormat().Clone().Parts)
	return v
}

// Equal compares identity and ordered parts; nil and empty slices are equal.
func (v UserInput) Equal(other UserInput) bool {
	return v.ID == other.ID && v.toFormat().Equal(other.toFormat())
}
func (v UserInput) toFormat() sessionformat.UserInput {
	return sessionformat.UserInput{RunID: v.ID.RunID.Ordinal, ID: v.ID.Ordinal, Parts: partsToFormat(v.Parts)}
}

// NewUserInputRecord encodes an owned submission under explicit limits. Failure
// returns zero; construction neither allocates identity nor establishes acceptance.
func NewUserInputRecord(v UserInput, limits codec.Limits) (Record, error) {
	if err := v.ID.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	checkPartsSize(v.Parts, &b)
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.ID.RunID.HistoryID, userInput: v.ID, value: format.Record{Kind: sessionformat.KindUserInput, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// InputView is an immutable input handle. Zero has zero ID and nil parts.
// Exports detach mutable storage; copying the handle costs O(1).
type InputView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// ID returns the scoped occurrence, or zero for zero.
func (v InputView) ID() InputID {
	if v.record == nil {
		return InputID{}
	}
	return InputID{RunID: RunID{HistoryID: v.scope, Ordinal: v.record.input.RunID}, Ordinal: v.record.input.ID}
}

// Parts returns deeply detached content, or nil for zero.
func (v InputView) Parts() []ContentPart {
	if v.record == nil {
		return nil
	}
	return partsFromFormat(v.record.input.Clone().Parts)
}

// Format returns a detached DTO, or zero for zero.
func (v InputView) Format() sessionformat.UserInput {
	if v.record == nil {
		return sessionformat.UserInput{}
	}
	return v.record.input.Clone()
}
