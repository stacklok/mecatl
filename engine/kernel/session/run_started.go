package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// RunStarted is the canonical run-start payload. Its numeric ID is relative to
// the enclosing Session; RunStartedView reconstructs full identity on inspection.
type RunStarted = sessionformat.RunStarted

// newRunStartedRecord encodes an owned run-start record under explicit limits.
// Failure returns nil; construction neither accepts nor allocates a run.
func newRunStartedRecord(v RunStarted, limits codec.Limits) (*materializedRecord, error) {
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindRunStarted, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// RunStartedView retains immutable run-start backing and its enclosing Session scope.
// Zero has zero identity; capture and inspection cost O(1).
//
//dtogen:include
type RunStartedView struct {
	scope sessionid.SessionID
	value *sessionformat.RunStarted
}

// ID returns the full scoped run identity, or zero for a zero view.
func (v RunStartedView) ID() RunID {
	if v.value == nil {
		return RunID{}
	}
	return RunID{sessionID: v.scope, ordinal: v.value.ID}
}
