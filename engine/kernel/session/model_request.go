package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// SourceHead identifies an exact accepted history commit, not membership proof.
// Zero is invalid; enclosing history validation establishes its scope.
type SourceHead = sessionformat.SourceHead

// ConstructionRecipe commits to a construction algorithm without executing it.
// Assignment borrows its byte slices; Clone detaches them. Zero is invalid.
type ConstructionRecipe = sessionformat.ConstructionRecipe

// ModelRequest is the canonical invocation commitment. AddModelRequest supplies
// the destination and allocates identity; incoming RunID and ID must be zero.
// RetryOf is a destination-run-relative component without origin provenance.
// Assignment shares nested storage; Clone detaches it.
type ModelRequest = sessionformat.ModelRequest

func checkModelRequestSize(v ModelRequest, b *valueBudget) {
	b.text(v.Recipe.Algorithm)
	b.bytes(len(v.Recipe.Parameters))
	b.bytes(len(v.Recipe.Check))
}

// newModelRequestRecord sizes before filling the local copy and encoding.
// Destination and zero authoring fields have been checked by the caller.
func newModelRequestRecord(v ModelRequest, id ModelRequestID, limits codec.Limits) (*materializedRecord, error) {
	if err := id.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	checkModelRequestSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.ID = id.runID.ordinal, id.requestOrdinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindModelRequest, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

func requestID(scope historyid.HistoryID, run, ordinal uint64) ModelRequestID {
	return ModelRequestID{runID: RunID{historyID: scope, ordinal: run}, requestOrdinal: ordinal}
}

// ConstructionRecipeView retains immutable construction backing for its lifetime.
// Zero returns empty scalars and zero Bytes. Reads are O(1); Parameters and Check
// borrow their leaves, while Copy explicitly detaches only the selected bytes.
//
//dtogen:include
type ConstructionRecipeView struct {
	value *sessionformat.ConstructionRecipe
}

// SourceHeadView retains an immutable accepted-history reference. Zero has zero
// sequence and CID. Reads are O(1); the backing remains retained for its lifetime.
//
//dtogen:include
type SourceHeadView struct {
	value *sessionformat.SourceHead
}

// CID returns the content identity, or zero for a zero view.
func (v SourceHeadView) CID() contentid.CID {
	if v.value == nil {
		return contentid.CID{}
	}
	return v.value.CID
}

// ModelRequestView retains immutable accepted backing for its lifetime. Zero has
// zero identity and zero child views. Navigation is O(1); byte leaves borrow
// backing and Copy explicitly detaches only the selected leaf.
//
//dtogen:include
type ModelRequestView struct {
	scope historyid.HistoryID
	value *sessionformat.ModelRequest
}

// ID returns the full scoped identity, or zero for zero.
func (v ModelRequestView) ID() ModelRequestID {
	if v.value == nil {
		return ModelRequestID{}
	}
	return requestID(v.scope, v.value.RunID, v.value.ID)
}

// RetryOf returns optional same-run correlation, never a mutable pointer.
func (v ModelRequestView) RetryOf() (ModelRequestID, bool) {
	if v.value == nil || v.value.RetryOf == nil {
		return ModelRequestID{}, false
	}
	return requestID(v.scope, v.value.RunID, *v.value.RetryOf), true
}
