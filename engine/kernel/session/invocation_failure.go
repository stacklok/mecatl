package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InvocationFailureClass is a closed classification of locally observed invocation
// or delivery failure. Empty and unknown values are rejected.
type InvocationFailureClass = sessionformat.InvocationFailureClass

const (
	// InvocationFailureTransport identifies an observed transport failure.
	InvocationFailureTransport InvocationFailureClass = sessionformat.InvocationFailureTransport
	// InvocationFailureTimeout identifies an observed timeout.
	InvocationFailureTimeout InvocationFailureClass = sessionformat.InvocationFailureTimeout
	// InvocationFailureLocal identifies a local invocation failure.
	InvocationFailureLocal InvocationFailureClass = sessionformat.InvocationFailureLocal
	// InvocationFailureOther identifies another observed failure.
	InvocationFailureOther InvocationFailureClass = sessionformat.InvocationFailureOther
)

// InvocationFailure is canonical observed failure evidence, not a fabricated
// response. AddInvocationFailure supplies the destination request separately;
// incoming RunID and RequestID must be zero. Clone detaches mutable storage.
type InvocationFailure = sessionformat.InvocationFailure

func checkInvocationFailureSize(v InvocationFailure, b *valueBudget) {
	b.text(string(v.Class))
	checkEvidenceSize(v.Evidence, b)
	checkUsageSize(v.Usage, b)
	if v.Explanation != nil {
		b.pointer()
		b.text(*v.Explanation)
	}
}

// newInvocationFailureRecord owns one observed terminal invocation failure.
// Sizing precedes bounded encoding. Failure returns nil; admission resolves membership.
func newInvocationFailureRecord(v InvocationFailure, request ModelRequestID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	checkInvocationFailureSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.RequestID = request.runID.ordinal, request.requestOrdinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindInvocationFailure, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// InvocationFailureView is an immutable observed outcome. Zero has zero request
// and empty class. Structured-child access is O(1); nested byte views borrow
// retained backing, and Copy detaches only their selected leaf.
type InvocationFailureView struct {
	scope historyid.HistoryID
	value *sessionformat.InvocationFailure
}

// RequestID returns the full accepted request reference, or zero for zero.
func (v InvocationFailureView) RequestID() ModelRequestID {
	if v.value == nil {
		return ModelRequestID{}
	}
	return requestID(v.scope, v.value.RunID, v.value.RequestID)
}
