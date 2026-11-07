package format

import (
	"errors"
	"unicode/utf8"
)

const (
	// ResponseContentOutput encodes ordinary model output.
	ResponseContentOutput = "output"
	// ResponseContentReasoning encodes readable model reasoning.
	ResponseContentReasoning = "reasoning"
	// ResponseContentRefusal encodes a model refusal.
	ResponseContentRefusal = "refusal"
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

// OpaqueProviderData retains producer-owned exact bytes. The session does not
// interpret the format or payload. Assignment shares Data; Clone detaches it.
//
//cborgen:validate Validate
type OpaqueProviderData struct {
	Producer string `cbor:"p"`
	Format   string `cbor:"f"`
	Version  int64  `cbor:"v"`
	Data     []byte `cbor:"d"`
}

// Validate checks identifiers and version, not payload semantics.
func (v OpaqueProviderData) Validate() error {
	if v.Producer == "" || v.Format == "" || !utf8.ValidString(v.Producer) || !utf8.ValidString(v.Format) || v.Version <= 0 {
		return errors.New("opaque provider data requires UTF-8 producer, format, and positive version")
	}
	return nil
}

// InvocationUsage retains optional reported counters and optional usage-only raw
// evidence. Input and Output are inclusive counts; CacheRead, CacheWrite, and
// Reasoning are breakdowns rather than additional spend. Nil counters are absent
// and distinct from reported zero.
//
//cborgen:validate Validate
type InvocationUsage struct {
	Input      *int64              `cbor:"i,omitempty"`
	Output     *int64              `cbor:"o,omitempty"`
	CacheRead  *int64              `cbor:"r,omitempty"`
	CacheWrite *int64              `cbor:"w,omitempty"`
	Reasoning  *int64              `cbor:"n,omitempty"`
	Raw        *OpaqueProviderData `cbor:"x,omitempty"`
}

// Validate checks nonnegative reported counters, not accounting equations.
func (v InvocationUsage) Validate() error {
	for _, n := range []*int64{v.Input, v.Output, v.CacheRead, v.CacheWrite, v.Reasoning} {
		if n != nil && *n < 0 {
			return errors.New("usage count must be nonnegative")
		}
	}
	return nil
}

// ObservedEvidence retains optional safe observed detail and correlation without
// asserting remote effects. Nil is absent; non-nil empty strings are preserved.
//
//cborgen:validate Validate
type ObservedEvidence struct {
	Detail      *string `cbor:"d,omitempty"`
	Correlation *string `cbor:"c,omitempty"`
}

// Validate checks UTF-8 only; callers are responsible for disclosure safety.
func (v ObservedEvidence) Validate() error {
	for _, s := range []*string{v.Detail, v.Correlation} {
		if s != nil && !utf8.ValidString(*s) {
			return errors.New("observed evidence must be UTF-8")
		}
	}
	return nil
}

// RequestedCall retains one complete requested-call occurrence. Name and arguments
// are exact opaque adapter output: empty names and invalid or empty JSON are valid.
// Assignment shares Arguments; Clone detaches them.
//
//cborgen:validate Validate
type RequestedCall struct {
	Name      string `cbor:"n"`
	Arguments []byte `cbor:"a"`
}

// Validate checks only UTF-8 name syntax.
func (v RequestedCall) Validate() error {
	if !utf8.ValidString(v.Name) {
		return errors.New("call name must be UTF-8")
	}
	return nil
}

// ResponseContent is one content branch. Parts preserves order; nil and empty are
// valid explicitly empty content. Assignment shares parts; Clone detaches them.
//
//cborgen:validate Validate
type ResponseContent struct {
	Kind  string        `cbor:"k"`
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
// required. Assignment shares children; Clone detaches them.
//
//cborgen:validate Validate
type ResponseItem struct {
	Content *ResponseContent `cbor:"c,omitempty"`
	Call    *RequestedCall   `cbor:"a,omitempty"`
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
	RunID          uint64              `cbor:"r"`
	RequestOrdinal uint64              `cbor:"q"`
	Terminal       string              `cbor:"t"`
	ProviderReason *string             `cbor:"p,omitempty"`
	Items          []ResponseItem      `cbor:"i"`
	Evidence       *ObservedEvidence   `cbor:"e,omitempty"`
	Usage          *InvocationUsage    `cbor:"u,omitempty"`
	Private        *OpaqueProviderData `cbor:"x,omitempty"`
}

// Validate checks local identity, terminal class and optional UTF-8 reason.
func (v ModelResponse) Validate() error {
	if v.RunID == 0 || v.RequestOrdinal == 0 {
		return errors.New("response requires positive run and request ordinals")
	}
	switch v.Terminal {
	case ResponseTerminalNormal, ResponseTerminalLimit, ResponseTerminalRefusal, ResponseTerminalError, ResponseTerminalCancelled, ResponseTerminalOther:
	default:
		return errors.New("unknown response terminal class")
	}
	if v.ProviderReason != nil && !utf8.ValidString(*v.ProviderReason) {
		return errors.New("provider reason must be UTF-8")
	}
	return nil
}
