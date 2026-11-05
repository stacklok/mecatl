package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserInput is an ordered authored input owned by an active run. Zero ID is
// invalid. Nil and empty Messages mean explicitly empty input; one empty message
// is distinct. Assignment shares storage; Clone deeply detaches it.
type UserInput struct {
	ID       InputID
	Messages []AuthoredMessage
}

// Validate checks full identity and content, not membership or active-run admission.
func (v UserInput) Validate() error {
	if err := v.ID.Validate(); err != nil {
		return err
	}
	for _, m := range v.Messages {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns independently owned messages.
func (v UserInput) Clone() UserInput {
	v.Messages = messagesFromFormat(v.toFormat().Clone().Messages)
	return v
}

// Equal compares identity and ordered messages; nil and empty slices are equal.
func (v UserInput) Equal(other UserInput) bool {
	return v.ID == other.ID && v.toFormat().Equal(other.toFormat())
}
func (v UserInput) toFormat() sessionformat.UserInput {
	return sessionformat.UserInput{RunID: v.ID.RunID.Ordinal, ID: v.ID.Ordinal, Messages: messagesToFormat(v.Messages)}
}

// NewUserInputRecord encodes an owned v2 rich input under explicit limits. Failure
// returns zero; construction does not allocate identity or establish acceptance.
func NewUserInputRecord(v UserInput, limits codec.Limits) (Record, error) {
	if err := v.ID.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	b.collection(len(v.Messages))
	if b.err == nil {
		for _, m := range v.Messages {
			m.Content.checkSize(&b)
		}
	}
	if b.err != nil {
		return Record{}, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.ID.RunID.HistoryID, userInput: v.ID, messageCount: uint64(len(v.Messages)), value: format.Record{Kind: sessionformat.KindUserInput, KindVersion: sessionformat.UserInputVersion, Payload: payload}}, nil
}

// InputView is an immutable input handle. Zero has zero ID and nil messages.
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

// Messages returns deeply detached authored messages, or nil for zero.
func (v InputView) Messages() []AuthoredMessage {
	if v.record == nil {
		return nil
	}
	return messagesFromFormat(v.record.input.Clone().Messages)
}

// Format returns a detached DTO, or zero for zero.
func (v InputView) Format() sessionformat.UserInput {
	if v.record == nil {
		return sessionformat.UserInput{}
	}
	return v.record.input.Clone()
}
