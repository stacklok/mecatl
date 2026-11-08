package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RunEndedReason is a closed owner-selected terminal run category, encoded as a
// UTF-8 string of at most 32 bytes. Empty and unknown values are rejected.
// The taxonomy is provisional: revisit the combined interruption category only
// if usage establishes a useful distinction between cancellation and interruption.
// No reason proves remote cancellation or absence of processing or spend.
type RunEndedReason string

const (
	// RunEndedCompleted records clean completed termination.
	RunEndedCompleted RunEndedReason = sessionformat.RunEndedCompleted
	// RunEndedError records owner-selected error termination.
	RunEndedError RunEndedReason = sessionformat.RunEndedError
	// RunEndedLimit records owner-selected limit termination.
	RunEndedLimit RunEndedReason = sessionformat.RunEndedLimit
	// RunEndedInterrupted records explicit cancellation or execution interruption.
	// It does not assert remote cancellation or absence of processing or spend.
	RunEndedInterrupted RunEndedReason = sessionformat.RunEndedInterrupted
	// RunEndedCaptureFailed records inability to capture a complete response.
	RunEndedCaptureFailed RunEndedReason = sessionformat.RunEndedCaptureFailed
)

// CaptureFailureEvidence retains bounded same-invocation evidence for an accepted
// pending request whose complete response could not be captured. It has no response
// content or private envelope. Assignment borrows nested storage; Clone detaches it.
type CaptureFailureEvidence struct {
	RequestID ModelRequestID
	Evidence  *ObservedEvidence
	Usage     *InvocationUsage
}

// Validate checks local evidence shape and nested values without acceptance lookup.
func (v CaptureFailureEvidence) Validate() error {
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
func (v CaptureFailureEvidence) Clone() CaptureFailureEvidence {
	out := v
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

// Equal compares the complete scoped request reference and retained evidence.
func (v CaptureFailureEvidence) Equal(other CaptureFailureEvidence) bool {
	return v.RequestID == other.RequestID && v.toFormat().Equal(other.toFormat())
}
func (v CaptureFailureEvidence) toFormat() sessionformat.CaptureFailureEvidence {
	return sessionformat.CaptureFailureEvidence{RequestID: v.RequestID.requestOrdinal, Evidence: evidenceToFormat(v.Evidence), Usage: usageToFormat(v.Usage)}
}

// RunEnded is the unified explicit terminal fact. It neither fabricates an outcome
// nor promises cancellation. Capture is allowed only for CaptureFailed and must have
// the same full run scope. Assignment borrows nested storage; Clone detaches it.
type RunEnded struct {
	RunID  RunID
	Reason RunEndedReason
	// Explanation is optional safe human-readable ending detail. Callers must apply
	// their own disclosure rules; validation checks UTF-8 and structure only and is
	// not a sanitizer.
	Explanation *string
	Capture     *CaptureFailureEvidence
}

// Validate checks local ending values and full capture-request scope.
func (v RunEnded) Validate() error {
	if err := v.RunID.Validate(); err != nil {
		return err
	}
	if v.Capture != nil && v.Capture.RequestID.runID != v.RunID {
		return codec.ErrInvalidData
	}
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Capture != nil {
		return v.Capture.Validate()
	}
	return nil
}

// Clone detaches the optional explanation and capture evidence.
func (v RunEnded) Clone() RunEnded {
	out := v
	if v.Explanation != nil {
		s := *v.Explanation
		out.Explanation = &s
	}
	if v.Capture != nil {
		x := v.Capture.Clone()
		out.Capture = &x
	}
	return out
}

// Equal compares complete ending values, including the full scoped capture request.
func (v RunEnded) Equal(other RunEnded) bool {
	if v.RunID != other.RunID || v.Reason != other.Reason || (v.Explanation == nil) != (other.Explanation == nil) || (v.Capture == nil) != (other.Capture == nil) {
		return false
	}
	if v.Explanation != nil && *v.Explanation != *other.Explanation {
		return false
	}
	return v.Capture == nil || v.Capture.Equal(*other.Capture)
}
func (v RunEnded) toFormat() sessionformat.RunEnded {
	out := sessionformat.RunEnded{RunID: v.RunID.ordinal, Reason: string(v.Reason), Explanation: v.Explanation}
	if v.Capture != nil {
		x := v.Capture.toFormat()
		out.Capture = &x
	}
	return out
}
func (v RunEnded) checkSize(b *valueBudget) {
	b.text(string(v.Reason))
	if v.Explanation != nil {
		b.pointer()
		b.text(*v.Explanation)
	}
	if v.Capture != nil {
		b.pointer()
		checkEvidenceSize(v.Capture.Evidence, b)
		checkUsageSize(v.Capture.Usage, b)
	}
}

// newRunEndedRecord encodes a unified ending after sizing all retained evidence.
// Target history and full capture scope are checked before omission. Failure is nil.
func newRunEndedRecord(scope historyid.HistoryID, v RunEnded, limits codec.Limits) (*materializedRecord, error) {
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
	payload, err := codec.Encode(v.toFormat(), limits)
	if err != nil {
		return nil, err
	}
	return decodeMaterialized(format.Record{Kind: sessionformat.KindRunEnded, KindVersion: sessionformat.Version, Payload: payload}, limits)
}

// minimalEnding admits only an exact one-fact bounded-shape ending as cleanup.
func (r *materializedRecord) minimalEnding() bool {
	return r != nil && r.value.Kind == sessionformat.KindRunEnded && r.ended.Explanation == nil && r.ended.Capture == nil
}

// CaptureFailureEvidenceView is immutable bounded evidence for a failed capture.
// RequestID reconstructs the full history and run scope retained by its enclosing
// ending. Zero has a zero request and no optional children; child access is O(1).
type CaptureFailureEvidenceView struct {
	runID RunID
	value *sessionformat.CaptureFailureEvidence
}

// RequestID returns the full scoped captured request identity, or zero for zero.
func (v CaptureFailureEvidenceView) RequestID() ModelRequestID {
	if v.value == nil {
		return ModelRequestID{}
	}
	return requestID(v.runID.historyID, v.runID.ordinal, v.value.RequestID)
}

// Evidence returns pinned observed evidence when it was retained.
func (v CaptureFailureEvidenceView) Evidence() (ObservedEvidenceView, bool) {
	if v.value == nil || v.value.Evidence == nil {
		return ObservedEvidenceView{}, false
	}
	return ObservedEvidenceView{value: v.value.Evidence}, true
}

// Usage returns pinned reported usage when it was retained.
func (v CaptureFailureEvidenceView) Usage() (InvocationUsageView, bool) {
	if v.value == nil || v.value.Usage == nil {
		return InvocationUsageView{}, false
	}
	return InvocationUsageView{value: v.value.Usage}, true
}

// RunEndedView is an immutable terminal fact. Zero has zero run, empty reason and
// no optional evidence. Scalar and pinned structured-child reads are O(1); byte
// leaves returned by those children detach only their own storage.
type RunEndedView struct {
	scope historyid.HistoryID
	value *sessionformat.RunEnded
}

// RunID returns the full terminal run identity, or zero for zero.
func (v RunEndedView) RunID() RunID {
	if v.value == nil {
		return RunID{}
	}
	return RunID{historyID: v.scope, ordinal: v.value.RunID}
}

// Reason returns the owner-selected terminal category, or empty for zero.
func (v RunEndedView) Reason() RunEndedReason {
	if v.value == nil {
		return ""
	}
	return RunEndedReason(v.value.Reason)
}

// Explanation preserves absence versus explicitly empty safe detail.
func (v RunEndedView) Explanation() (string, bool) {
	if v.value == nil || v.value.Explanation == nil {
		return "", false
	}
	return *v.value.Explanation, true
}

// Capture returns a pinned capture-failure evidence view when present. It does not
// resolve the referenced request or certify remote failure.
func (v RunEndedView) Capture() (CaptureFailureEvidenceView, bool) {
	if v.value == nil || v.value.Capture == nil {
		return CaptureFailureEvidenceView{}, false
	}
	return CaptureFailureEvidenceView{runID: v.RunID(), value: v.value.Capture}, true
}
