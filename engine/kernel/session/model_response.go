package session

import (
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ResponseContentKind selects one canonical content meaning. Empty and unknown
// values are rejected.
type ResponseContentKind = sessionformat.ResponseContentKind

const (
	// ResponseContentOutput retains ordinary model output.
	ResponseContentOutput ResponseContentKind = sessionformat.ResponseContentOutput
	// ResponseContentReasoning retains readable model reasoning.
	ResponseContentReasoning ResponseContentKind = sessionformat.ResponseContentReasoning
	// ResponseContentRefusal retains a model refusal.
	ResponseContentRefusal ResponseContentKind = sessionformat.ResponseContentRefusal
)

// ResponseTerminalClass is the closed normalized terminal provider class. Empty
// and unknown values are rejected, unlike the provider-owned stop reason.
type ResponseTerminalClass = sessionformat.ResponseTerminalClass

const (
	// ResponseTerminalNormal identifies a normal terminal response.
	ResponseTerminalNormal ResponseTerminalClass = sessionformat.ResponseTerminalNormal
	// ResponseTerminalLimit identifies a provider limit terminal response.
	ResponseTerminalLimit ResponseTerminalClass = sessionformat.ResponseTerminalLimit
	// ResponseTerminalRefusal identifies a provider refusal terminal response.
	ResponseTerminalRefusal ResponseTerminalClass = sessionformat.ResponseTerminalRefusal
	// ResponseTerminalError identifies a provider error terminal response.
	ResponseTerminalError ResponseTerminalClass = sessionformat.ResponseTerminalError
	// ResponseTerminalCancelled identifies a provider cancellation terminal response.
	ResponseTerminalCancelled ResponseTerminalClass = sessionformat.ResponseTerminalCancelled
	// ResponseTerminalOther identifies another terminal response.
	ResponseTerminalOther ResponseTerminalClass = sessionformat.ResponseTerminalOther
)

// RequestedCall retains an exact call name and opaque arguments, not necessarily
// JSON. Assignment borrows arguments; Clone detaches them.
type RequestedCall = sessionformat.RequestedCall

// ResponseContent retains ordered output, reasoning, or refusal parts, including
// empty content. Assignment borrows parts; Clone detaches them.
type ResponseContent = sessionformat.ResponseContent

// ResponseItem preserves response order with exactly one content or call branch.
// Assignment borrows its child; Clone detaches it.
type ResponseItem = sessionformat.ResponseItem

// ModelResponse is the canonical atomic terminal response. AddModelResponse takes
// the full destination request separately and requires RunID and RequestID zero.
// Assignment shares nested storage; Clone detaches it. Optional presence is retained.
type ModelResponse = sessionformat.ModelResponse

func checkModelResponseSize(v ModelResponse, b *valueBudget) {
	b.text(string(v.Terminal))
	checkEvidenceSize(v.Evidence, b)
	checkUsageSize(v.Usage, b)
	checkOpaqueSize(v.Private, b)
	if v.ProviderStopReason != nil {
		b.pointer()
		b.text(*v.ProviderStopReason)
	}
	b.collection(len(v.Items))
	if b.err != nil {
		return
	}
	for _, item := range v.Items {
		if item.Content != nil {
			b.pointer()
			b.text(string(item.Content.Kind))
			checkPartsSize(item.Content.Parts, b)
		}
		if item.Call != nil {
			b.pointer()
			b.text(item.Call.Name)
			b.bytes(len(item.Call.Arguments))
		}
	}
}

// newModelResponseRecord owns a complete response, never a truncated subset.
// Sizing precedes bounded encoding and materialization. Failure returns nil.
func newModelResponseRecord(v ModelResponse, request ModelRequestID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	checkModelResponseSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.RequestID = request.runID.ordinal, request.requestOrdinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindModelResponse, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ModelResponseView retains immutable complete-response backing for its lifetime.
// Zero has no identity or items. Item navigation and structured-child access are
// O(1); call lookup scans response order. Byte leaves borrow backing; Copy
// explicitly detaches only the selected bytes.
type ModelResponseView struct {
	scope historyid.HistoryID
	value *sessionformat.ModelResponse
}

// RequestID returns the full accepted request reference, or zero for zero.
func (v ModelResponseView) RequestID() ModelRequestID {
	if v.value == nil {
		return ModelRequestID{}
	}
	return requestID(v.scope, v.value.RunID, v.value.RequestID)
}

// CallCount derives the number of complete calls, independent of content items.
// It scans the response; no redundant call identities are stored in DTOs.
func (v ModelResponseView) CallCount() int {
	n := 0
	for i := 0; i < v.Items().Len(); i++ {
		if v.value.Items[i].Call != nil {
			n++
		}
	}
	return n
}

// CallAt returns the ith call with its derived contiguous call identity. It scans
// response order and panics outside [0, CallCount()).
func (v ModelResponseView) CallAt(i int) RequestedCallView {
	if i >= 0 {
		n := 0
		for j := 0; j < v.Items().Len(); j++ {
			if c := v.value.Items[j].Call; c != nil {
				if n == i {
					return RequestedCallView{id: RequestedCallID{requestID: v.RequestID(), ordinal: uint64(n) + 1}, value: c}
				}
				n++
			}
		}
	}
	panic("call index out of range")
}

// Calls yields complete calls with IDs derived from call order, not item order.
func (v ModelResponseView) Calls() iter.Seq[RequestedCallView] {
	return func(yield func(RequestedCallView) bool) {
		var ordinal uint64
		for i := 0; i < v.Items().Len(); i++ {
			if c := v.value.Items[i].Call; c != nil {
				ordinal++
				if !yield(RequestedCallView{id: RequestedCallID{requestID: v.RequestID(), ordinal: ordinal}, value: c}) {
					return
				}
			}
		}
	}
}

// RequestedCallDataView retains immutable identity-free call backing for its
// lifetime. Zero returns an empty name and zero Bytes. Reads are O(1); Arguments
// borrows its leaf and Copy explicitly detaches it. Occurrence identity is
// deliberately available only through ModelResponseView.Calls and CallAt.
type RequestedCallDataView struct {
	value *sessionformat.RequestedCall
}

// ResponseItemView is a pinned union handle. Zero has neither branch. Branch
// navigation is O(1); Items and Calls traverse their ordered DTOs linearly.
type ResponseItemView struct{ value *sessionformat.ResponseItem }

// ResponseContentView retains immutable content and ordered parts for its lifetime.
// Zero is empty; Parts borrows backing and adapts elements on demand in O(1).
type ResponseContentView struct {
	value *sessionformat.ResponseContent
}

// RequestedCallView retains immutable complete-call backing for its lifetime.
// Zero has zero ID, empty name and zero Bytes. Reads are O(1); Arguments borrows
// its leaf and Copy explicitly detaches it.
type RequestedCallView struct {
	id    RequestedCallID
	value *sessionformat.RequestedCall
}

// ID returns the scoped call identity, derived from accepted call order.
func (v RequestedCallView) ID() RequestedCallID { return v.id }
