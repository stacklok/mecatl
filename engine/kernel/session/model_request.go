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

// ModelRequest is one invocation commitment. Source is the exact accepted head;
// RetryOf is optional same-run correlation only. Assignment shares nested bytes and
// pointer storage; Clone detaches them. Zero ID is invalid.
type ModelRequest struct {
	ID      ModelRequestID
	Source  SourceHead
	Recipe  ConstructionRecipe
	RetryOf *ModelRequestID
}

// Validate checks local values and full retry scope, not accepted membership/order.
func (v ModelRequest) Validate() error {
	if err := v.ID.Validate(); err != nil {
		return err
	}
	if err := v.Source.Validate(); err != nil {
		return err
	}
	if err := v.Recipe.Validate(); err != nil {
		return err
	}
	if v.RetryOf != nil && v.RetryOf.RunID != v.ID.RunID {
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

// Equal compares exact scoped identity, source, recipe and retry presence.
func (v ModelRequest) Equal(other ModelRequest) bool {
	return v.ID == other.ID && v.Source == other.Source && v.Recipe.Equal(other.Recipe) && ((v.RetryOf == nil && other.RetryOf == nil) || (v.RetryOf != nil && other.RetryOf != nil && *v.RetryOf == *other.RetryOf))
}

// toFormat omits history scope only after Validate has checked retry scope.
func (v ModelRequest) toFormat() sessionformat.ModelRequest {
	out := sessionformat.ModelRequest{RunID: v.ID.RunID.Ordinal, Ordinal: v.ID.Ordinal, Source: v.Source.toFormat(), Recipe: v.Recipe.toFormat()}
	if v.RetryOf != nil {
		out.RetryOf = new(v.RetryOf.Ordinal)
	}
	return out
}

func (v ModelRequest) checkSize(b *valueBudget) {
	b.text(v.Recipe.Algorithm)
	b.bytes(len(v.Recipe.Parameters))
	b.bytes(len(v.Recipe.Check))
}

// NewModelRequestRecord owns one invocation commitment. It sizes before DTO
// conversion and checks full public scope before omitting enclosing identities.
// Failure returns zero; accepted source membership is checked by the aggregate.
func NewModelRequestRecord(v ModelRequest, limits codec.Limits) (Record, error) {
	b := newValueBudget(limits)
	v.checkSize(&b)
	if b.err != nil {
		return Record{}, b.err
	}
	if err := v.Validate(); err != nil {
		return Record{}, codec.ErrInvalidData
	}
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return Record{}, err
	}
	metadata := ModelRequest{ID: v.ID, Source: v.Source}
	if v.RetryOf != nil {
		metadata.RetryOf = new(*v.RetryOf)
	}
	return Record{scope: v.ID.RunID.HistoryID, modelRequest: metadata, value: format.Record{Kind: sessionformat.KindModelRequest, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

func requestID(scope historyid.HistoryID, run, ordinal uint64) ModelRequestID {
	return ModelRequestID{RunID: RunID{HistoryID: scope, Ordinal: run}, Ordinal: ordinal}
}
func requestFromFormat(scope historyid.HistoryID, v sessionformat.ModelRequest) ModelRequest {
	out := ModelRequest{ID: requestID(scope, v.RunID, v.Ordinal), Source: SourceHead{Sequence: v.Source.Sequence, CID: v.Source.CID}, Recipe: ConstructionRecipe{Algorithm: v.Recipe.Algorithm, Version: v.Recipe.Version, Parameters: v.Recipe.Parameters, Check: v.Recipe.Check}}
	if v.RetryOf != nil {
		id := requestID(scope, v.RunID, *v.RetryOf)
		out.RetryOf = &id
	}
	return out
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
	return requestID(v.scope, v.value.RunID, v.value.Ordinal)
}

// Source returns the exact pinned accepted source, or zero for zero.
func (v ModelRequestView) Source() SourceHead {
	if v.value == nil {
		return SourceHead{}
	}
	return SourceHead{Sequence: v.value.Source.Sequence, CID: v.value.Source.CID}
}

// Recipe returns detached construction bytes, or zero for zero.
func (v ModelRequestView) Recipe() ConstructionRecipe {
	if v.value == nil {
		return ConstructionRecipe{}
	}
	r := v.value.Recipe
	return ConstructionRecipe{Algorithm: r.Algorithm, Version: r.Version, Parameters: bytes.Clone(r.Parameters), Check: bytes.Clone(r.Check)}
}

// RetryOf returns optional same-run correlation, never a mutable pointer.
func (v ModelRequestView) RetryOf() (ModelRequestID, bool) {
	if v.value == nil || v.value.RetryOf == nil {
		return ModelRequestID{}, false
	}
	return requestID(v.scope, v.value.RunID, *v.value.RetryOf), true
}
