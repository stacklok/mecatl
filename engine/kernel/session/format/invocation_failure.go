package format

import (
	"errors"
	"unicode/utf8"
)

// InvocationFailure.Class values form a closed vocabulary; empty and unknown
// classes are rejected.
const (
	// InvocationFailureTransport encodes an observed transport failure.
	InvocationFailureTransport = "transport"
	// InvocationFailureTimeout encodes an observed timeout.
	InvocationFailureTimeout = "timeout"
	// InvocationFailureLocal encodes an observed local failure.
	InvocationFailureLocal = "local"
	// InvocationFailureOther encodes another observed failure.
	InvocationFailureOther = "other"
)

// InvocationFailure records an observed invocation or delivery failure without
// constructing a provider response or asserting no remote processing. Assignment
// shares nested storage; Clone detaches it.
//
//dtogen:validate Validate
type InvocationFailure struct {
	// RunID is the required positive numeric run component within the history
	// supplied by the enclosing envelope.
	RunID uint64 `cbor:"r"`
	// RequestID is the required positive numeric request component within RunID.
	// Payload validation checks positivity, not accepted request membership.
	RequestID uint64 `cbor:"q"`
	// Class is required and must be an InvocationFailure closed-vocabulary value.
	Class string `cbor:"c"`
	// Explanation is optional UTF-8 owner-provided human-readable failure detail.
	// Nil omits it; non-nil empty is retained. It is distinct from Evidence.Detail.
	Explanation *string `cbor:"e,omitempty"`
	// Evidence is optional diagnostic observation. Nil omits it; codec traversal
	// validates its local UTF-8 shape, not disclosure safety or remote effects.
	Evidence *ObservedEvidence `cbor:"v,omitempty"`
	// Usage is optional provider-reported usage. Nil omits it; codec traversal
	// validates local counters and optional opaque metadata.
	Usage *InvocationUsage `cbor:"u,omitempty"`
}

// Validate checks local identity, the closed observed class, and UTF-8 explanation
// syntax; it does not establish disclosure safety.
func (v InvocationFailure) Validate() error {
	if v.RunID == 0 || v.RequestID == 0 {
		return errors.New("invocation failure requires positive run and request ordinals")
	}
	switch v.Class {
	case InvocationFailureTransport, InvocationFailureTimeout, InvocationFailureLocal, InvocationFailureOther:
	default:
		return errors.New("unknown invocation failure class")
	}
	if v.Explanation != nil && !utf8.ValidString(*v.Explanation) {
		return errors.New("failure explanation must be UTF-8")
	}
	return nil
}
