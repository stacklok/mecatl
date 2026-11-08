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
	// HistoryID is the enclosing history identity, omitted from the creation
	// persistence payload.
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

// newCreatedRecord encodes an owned v1 creation record using explicit payload
// limits. Target history is checked before scope omission. Failure returns nil;
// construction does not establish first/unique creation.
func newCreatedRecord(scope historyid.HistoryID, v Created, limits codec.Limits) (*materializedRecord, error) {
	if err := v.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	if scope != v.HistoryID {
		return nil, ErrIdentity
	}
	payload, err := codec.Encode(sessionformat.Created{}, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindCreated, KindVersion: sessionformat.Version, Payload: payload}, limits)
}
