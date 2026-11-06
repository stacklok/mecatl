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
	// ID is the required run-scoped submission identity.
	ID UserInputID
	// Parts is the ordered authored content; assignment borrows nested storage.
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
	return UserInputID{RunID: RunID{HistoryID: v.scope, Ordinal: v.value.RunID}, Ordinal: v.value.ID}
}

// PartCount returns the number of ordered parts, or zero for zero.
func (v UserInputView) PartCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Parts)
}

// PartAt returns an immutable part. It panics outside [0, PartCount()).
func (v UserInputView) PartAt(i int) ContentPartView {
	return ContentPartView{value: &v.value.Parts[i]}
}
