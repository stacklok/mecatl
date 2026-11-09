package format

import (
	"errors"
	"unicode/utf8"
)

// InvocationFailureClass is a closed classification of observed invocation failure.
// Empty and unknown values are rejected.
type InvocationFailureClass string

// InvocationFailure.Class values form a closed vocabulary; empty and unknown
// classes are rejected.
const (
	// InvocationFailureTransport encodes an observed transport failure.
	InvocationFailureTransport InvocationFailureClass = "transport"
	// InvocationFailureTimeout encodes an observed timeout.
	InvocationFailureTimeout InvocationFailureClass = "timeout"
	// InvocationFailureLocal encodes an observed local failure.
	InvocationFailureLocal InvocationFailureClass = "local"
	// InvocationFailureOther encodes another observed failure.
	InvocationFailureOther InvocationFailureClass = "other"
)

// InvocationFailure records an observed invocation or delivery failure without
// constructing a provider response or asserting no remote processing. Assignment
// shares nested storage; Clone detaches it. Session authoring supplies the full
// destination request separately and requires RunID and RequestID zero. Validate
// requires the fully populated persisted shape.
//
//dtogen:validate validateLocal
//dtogen:include
type InvocationFailure struct {
	// RunID is the required positive numeric run component within the history
	// supplied by the enclosing envelope.
	RunID uint64 `cbor:"r" view:"-"`
	// RequestID is the required positive numeric request component within RunID.
	// Payload validation checks positivity, not accepted request membership.
	RequestID uint64 `cbor:"q" view:"manual"`
	// Class is required and must be an InvocationFailure closed-vocabulary value.
	Class InvocationFailureClass `cbor:"c"`
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

// Validate checks persisted identity, closed class and all retained evidence.
// It does not establish acceptance, disclosure safety or remote effects.
func (v InvocationFailure) Validate() error {
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

// validateLocal checks immediate fields after bounded traversal validates children.
func (v InvocationFailure) validateLocal() error {
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
