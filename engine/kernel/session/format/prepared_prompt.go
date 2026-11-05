package format

import "errors"

// PreparedPrompt selects exactly one origin by pointer presence. AuthoredMessage
// references a whole unchanged source message by zero-based index; it carries no
// caller-selected purpose. Assignment shares pointers; Clone detaches them.
//
//cborgen:validate Validate
type PreparedPrompt struct {
	AuthoredMessage *uint64        `cbor:"a,omitempty"`
	Harness         *HarnessPrompt `cbor:"h,omitempty"`
}

// Validate checks origin exclusivity, not source-message bounds or child validity.
func (v PreparedPrompt) Validate() error {
	if (v.AuthoredMessage == nil) == (v.Harness == nil) {
		return errors.New("prepared prompt requires exactly one origin")
	}
	return nil
}

// HarnessPrompt retains rewritten or supplemental content with declared purpose
// and provenance, never an exact-user claim or provider role. Assignment shares
// Content and Source storage; Clone detaches them.
//
//cborgen:validate Validate
type HarnessPrompt struct {
	Purpose string        `cbor:"p"`
	Content Content       `cbor:"c"`
	Source  ContentSource `cbor:"s"`
}

// Validate checks purpose only, not child validity, trust, or authorization.
func (v HarnessPrompt) Validate() error {
	if v.Purpose != "instructions" && v.Purpose != "context" {
		return errors.New("unknown harness prompt purpose")
	}
	return nil
}
