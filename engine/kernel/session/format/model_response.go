package format

import (
	"errors"
	"unicode/utf8"
)

// ResponseContent.Kind values form a closed vocabulary; empty and unknown kinds
// are rejected.
const (
	// ResponseContentOutput encodes ordinary model output.
	ResponseContentOutput = "output"
	// ResponseContentReasoning encodes readable model reasoning.
	ResponseContentReasoning = "reasoning"
	// ResponseContentRefusal encodes a model refusal.
	ResponseContentRefusal = "refusal"
)

// Normalized ModelResponse.Terminal classes form a closed vocabulary. Empty and
// unknown classes are rejected; ProviderStopReason remains provider-owned.
const (
	// ResponseTerminalNormal encodes a normal terminal response.
	ResponseTerminalNormal = "normal"
	// ResponseTerminalLimit encodes a provider limit terminal response.
	ResponseTerminalLimit = "limit"
	// ResponseTerminalRefusal encodes a provider refusal terminal response.
	ResponseTerminalRefusal = "refusal"
	// ResponseTerminalError encodes a provider error terminal response.
	ResponseTerminalError = "error"
	// ResponseTerminalCancelled encodes a provider cancellation terminal response.
	ResponseTerminalCancelled = "cancelled"
	// ResponseTerminalOther encodes another terminal response.
	ResponseTerminalOther = "other"
)

// RequestedCall retains one complete requested-call occurrence in response order.
// Name and Arguments are exact opaque adapter output: empty names and invalid or
// empty JSON are valid. Arguments may be nil or empty, which are equivalent empty
// bytes. Assignment shares Arguments; Clone detaches them.
//
//cborgen:validate Validate
type RequestedCall struct {
	// Name is UTF-8 adapter output. It may be empty or unknown to the session.
	Name string `cbor:"n"`
	// Arguments are opaque adapter output and are not required to be JSON. Nil and
	// empty are equivalent empty bytes; validation does not interpret them.
	Arguments []byte `cbor:"a"`
}

// Validate checks only UTF-8 name syntax.
func (v RequestedCall) Validate() error {
	if !utf8.ValidString(v.Name) {
		return errors.New("call name must be UTF-8")
	}
	return nil
}

// ResponseContent is one ordered content branch. Kind selects a closed content
// meaning; Parts preserves order, duplicates, and boundaries. Parts may be nil or
// empty, which are equivalent explicitly empty content. Assignment shares parts;
// Clone detaches them.
//
//cborgen:validate Validate
type ResponseContent struct {
	// Kind is required and must be a ResponseContent closed-vocabulary value.
	Kind string `cbor:"k"`
	// Parts are ordered content parts for Kind. Nil and empty are equivalent empty
	// content; codec traversal validates each part.
	Parts []ContentPart `cbor:"p"`
}

// Validate checks the closed content kind; codec traversal validates parts.
func (v ResponseContent) Validate() error {
	switch v.Kind {
	case ResponseContentOutput, ResponseContentReasoning, ResponseContentRefusal:
		return nil
	default:
		return errors.New("unknown response content kind")
	}
}

// ResponseItem preserves one ordered response occurrence. Exactly one branch is
// required; assignment shares its selected child and Clone detaches it.
//
//cborgen:validate Validate
type ResponseItem struct {
	// Content is the optional content branch. It is mutually exclusive with Call;
	// codec traversal validates it when present.
	Content *ResponseContent `cbor:"c,omitempty"`
	// Call is the optional requested-call branch. It is mutually exclusive with
	// Content; codec traversal validates it when present.
	Call *RequestedCall `cbor:"a,omitempty"`
}

// Validate checks branch exclusivity only; codec traversal validates the child.
func (v ResponseItem) Validate() error {
	if (v.Content == nil) == (v.Call == nil) {
		return errors.New("response item requires exactly one branch")
	}
	return nil
}

// ModelResponse is an atomic terminal provider response. Private is one
// response-wide envelope, not per-item provider identity. Assignment shares nested
// storage; Clone detaches it.
//
//cborgen:validate Validate
type ModelResponse struct {
	// RunID is the required positive numeric run component within the history
	// supplied by the enclosing envelope.
	RunID uint64 `cbor:"r"`
	// RequestID is the required positive numeric request component within RunID.
	// Payload validation checks positivity, not accepted request membership.
	RequestID uint64 `cbor:"q"`
	// Terminal is a required normalized ResponseTerminal class; empty and unknown
	// classes are rejected independently of the original provider stop reason.
	Terminal string `cbor:"t"`
	// ProviderStopReason retains the provider's original stop/finish reason, not
	// reasoning content or a reasoning summary. The vocabulary is provider-owned
	// and open; any UTF-8 string is allowed. Nil omits key p; non-nil empty is retained.
	ProviderStopReason *string `cbor:"p,omitempty"`
	// Items preserve complete response order, including content/call interleaving.
	// Nil and empty are equivalent empty responses; codec traversal validates items.
	Items []ResponseItem `cbor:"i"`
	// Evidence is optional diagnostic observation. Nil omits it; traversal validates
	// only its local UTF-8 contract, not disclosure safety or remote effects.
	Evidence *ObservedEvidence `cbor:"e,omitempty"`
	// Usage is optional provider-reported usage. Nil omits it; traversal validates
	// its local counters and optional opaque metadata.
	Usage *InvocationUsage `cbor:"u,omitempty"`
	// Private is optional response-wide opaque provider data, not per-item identity.
	// Nil omits it; traversal validates metadata but does not interpret its bytes.
	Private *OpaqueProviderData `cbor:"x,omitempty"`
}

// Validate checks local identity, terminal class and optional UTF-8 reason.
func (v ModelResponse) Validate() error {
	if v.RunID == 0 || v.RequestID == 0 {
		return errors.New("response requires positive run and request ordinals")
	}
	switch v.Terminal {
	case ResponseTerminalNormal, ResponseTerminalLimit, ResponseTerminalRefusal, ResponseTerminalError, ResponseTerminalCancelled, ResponseTerminalOther:
	default:
		return errors.New("unknown response terminal class")
	}
	if v.ProviderStopReason != nil && !utf8.ValidString(*v.ProviderStopReason) {
		return errors.New("provider stop reason must be UTF-8")
	}
	return nil
}
