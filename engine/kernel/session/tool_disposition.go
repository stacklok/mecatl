package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ToolReturnStatus is the closed tool-level return meaning, not rollback semantics.
type ToolReturnStatus = sessionformat.ToolReturnStatus

const (
	// ToolReturnSuccess reports tool-level success.
	ToolReturnSuccess = sessionformat.ToolReturnSuccess
	// ToolReturnError reports tool-level error, not absence of side effects.
	ToolReturnError = sessionformat.ToolReturnError
)

// ToolReturned retains observed terminal output; Clone detaches mutable storage.
type ToolReturned = sessionformat.ToolReturned

// ToolNotInvokedReason is the closed known non-invocation vocabulary.
type ToolNotInvokedReason = sessionformat.ToolNotInvokedReason

const (
	// ToolNotInvokedPolicyRejected reports policy rejection.
	ToolNotInvokedPolicyRejected = sessionformat.ToolNotInvokedPolicyRejected
	// ToolNotInvokedPreparationFailed reports failed preparation.
	ToolNotInvokedPreparationFailed = sessionformat.ToolNotInvokedPreparationFailed
	// ToolNotInvokedCancelled reports cancellation before invocation.
	ToolNotInvokedCancelled = sessionformat.ToolNotInvokedCancelled
	// ToolNotInvokedOther reports another known non-invocation reason.
	ToolNotInvokedOther = sessionformat.ToolNotInvokedOther
)

// ToolNotInvoked retains known non-invocation, before or after commitment.
type ToolNotInvoked = sessionformat.ToolNotInvoked

// ToolIndeterminateReason is the closed uncertain-outcome vocabulary.
type ToolIndeterminateReason = sessionformat.ToolIndeterminateReason

const (
	// ToolIndeterminateTransport reports transport uncertainty.
	ToolIndeterminateTransport = sessionformat.ToolIndeterminateTransport
	// ToolIndeterminateTimeout reports timeout uncertainty.
	ToolIndeterminateTimeout = sessionformat.ToolIndeterminateTimeout
	// ToolIndeterminateExecutorLost reports loss of the executor.
	ToolIndeterminateExecutorLost = sessionformat.ToolIndeterminateExecutorLost
	// ToolIndeterminateCancelled reports cancellation with uncertain outcome.
	ToolIndeterminateCancelled = sessionformat.ToolIndeterminateCancelled
	// ToolIndeterminateOther reports another uncertainty reason.
	ToolIndeterminateOther = sessionformat.ToolIndeterminateOther
)

// ToolIndeterminate retains uncertain outcome and incomplete output capture.
// Assignment shares output storage; Clone detaches it.
type ToolIndeterminate = sessionformat.ToolIndeterminate

// ToolDisposition retains exactly one passive requested-call outcome.
// AddToolDisposition takes the destination separately and requires RunID,
// RequestID and CallID zero. Assignment shares storage; Clone detaches it.
type ToolDisposition = sessionformat.ToolDisposition

// checkToolDispositionSize bounds all retained branches before encoding, including
// malformed unions; no output is cloned or JSON parsed by this sizing pass.
func checkToolDispositionSize(v ToolDisposition, b *valueBudget) {
	if v.Diagnostic != nil {
		b.pointer()
		b.text(*v.Diagnostic)
	}
	if v.Returned != nil {
		b.pointer()
		b.text(string(v.Returned.Status))
		checkToolOutputSize(v.Returned.Output, b)
	}
	if v.NotInvoked != nil {
		b.pointer()
		b.text(string(v.NotInvoked.Reason))
	}
	if v.Indeterminate != nil {
		b.pointer()
		b.text(string(v.Indeterminate.Reason))
		checkToolOutputSize(v.Indeterminate.Output, b)
	}
}

// newToolDispositionRecord owns a complete outcome, never a truncated substitute.
// Destination and zero DTO identities were checked by the caller. Bounded sizing
// and codec validation precede materialization; failure returns nil.
func newToolDispositionRecord(v ToolDisposition, call RequestedCallID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	checkToolDispositionSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID, v.RequestID, v.CallID = call.requestID.runID.ordinal, call.requestID.requestOrdinal, call.ordinal
	payload, err := codec.Encode(v, limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindToolDisposition, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// ToolDispositionView retains immutable complete outcome backing for its lifetime.
// Zero has no identity or branches. Navigation is O(1), borrowing descendants;
// byte Copy detaches only the selected leaf, never sibling output.
//
//dtogen:include
type ToolDispositionView struct {
	scope historyid.HistoryID
	value *sessionformat.ToolDisposition
}

// CallID returns the full scoped requested-call identity, or zero for zero.
func (v ToolDispositionView) CallID() RequestedCallID {
	if v.value == nil {
		return RequestedCallID{}
	}
	return RequestedCallID{requestID: requestID(v.scope, v.value.RunID, v.value.RequestID), ordinal: v.value.CallID}
}

// ToolReturnedView borrows immutable terminal output backing for its lifetime.
// Zero has empty status and output. All navigation is O(1) without sibling copies.
//
//dtogen:include
type ToolReturnedView struct{ value *sessionformat.ToolReturned }

// ToolNotInvokedView borrows immutable non-invocation backing for its lifetime.
// Zero has empty reason; reads are O(1).
//
//dtogen:include
type ToolNotInvokedView struct{ value *sessionformat.ToolNotInvoked }

// ToolIndeterminateView borrows immutable uncertain-outcome backing for its
// lifetime. Zero has empty reason and output; navigation is O(1).
//
//dtogen:include
type ToolIndeterminateView struct {
	value *sessionformat.ToolIndeterminate
}
