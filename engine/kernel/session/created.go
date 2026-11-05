package session

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Created is the public session creation fact. The session's identity is its
// history identity; no separate session resource is allocated. Zero is invalid.
// This value has no mutable storage and proves no history membership.
type Created struct {
	// HistoryID is retained privately by Records for preparation's scope check;
	// the creation persistence payload does not repeat it.
	HistoryID historyid.HistoryID
}

// Validate checks the history identity, not creation ordering or uniqueness.
func (v Created) Validate() error {
	if _, err := historyid.ParseHistoryID(v.HistoryID.String()); err != nil {
		return errors.New("invalid session history ID")
	}
	return nil
}

// Clone returns v, which contains no mutable storage.
func (v Created) Clone() Created { return v }

// Equal reports whether the creation facts have the same history identity.
func (v Created) Equal(other Created) bool { return v == other }

// NewCreatedRecord encodes an owned v1 creation record using explicit payload
// limits. Failure returns zero; it does not establish first/unique creation.
func NewCreatedRecord(v Created, limits codec.Limits) (Record, error) {
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	payload, err := codec.Encode(sessionformat.Created{}, limits)
	if err != nil {
		return Record{}, err
	}
	return Record{scope: v.HistoryID, value: format.Record{Kind: sessionformat.KindCreated, KindVersion: sessionformat.Version, Payload: payload}}, nil
}
