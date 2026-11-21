package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// Created is the canonical empty creation payload. Session identity belongs to
// the enclosing root history envelope, not the payload; CreatedView exposes it
// on inspection.
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

// CreatedView retains immutable creation backing and its enclosing Session scope.
// Zero has zero identity; capture and inspection cost O(1).
//
//dtogen:include
type CreatedView struct {
	scope sessionid.SessionID
	value *sessionformat.Created
}

// SessionID returns the enclosing Session, or zero for a zero view.
func (v CreatedView) SessionID() sessionid.SessionID {
	if v.value == nil {
		return sessionid.SessionID{}
	}
	return v.scope
}
