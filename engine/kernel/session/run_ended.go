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
type RunEndedReason = sessionformat.RunEndedReason

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

// CaptureFailureEvidence retains evidence for an accepted pending request whose
// response could not be captured. RequestID is destination-run-relative, without
// origin provenance. Assignment shares nested storage; Clone detaches it.
type CaptureFailureEvidence = sessionformat.CaptureFailureEvidence

// RunEnded is the canonical explicit terminal fact. AddRunEnded supplies the
// destination separately and requires RunID zero. Capture is allowed only for
// CaptureFailed. Assignment shares nested storage; Clone detaches it.
type RunEnded = sessionformat.RunEnded

func checkRunEndedSize(v RunEnded, b *valueBudget) {
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

// newRunEndedRecord sizes retained evidence before filling the local DTO copy.
// Destination and zero authoring fields have been checked by the caller.
func newRunEndedRecord(v RunEnded, run RunID, limits codec.Limits) (*materializedRecord, error) {
	b := newValueBudget(limits)
	checkRunEndedSize(v, &b)
	if b.err != nil {
		return nil, b.err
	}
	v.RunID = run.ordinal
	payload, err := codec.Encode(v, limits)
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
//
//dtogen:include
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

// RunEndedView is an immutable terminal fact. Zero has zero run, empty reason and
// no optional evidence. Scalar and pinned structured-child reads are O(1); byte
// views borrow retained backing and Copy detaches only their selected leaf.
//
//dtogen:include
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

// Capture returns a pinned capture-failure evidence view when present. It does not
// resolve the referenced request or certify remote failure.
func (v RunEndedView) Capture() (CaptureFailureEvidenceView, bool) {
	if v.value == nil || v.value.Capture == nil {
		return CaptureFailureEvidenceView{}, false
	}
	return CaptureFailureEvidenceView{runID: v.RunID(), value: v.value.Capture}, true
}
