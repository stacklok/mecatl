package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// ToolInvocation retains effective committed intent, not dispatch or execution
// authority. AddToolInvocation supplies the destination separately and requires
// RunID, RequestID and CallID zero. Assignment shares storage; Clone detaches it.
type ToolInvocation = sessionformat.ToolInvocation

// checkToolInvocationSize bounds immediate authoring storage. The generated
// codec checks structured arguments with the complete payload's shared budget
// before allocating encoded bytes or detaching any mutable descendants.
func checkToolInvocationSize(v ToolInvocation, b *valueBudget) {
	b.text(v.Name)
	b.collection(len(v.Prerequisites))
}

// newToolInvocationRecord materializes a complete privately owned fact after
// bounded validation. The caller has checked destination and zero DTO identities.
// Failure returns nil and does not mutate caller storage.
func newToolInvocationRecord(v ToolInvocation, call RequestedCallID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	checkToolInvocationSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.RequestID, v.CallID = call.requestID.runID.ordinal, call.requestID.requestOrdinal, call.ordinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindToolInvocation, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ToolInvocationView retains immutable complete intent backing for its lifetime.
// Zero has no identity or prerequisites and empty arguments. Navigation takes
// O(1), borrowing structured arguments without copying siblings.
//
//dtogen:include
type ToolInvocationView struct {
	scope historyid.HistoryID
	value *sessionformat.ToolInvocation
}

// CallID returns the full scoped requested-call identity, or zero for zero.
func (v ToolInvocationView) CallID() RequestedCallID {
	if v.value == nil {
		return RequestedCallID{}
	}
	return RequestedCallID{requestID: requestID(v.scope, v.value.RunID, v.value.RequestID), ordinal: v.value.CallID}
}

// Prerequisites borrows the ordered components and adapts full same-response
// identities in O(1) per element. Zero is empty. The retained adapter captures
// request scope, may allocate when escaping, and never copies argument payloads.
func (v ToolInvocationView) Prerequisites() views.SliceView[uint64, RequestedCallID] {
	if v.value == nil {
		return views.SliceView[uint64, RequestedCallID]{}
	}
	request := v.CallID().RequestID()
	return views.BorrowSliceView(v.value.Prerequisites, func(component *uint64) RequestedCallID {
		return RequestedCallID{requestID: request, ordinal: *component}
	})
}
