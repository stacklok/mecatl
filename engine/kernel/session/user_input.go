package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserInput is the canonical submission payload. AddUserInput takes its destination
// separately and requires RunID and ID zero. Validate checks the persisted shape.
// Assignment shares Parts; Clone detaches them.
type UserInput = sessionformat.UserInput

// newUserInputRecord sizes before populating the local copy and bounded encoding.
// The caller has validated destination and zero authoring fields; failure returns nil.
func newUserInputRecord(v UserInput, id UserInputID, limits codec.Limits) (*materializedRecord, error) {
	if err := id.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	checkPartsSize(v.Parts, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.ID = id.runID.ordinal, id.ordinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindUserInput, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// UserInputView retains immutable accepted backing storage, including its record.
// Copying or navigating a handle costs O(1). Zero has zero ID and no parts.
type UserInputView struct {
	scope historyid.HistoryID
	value *sessionformat.UserInput
}

// ID returns the scoped occurrence, or zero for zero.
func (v UserInputView) ID() UserInputID {
	if v.value == nil {
		return UserInputID{}
	}
	return UserInputID{runID: RunID{historyID: v.scope, ordinal: v.value.RunID}, ordinal: v.value.ID}
}
