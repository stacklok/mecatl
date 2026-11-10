package format

import (
	"errors"
	"unicode/utf8"
)

// RunEndedReason is a closed owner-selected terminal category, at most 32 bytes.
// No reason proves remote cancellation or absence of processing or spend.
type RunEndedReason string

// RunEnded.Reason values form a closed vocabulary; empty and unknown labels are
// rejected along with labels exceeding MaxRunEndedReasonBytes.
const (
	// RunEndedCompleted encodes clean completed termination.
	RunEndedCompleted RunEndedReason = "completed"
	// RunEndedError encodes owner-selected error termination.
	RunEndedError RunEndedReason = "error"
	// RunEndedLimit encodes owner-selected limit termination.
	RunEndedLimit RunEndedReason = "limit"
	// RunEndedInterrupted encodes explicit cancellation or execution interruption.
	// It does not prove remote cancellation or absence of processing or spend.
	RunEndedInterrupted RunEndedReason = "interrupted"
	// RunEndedCaptureFailed encodes inability to retain an admissible model response
	// or tool outcome, including a single oversized result. Truncation or omission
	// alone is not capture failure, and a tool-level error is a normal return.
	RunEndedCaptureFailed RunEndedReason = "capture-failed"
)

// MaxRunEndedReasonBytes bounds the UTF-8 byte length of every ending label.
// Closed-vocabulary validation also enforces this bound on future additions.
const MaxRunEndedReasonBytes = 32

// CaptureFailureEvidence is ending-owned evidence for an accepted pending request
// whose complete response could not be captured. It has no response body or private
// envelope. Assignment shares nested storage; Clone detaches it. RequestID is
// interpreted within the explicit destination run without origin provenance.
//
//dtogen:validate validateLocal
//dtogen:include
type CaptureFailureEvidence struct {
	// RequestID is the required positive numeric request component within the
	// enclosing RunEnded's run. Its history envelope supplies history scope.
	// Local validation checks positivity, not pending request membership.
	RequestID uint64 `cbor:"q" view:"manual"`
	// Evidence is optional diagnostic observation for the uncaptured response. Nil
	// omits it; codec traversal validates only its local UTF-8 shape.
	Evidence *ObservedEvidence `cbor:"e,omitempty"`
	// Usage is optional provider-reported usage for the uncaptured response. Nil
	// omits it; codec traversal validates local counters and opaque metadata.
	Usage *InvocationUsage `cbor:"u,omitempty"`
}

// Validate checks the relative request identity and all retained evidence.
func (v CaptureFailureEvidence) Validate() error {
	if err := v.validateLocal(); err != nil {
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

// validateLocal checks identity after bounded codec traversal validates children.
func (v CaptureFailureEvidence) validateLocal() error {
	if v.RequestID == 0 {
		return errors.New("capture failure evidence requires a positive request ordinal")
	}
	return nil
}

// RunEnded terminally records the owner's ending reason without fabricating any
// response, cancellation, or external effect. Evidence is permitted only for a
// capture-failed ending. Assignment shares nested storage; Clone detaches it.
// Session authoring supplies destination scope separately and requires RunID zero;
// Validate requires the fully populated persisted shape.
//
//dtogen:validate validateLocal
//dtogen:include
type RunEnded struct {
	// RunID is the required positive numeric run component within the history
	// supplied by the enclosing envelope. The aggregate checks active ownership.
	RunID uint64 `cbor:"r" view:"manual"`
	// Reason is required and uses the closed RunEnded vocabulary; empty and
	// unknown labels are rejected. Labels must be valid UTF-8, at most
	// MaxRunEndedReasonBytes bytes. Earlier experimental numeric codes reject.
	// The taxonomy is provisional: revisit the combined interruption category
	// only if usage establishes a useful cancellation/interruption distinction.
	Reason RunEndedReason `cbor:"e"`
	// BulkToolCloseout is optional and defaults to false when omitted. False is
	// omitted on encoding and means ordinary termination; aggregate admission
	// checks that every tool call is settled. True explicitly closes outstanding
	// tool calls and is valid only for limit or capture-failed endings. It does not
	// fabricate model outcomes or prove stopped external effects.
	BulkToolCloseout bool `cbor:"b,omitempty"`
	// Explanation is optional UTF-8 owner-provided human-readable ending detail.
	// Nil omits it; non-nil empty is retained. Validation is not disclosure sanitization.
	Explanation *string `cbor:"x,omitempty"`
	// Capture is optional uncaptured-response evidence. Nil omits it; it is allowed
	// only when Reason is RunEndedCaptureFailed, while aggregate admission checks
	// that its request is accepted and pending in RunID.
	Capture *CaptureFailureEvidence `cbor:"c,omitempty" view:"manual"`
}

// Validate checks persisted identity, closed reason and optional capture evidence.
func (v RunEnded) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Capture != nil {
		return v.Capture.Validate()
	}
	return nil
}

// validateLocal checks immediate fields after bounded traversal validates children.
func (v RunEnded) validateLocal() error {
	if v.RunID == 0 {
		return errors.New("run ending requires a positive run ordinal")
	}
	if len(v.Reason) > MaxRunEndedReasonBytes || !utf8.ValidString(string(v.Reason)) {
		return errors.New("run ending reason must be UTF-8 and at most 32 bytes")
	}
	switch v.Reason {
	case RunEndedCompleted, RunEndedError, RunEndedLimit, RunEndedInterrupted, RunEndedCaptureFailed:
	default:
		return errors.New("unknown run ending reason")
	}
	if v.BulkToolCloseout && v.Reason != RunEndedLimit && v.Reason != RunEndedCaptureFailed {
		return errors.New("bulk tool closeout requires a limit or capture-failed ending")
	}
	if v.Explanation != nil && !utf8.ValidString(*v.Explanation) {
		return errors.New("ending explanation must be UTF-8")
	}
	if v.Capture != nil && v.Reason != RunEndedCaptureFailed {
		return errors.New("capture evidence requires a capture-failed ending")
	}
	return nil
}
