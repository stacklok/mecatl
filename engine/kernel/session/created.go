package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// Created is the canonical empty creation payload. History identity belongs to
// the enclosing envelope, not the payload; CreatedView exposes it on inspection.
type Created = sessionformat.Created

// newCreatedRecord encodes an owned creation record under explicit limits.
// Failure returns nil; construction does not establish first/unique creation.
func newCreatedRecord(v Created, limits codec.Limits) (*materializedRecord, error) {
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindCreated, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// CreatedView retains immutable creation backing and its enclosing history.
// Zero has zero identity; capture and inspection cost O(1).
type CreatedView struct {
	scope historyid.HistoryID
	value *sessionformat.Created
}

// HistoryID returns the enclosing history, or zero for a zero view.
func (v CreatedView) HistoryID() historyid.HistoryID {
	if v.value == nil {
		return historyid.HistoryID{}
	}
	return v.scope
}
