package session

import (
	"bytes"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// SourceHead identifies the exact accepted commit before a request. It is a
// history-local reference, not a proposed commit or membership proof. Zero is invalid.
type SourceHead struct {
	// Sequence is the positive accepted commit sequence.
	Sequence int64
	// CID is the exact accepted commit content identity.
	CID contentid.CID
}

// Validate checks reference shape without history lookup.
func (v SourceHead) Validate() error { return v.toFormat().Validate() }

// Clone returns v, which owns its fixed-size CID storage.
func (v SourceHead) Clone() SourceHead { return v }

// Equal reports exact source-head identity.
func (v SourceHead) Equal(other SourceHead) bool { return v == other }
func (v SourceHead) toFormat() sessionformat.SourceHead {
	return sessionformat.SourceHead{Sequence: v.Sequence, CID: v.CID}
}

// ConstructionRecipe commits to a construction algorithm. Parameters may be empty;
// Check is required exact algorithm-owned bytes. Assignment borrows byte slices;
// Clone detaches them. Zero is invalid.
type ConstructionRecipe struct {
	Algorithm  string
	Version    int64
	Parameters []byte
	Check      []byte
}

// Validate checks local structure only; it neither selects nor executes an algorithm.
func (v ConstructionRecipe) Validate() error { return v.toFormat().Validate() }

// Clone detaches exact parameter and check bytes.
func (v ConstructionRecipe) Clone() ConstructionRecipe {
	v.Parameters = bytes.Clone(v.Parameters)
	v.Check = bytes.Clone(v.Check)
	return v
}

// Equal compares exact identifiers and bytes; nil and empty byte slices are equal.
func (v ConstructionRecipe) Equal(other ConstructionRecipe) bool {
	return v.toFormat().Equal(other.toFormat())
}
func (v ConstructionRecipe) toFormat() sessionformat.ConstructionRecipe {
	return sessionformat.ConstructionRecipe{Algorithm: v.Algorithm, Version: v.Version, Parameters: v.Parameters, Check: v.Check}
}

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
	out := sessionformat.ModelRequest{RunID: v.RunID.ordinal, ID: id.requestOrdinal, Source: v.Source.toFormat(), Recipe: v.Recipe.toFormat()}
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
// conversion and checks target history and full public scope before omission.
// Failure returns nil; accepted source membership is checked by the aggregate.
func newModelRequestRecord(scope historyid.HistoryID, v ModelRequest, id ModelRequestID, limits codec.Limits) (*materializedRecord, error) {
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
	if scope != v.RunID.historyID {
		return nil, ErrIdentity
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

// ConstructionRecipeView is an immutable construction recipe. Zero returns empty
// scalars and nil byte leaves. Scalar reads are O(1); Parameters and Check each
// detach only their own bytes.
type ConstructionRecipeView struct {
	value *sessionformat.ConstructionRecipe
}

// Algorithm returns the construction algorithm identifier, or empty for zero.
func (v ConstructionRecipeView) Algorithm() string {
	if v.value == nil {
		return ""
	}
	return v.value.Algorithm
}

// Version returns the construction algorithm revision, or zero for zero.
func (v ConstructionRecipeView) Version() int64 {
	if v.value == nil {
		return 0
	}
	return v.value.Version
}

// Parameters returns detached algorithm-owned parameter bytes. Nil and empty remain
// equivalent.
func (v ConstructionRecipeView) Parameters() []byte {
	if v.value == nil {
		return nil
	}
	return bytes.Clone(v.value.Parameters)
}

// Check returns detached required algorithm-owned check bytes.
func (v ConstructionRecipeView) Check() []byte {
	if v.value == nil {
		return nil
	}
	return bytes.Clone(v.value.Check)
}

// ModelRequestView is an immutable accepted commitment. Zero has zero identity;
// byte-bearing exports detach storage, while scalar access is constant-time.
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

// Source returns the exact pinned accepted source, or zero for zero.
func (v ModelRequestView) Source() SourceHead {
	if v.value == nil {
		return SourceHead{}
	}
	return SourceHead{Sequence: v.value.Source.Sequence, CID: v.value.Source.CID}
}

// Recipe returns a pinned immutable construction recipe. It returns a zero child
// view for a zero request.
func (v ModelRequestView) Recipe() ConstructionRecipeView {
	if v.value == nil {
		return ConstructionRecipeView{}
	}
	return ConstructionRecipeView{value: &v.value.Recipe}
}

// RetryOf returns optional same-run correlation, never a mutable pointer.
func (v ModelRequestView) RetryOf() (ModelRequestID, bool) {
	if v.value == nil || v.value.RetryOf == nil {
		return ModelRequestID{}, false
	}
	return requestID(v.scope, v.value.RunID, *v.value.RetryOf), true
}
