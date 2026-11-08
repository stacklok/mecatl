package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// InvocationFailureClass is a closed classification of locally observed invocation
// or delivery failure. Empty and unknown values are rejected.
type InvocationFailureClass string

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

// InvocationFailure records an observed failure for RequestID without fabricating a
// response or asserting no remote processing or charge. Assignment borrows nested
// storage; Clone detaches it. Explanation nil is absent and distinct from empty.
type InvocationFailure struct {
	RequestID   ModelRequestID
	Class       InvocationFailureClass
	Explanation *string
	Evidence    *ObservedEvidence
	Usage       *InvocationUsage
}

// Validate checks local values without proving acceptance or remote effects.
func (v InvocationFailure) Validate() error {
	if err := v.RequestID.Validate(); err != nil {
		return err
	}
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Evidence != nil {
		if err := v.Evidence.Validate(); err != nil {
			return err
		}
	}
	if v.Usage != nil {
		return v.Usage.Validate()
	}
	return nil
}

// Clone detaches all mutable nested storage.
func (v InvocationFailure) Clone() InvocationFailure {
	out := v
	if v.Explanation != nil {
		s := *v.Explanation
		out.Explanation = &s
	}
	if v.Evidence != nil {
		x := v.Evidence.Clone()
		out.Evidence = &x
	}
	if v.Usage != nil {
		x := v.Usage.Clone()
		out.Usage = &x
	}
	return out
}

// Equal compares exact scoped identity, optional fields, and retained evidence.
func (v InvocationFailure) Equal(other InvocationFailure) bool {
	return v.RequestID == other.RequestID && v.toFormat().Equal(other.toFormat())
}
func (v InvocationFailure) toFormat() sessionformat.InvocationFailure {
	return sessionformat.InvocationFailure{RunID: v.RequestID.runID.ordinal, RequestID: v.RequestID.requestOrdinal, Class: string(v.Class), Explanation: v.Explanation, Evidence: v.Evidence, Usage: v.Usage}
}
func (v InvocationFailure) checkSize(b *valueBudget) {
	b.text(string(v.Class))
	checkEvidenceSize(v.Evidence, b)
	checkUsageSize(v.Usage, b)
	if v.Explanation != nil {
		b.pointer()
		b.text(*v.Explanation)
	}
}

// newInvocationFailureRecord owns one observed terminal invocation failure.
// Sizing precedes conversion. Failure returns nil and never resolves a request by itself.
func newInvocationFailureRecord(v InvocationFailure, limits codec.Limits) (*materializedRecord, error) {
	if err := v.RequestID.Validate(); err != nil {
		return nil, codec.ErrInvalidData
	}
	b := newValueBudget(limits)
	v.checkSize(&b)
	if b.err != nil {
		return nil, b.err
	}
	payload, err := codec.Encode(v.toFormat(), limits)
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

// Class returns the observed failure classification, or empty for zero.
func (v InvocationFailureView) Class() InvocationFailureClass {
	if v.value == nil {
		return ""
	}
	return InvocationFailureClass(v.value.Class)
}

// Explanation preserves absent versus explicitly empty safe detail.
func (v InvocationFailureView) Explanation() (string, bool) {
	if v.value == nil || v.value.Explanation == nil {
		return "", false
	}
	return *v.value.Explanation, true
}

// Evidence returns a pinned immutable view of observed detail and correlation.
func (v InvocationFailureView) Evidence() (ObservedEvidenceView, bool) {
	if v.value == nil || v.value.Evidence == nil {
		return ObservedEvidenceView{}, false
	}
	return ObservedEvidenceView{value: v.value.Evidence}, true
}

// Usage returns a pinned immutable view of reported counters and raw usage-only evidence.
func (v InvocationFailureView) Usage() (InvocationUsageView, bool) {
	if v.value == nil || v.value.Usage == nil {
		return InvocationUsageView{}, false
	}
	return InvocationUsageView{value: v.value.Usage}, true
}
