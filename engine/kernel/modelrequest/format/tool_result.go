package format

import (
	"errors"
	"unicode/utf8"

	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// ToolOutcome is the closed logical result vocabulary; zero and unknown are invalid.
//
//dtogen:validate Validate
type ToolOutcome string

const (
	// ToolOutcomeReturnedSuccess records a returned successful execution.
	ToolOutcomeReturnedSuccess ToolOutcome = "returned-success"
	// ToolOutcomeReturnedError records a returned execution error.
	ToolOutcomeReturnedError ToolOutcome = "returned-error"
	// ToolOutcomeNotInvoked records known non-invocation.
	ToolOutcomeNotInvoked ToolOutcome = "not-invoked"
	// ToolOutcomeIndeterminate records unknown outcome with possible side effects.
	ToolOutcomeIndeterminate ToolOutcome = "indeterminate"
)

// Validate rejects unknown outcomes without reinterpreting them.
func (v ToolOutcome) Validate() error {
	switch v {
	case ToolOutcomeReturnedSuccess, ToolOutcomeReturnedError, ToolOutcomeNotInvoked, ToolOutcomeIndeterminate:
		return nil
	}
	return errors.New("unknown tool outcome")
}

// ToolResult is one logical result in requested-call order. RequestV1 checks
// response-local correlation; assignment shares parts and Clone detaches them.
//
//dtogen:validate validateLocal
type ToolResult struct {
	// ProviderCallID is required nonempty exact UTF-8 from the original call.
	ProviderCallID string `cbor:"i"`
	// Outcome is required and closed; it is independent of rendered notices.
	Outcome ToolOutcome `cbor:"o"`
	// Parts preserves prepared notices and retained output order. Nil and empty
	// are equivalent. No diagnostics or execution evidence are embedded here.
	Parts []sessionformat.ContentPart `cbor:"p"`
}

// Validate checks ID, outcome and typed content, not enclosing call membership.
func (v ToolResult) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if err := v.Outcome.Validate(); err != nil {
		return err
	}
	return (ContentBlock{Parts: v.Parts}).Validate()
}

// validateLocal checks ID syntax after bounded child traversal.
func (v ToolResult) validateLocal() error {
	if v.ProviderCallID == "" || !utf8.ValidString(v.ProviderCallID) {
		return errors.New("result requires a nonempty UTF-8 provider call ID")
	}
	return nil
}
