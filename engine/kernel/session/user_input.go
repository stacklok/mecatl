package session

import (
	"bytes"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// UserInput is one public input fact owned by the active run. Zero is invalid.
// Assignment shares Content; Clone detaches it.
// The kernel does not interpret, normalize, or require UTF-8/JSON in Content.
type UserInput struct {
	// ID is the run-scoped occurrence, independent of content equality.
	ID InputID
	// Content is exact caller-approved input bytes. Nil and empty both mean
	// empty content; there is no separate absent-content state. The caller owns
	// its interpretation and must not supply credentials or unsafe config dumps.
	Content []byte
}

// Validate checks the occurrence identity without mutation or resource limits.
// It does not establish active-run acceptance.
func (v UserInput) Validate() error { return v.ID.Validate() }

// Clone returns an independently owned copy, preserving nil and empty storage.
func (v UserInput) Clone() UserInput {
	v.Content = bytes.Clone(v.Content)
	return v
}

// Equal compares identity and exact content; nil and empty content are equivalent.
func (v UserInput) Equal(other UserInput) bool {
	return v.ID == other.ID && bytes.Equal(v.Content, other.Content)
}

// NewUserInputRecord encodes an owned v1 input record using explicit payload
// limits. Failure returns zero. It neither accepts input nor allocates its ID.
func NewUserInputRecord(v UserInput, limits codec.Limits) (Record, error) {
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	payload, err := codec.Encode(sessionformat.UserInput{RunID: v.ID.RunID.Ordinal, ID: v.ID.Ordinal, Content: v.Content}, limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.ID.RunID.HistoryID, userInput: v.ID, inputBytes: len(v.Content), value: format.Record{Kind: sessionformat.KindUserInput, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// InputView is an immutable input handle. Zero has zero ID and nil content.
// Byte and DTO exports always detach storage; copying the handle costs O(1).
type InputView struct {
	scope  historyid.HistoryID
	record *acceptedRecord
}

// ID returns the run-scoped occurrence identity, or zero for zero.
func (v InputView) ID() InputID {
	if v.record == nil {
		return InputID{}
	}
	return InputID{RunID: RunID{HistoryID: v.scope, Ordinal: v.record.input.RunID}, Ordinal: v.record.input.ID}
}

// Content returns an owned copy of the exact opaque bytes, or nil for zero.
func (v InputView) Content() []byte {
	if v.record == nil {
		return nil
	}
	return bytes.Clone(v.record.input.Content)
}

// Format returns a detached input DTO, or zero for zero.
func (v InputView) Format() sessionformat.UserInput {
	if v.record == nil {
		return sessionformat.UserInput{}
	}
	return v.record.input.Clone()
}
