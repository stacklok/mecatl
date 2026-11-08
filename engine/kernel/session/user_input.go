package session

import (
	"iter"
	"slices"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserInput authors one submission in an active run. Zero RunID is invalid.
// Empty Parts contributes no content; nil and empty are equivalent. Assignment
// shares storage; Clone detaches it. AddUserInput allocates submission identity.
type UserInput struct {
	// RunID must identify the active run at this position in the batch.
	RunID RunID
	// Parts is the ordered authored content; assignment borrows nested storage.
	Parts []ContentPart
}

// Validate checks full run scope and content, not active-run admission.
func (v UserInput) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return err
	}
	return validateParts(v.Parts)
}

// Clone returns independently owned parts.
func (v UserInput) Clone() UserInput {
	if v.Parts != nil {
		parts := make([]ContentPart, len(v.Parts))
		for i := range v.Parts {
			parts[i] = v.Parts[i].Clone()
		}
		v.Parts = parts
	}
	return v
}

// Equal compares run identity and ordered parts; nil and empty slices are equal.
func (v UserInput) Equal(other UserInput) bool {
	return v.RunID == other.RunID && slices.EqualFunc(v.Parts, other.Parts, ContentPart.Equal)
}

// toFormat receives the allocated local identity after full scope validation.
func (v UserInput) toFormat(id UserInputID) sessionformat.UserInput {
	return sessionformat.UserInput{RunID: v.RunID.ordinal, ID: id.ordinal, Parts: v.Parts}
}

// newUserInputRecord encodes an owned submission under explicit limits. Failure
// returns nil; allocated identity is checked before scope omission. Construction
// neither allocates identity nor establishes acceptance.
func newUserInputRecord(v UserInput, id UserInputID, limits codec.Limits) (*materializedRecord, error) {
	if err := v.RunID.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	if err := id.Validate(); err != nil || id.runID != v.RunID {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	checkPartsSize(v.Parts, &b)
	if b.err != nil {
		return nil, b.err
	}
	if err := v.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	payload, err := codec.Encode(v.toFormat(id), limits)
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

// PartCount returns the number of ordered parts, or zero for zero.
func (v UserInputView) PartCount() int {
	if v.value == nil {
		return 0
	}
	return len(v.value.Parts)
}

// Parts returns a repeatable sequence of immutable parts in authored order.
// Zero yields nothing. The sequence stays pinned to this view's accepted storage.
func (v UserInputView) Parts() iter.Seq[ContentPartView] {
	return func(yield func(ContentPartView) bool) {
		for i := 0; i < v.PartCount(); i++ {
			if !yield(v.PartAt(i)) {
				return
			}
		}
	}
}

// PartAt returns an immutable part. It panics outside [0, PartCount()).
func (v UserInputView) PartAt(i int) ContentPartView {
	return ContentPartView{value: &v.value.Parts[i]}
}
