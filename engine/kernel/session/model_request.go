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

// ModelRequest authors one invocation commitment in RunID. Source asserts the exact
// accepted construction head, never an earlier same-batch fact. RetryOf is optional
// same-run correlation only. Assignment shares nested bytes and pointer storage;
// Clone detaches them. Zero RunID is invalid; AddModelRequest allocates identity.
type ModelRequest struct {
	// RunID must identify the active run; AddModelRequest allocates its request ID.
	RunID RunID
	// Source is the exact accepted head used for request construction.
	Source SourceHead
	// Recipe describes construction; assignment borrows its nested bytes.
	Recipe ConstructionRecipe
	// RetryOf optionally names an earlier accepted request in RunID. It is
	// correlation only; assignment borrows the pointer and each add gets a new ID.
	RetryOf *ModelRequestID
}

// Validate checks local values and full retry scope, not accepted membership/order.
func (v ModelRequest) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return err
	}
	if err := v.Source.Validate(); err != nil {
		return err
	}
	if err := v.Recipe.Validate(); err != nil {
		return err
	}
	if v.RetryOf != nil && v.RetryOf.runID != v.RunID {
		return codec.ErrInvalidData
	}
	if v.RetryOf != nil {
		return v.RetryOf.Validate()
	}
	return nil
}

// Clone returns independently owned recipe bytes and retry storage.
func (v ModelRequest) Clone() ModelRequest {
	v.Recipe = v.Recipe.Clone()
	if v.RetryOf != nil {
		id := *v.RetryOf
		v.RetryOf = &id
	}
	return v
}

// Equal compares full run scope, source, recipe and retry presence.
func (v ModelRequest) Equal(other ModelRequest) bool {
	return v.RunID == other.RunID && v.Source == other.Source && v.Recipe.Equal(other.Recipe) && ((v.RetryOf == nil && other.RetryOf == nil) || (v.RetryOf != nil && other.RetryOf != nil && *v.RetryOf == *other.RetryOf))
}

// toFormat omits history scope only after Validate has checked retry scope and
// the constructor has checked the allocated identity against RunID.
func (v ModelRequest) toFormat(id ModelRequestID) sessionformat.ModelRequest {
	out := sessionformat.ModelRequest{RunID: v.RunID.ordinal, ID: id.requestOrdinal, Source: v.Source, Recipe: v.Recipe}
	if v.RetryOf != nil {
		out.RetryOf = new(v.RetryOf.requestOrdinal)
	}
	return out
}

func (v ModelRequest) checkSize(b *valueBudget) {
	b.text(v.Recipe.Algorithm)
	b.bytes(len(v.Recipe.Parameters))
	b.bytes(len(v.Recipe.Check))
}

// newModelRequestRecord owns one invocation commitment. It sizes before DTO
// conversion and checks full reference scope before omission.
// Failure returns nil; accepted source membership is checked by the aggregate.
func newModelRequestRecord(v ModelRequest, id ModelRequestID, limits codec.Limits) (*materializedRecord, error) {
	if err := id.Validate(); err != nil || id.runID != v.RunID {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	v.checkSize(&b)
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
	return decodeMaterialized(format.Record{Kind: sessionformat.KindModelRequest, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

func requestID(scope historyid.HistoryID, run, ordinal uint64) ModelRequestID {
	return ModelRequestID{runID: RunID{historyID: scope, ordinal: run}, requestOrdinal: ordinal}
}

// ConstructionRecipeView retains immutable construction backing for its lifetime.
// Zero returns empty scalars and zero Bytes. Reads are O(1); Parameters and Check
// borrow their leaves, while Copy explicitly detaches only the selected bytes.
type ConstructionRecipeView struct {
	value *sessionformat.ConstructionRecipe
}

// SourceHeadView retains an immutable accepted-history reference. Zero has zero
// sequence and CID. Reads are O(1); the backing remains retained for its lifetime.
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
