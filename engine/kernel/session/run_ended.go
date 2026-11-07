package session

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// RunEndedReason is a closed owner-selected terminal run category. All defined
// reasons use one-byte CBOR unsigned codes, keeping minimal ending shapes equal.
type RunEndedReason uint64

const (
	// RunEndedCompleted records clean completed termination.
	RunEndedCompleted RunEndedReason = sessionformat.RunEndedCompleted
	// RunEndedError records owner-selected error termination.
	RunEndedError RunEndedReason = sessionformat.RunEndedError
	// RunEndedLimit records owner-selected limit termination.
	RunEndedLimit RunEndedReason = sessionformat.RunEndedLimit
	// RunEndedCancelled records owner-selected cancellation termination.
	RunEndedCancelled RunEndedReason = sessionformat.RunEndedCancelled
	// RunEndedInterrupted records owner-selected interrupted termination.
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
	return sessionformat.CaptureFailureEvidence{RequestOrdinal: v.RequestID.Ordinal, Evidence: evidenceToFormat(v.Evidence), Usage: usageToFormat(v.Usage)}
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
	if v.Capture != nil && v.Capture.RequestID.RunID != v.RunID {
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
	out := sessionformat.RunEnded{RunID: v.RunID.Ordinal, Reason: uint64(v.Reason), Explanation: v.Explanation}
	if v.Capture != nil {
		x := v.Capture.toFormat()
		out.Capture = &x
	}
	return out
}
func (v RunEnded) checkSize(b *valueBudget) {
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

// NewRunEndedRecord encodes a unified ending after sizing all retained evidence.
// Invalid full capture scope is rejected before DTO scope omission. Failure is zero.
func NewRunEndedRecord(v RunEnded, limits codec.Limits) (Record, error) {
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
	metadata := RunEnded{RunID: v.RunID, Reason: v.Reason}
	// Presence, not the explanation bytes, determines minimal-ending eligibility.
	if v.Explanation != nil {
		metadata.Explanation = new("")
	}
	if v.Capture != nil {
		metadata.Capture = &CaptureFailureEvidence{RequestID: v.Capture.RequestID}
	}
	return Record{scope: v.RunID.HistoryID, runEnded: metadata, value: format.Record{Kind: sessionformat.KindRunEnded, KindVersion: sessionformat.Version, Payload: payload}}, nil
}

// minimalEnding admits only an exact one-fact fixed-shape ending as cleanup.
func (r Record) minimalEnding() bool {
	return r.value.Kind == sessionformat.KindRunEnded && r.runEnded.Explanation == nil && r.runEnded.Capture == nil
}

func endedFromFormat(scope historyid.HistoryID, v sessionformat.RunEnded) RunEnded {
	run := RunID{HistoryID: scope, Ordinal: v.RunID}
	out := RunEnded{RunID: run, Reason: RunEndedReason(v.Reason), Explanation: v.Explanation}
	if v.Capture != nil {
		out.Capture = &CaptureFailureEvidence{RequestID: ModelRequestID{RunID: run, Ordinal: v.Capture.RequestOrdinal}}
	}
	return out
}

// RunEndedView is an immutable terminal fact. Zero has zero run/reason and no
// optional evidence. Scalar reads are O(1); evidence exports detach storage.
type RunEndedView struct {
	scope historyid.HistoryID
	value *sessionformat.RunEnded
}

// RunID returns the full terminal run identity, or zero for zero.
func (v RunEndedView) RunID() RunID {
	if v.value == nil {
		return RunID{}
	}
	return RunID{HistoryID: v.scope, Ordinal: v.value.RunID}
}

// Reason returns the owner-selected terminal category, or zero for zero.
func (v RunEndedView) Reason() RunEndedReason {
	if v.value == nil {
		return 0
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

// Capture returns detached capture-failure evidence. It does not resolve the
// referenced request or certify remote failure.
func (v RunEndedView) Capture() (CaptureFailureEvidence, bool) {
	if v.value == nil || v.value.Capture == nil {
		return CaptureFailureEvidence{}, false
	}
	c := v.value.Capture
	return (CaptureFailureEvidence{RequestID: ModelRequestID{RunID: v.RunID(), Ordinal: c.RequestOrdinal}, Evidence: evidenceFromFormat(c.Evidence), Usage: usageFromFormat(c.Usage)}).Clone(), true
}
