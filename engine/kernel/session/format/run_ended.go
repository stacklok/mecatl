package format

import (
	"errors"
	"unicode/utf8"
)

const (
	// RunEndedCompleted encodes clean completed termination.
	RunEndedCompleted = 1
	// RunEndedError encodes owner-selected error termination.
	RunEndedError = 2
	// RunEndedLimit encodes owner-selected limit termination.
	RunEndedLimit = 3
	// RunEndedCancelled encodes owner-selected cancellation termination.
	RunEndedCancelled = 4
	// RunEndedInterrupted encodes owner-selected interrupted termination.
	RunEndedInterrupted = 5
	// RunEndedCaptureFailed encodes inability to capture a complete response.
	RunEndedCaptureFailed = 6
)

// CaptureFailureEvidence is ending-owned evidence for a request whose complete
// response could not be captured. It has no response body or private envelope.
// Assignment shares nested storage; Clone detaches it.
//
//cborgen:validate Validate
type CaptureFailureEvidence struct {
	RequestOrdinal uint64            `cbor:"q"`
	Evidence       *ObservedEvidence `cbor:"e,omitempty"`
	Usage          *InvocationUsage  `cbor:"u,omitempty"`
}

// Validate checks only the local request reference shape.
func (v CaptureFailureEvidence) Validate() error {
	if v.RequestOrdinal == 0 {
		return errors.New("capture failure evidence requires a positive request ordinal")
	}
	return nil
}

// RunEnded terminally records the owner's ending reason without fabricating any
// response, cancellation, or external effect. Evidence is permitted only for a
// capture-failed ending. Assignment shares nested storage; Clone detaches it.
//
//cborgen:validate Validate
type RunEnded struct {
	RunID       uint64                  `cbor:"r"`
	Reason      uint64                  `cbor:"e"`
	Explanation *string                 `cbor:"x,omitempty"`
	Capture     *CaptureFailureEvidence `cbor:"c,omitempty"`
}

// Validate checks the local run and reason shape, including capture evidence use.
func (v RunEnded) Validate() error {
	if v.RunID == 0 {
		return errors.New("run ending requires a positive run ordinal")
	}
	switch v.Reason {
	case RunEndedCompleted, RunEndedError, RunEndedLimit, RunEndedCancelled, RunEndedInterrupted, RunEndedCaptureFailed:
	default:
		return errors.New("unknown run ending reason")
	}
	if v.Explanation != nil && !utf8.ValidString(*v.Explanation) {
		return errors.New("ending explanation must be UTF-8")
	}
	if v.Capture != nil && v.Reason != RunEndedCaptureFailed {
		return errors.New("capture evidence requires a capture-failed ending")
	}
	return nil
}
