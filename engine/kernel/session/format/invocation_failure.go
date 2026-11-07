package format

import (
	"errors"
	"unicode/utf8"
)

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
//cborgen:validate Validate
type InvocationFailure struct {
	RunID          uint64            `cbor:"r"`
	RequestOrdinal uint64            `cbor:"q"`
	Class          string            `cbor:"c"`
	Explanation    *string           `cbor:"e,omitempty"`
	Evidence       *ObservedEvidence `cbor:"v,omitempty"`
	Usage          *InvocationUsage  `cbor:"u,omitempty"`
}

// Validate checks local identity, the closed observed class and safe explanation.
func (v InvocationFailure) Validate() error {
	if v.RunID == 0 || v.RequestOrdinal == 0 {
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
