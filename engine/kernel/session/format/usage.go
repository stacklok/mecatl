package format

import (
	"errors"
	"unicode/utf8"
)

// Usage is one complete accounting report for a history-scoped identity. Its zero
// Go value is invalid because it lacks a positive ID and a valid report state.
// Explicitly reported zero token counts are valid and distinct from absent counts.
// The first record alone carries Definition and must be active; later records omit
// Definition. That first-record rule is aggregate policy; local validation cannot
// prove it. Accepted chronological order determines the latest replacement, never
// a delta, merge, or timestamp. Reports may repeat or decrease counters. Assignment
// shares nested storage; Clone detaches it. Local validation cannot prove allocation
// or membership; those checks belong to aggregate admission.
//
//dtogen:include
//dtogen:validate validateLocal
type Usage struct {
	// ID is the required positive usage component within the enclosing history.
	// It is independent of runs; the history envelope supplies omitted scope.
	ID uint64 `cbor:"o" view:"manual"`
	// Definition is required only on the first record for ID, and immutable
	// thereafter. Nil denotes a replacement report, not missing metadata to merge.
	Definition *UsageDefinition `cbor:"d,omitempty" view:"manual"`
	// Report is the required complete replacement, including explicit state.
	Report UsageReport `cbor:"r"`
}

// Validate checks the complete payload shape, not chronological legality.
func (v Usage) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Definition != nil {
		if err := v.Definition.Validate(); err != nil {
			return err
		}
	}
	return v.Report.Validate()
}

// validateLocal checks identity and the first-record rule after bounded children.
func (v Usage) validateLocal() error {
	if v.ID == 0 {
		return errors.New("usage ID must be positive")
	}
	if v.Definition != nil && v.Report.State != UsageReportActive {
		return errors.New("first usage report must be active")
	}
	return nil
}

const (
	// UsagePurposeMain labels conversational model work, without reserved authority.
	UsagePurposeMain = "main"
)

// UsageDefinition is immutable first-record metadata. Correlations omit enclosing
// history scope and assert association, not unique ownership or complete coverage.
// Assignment shares optional components; Clone detaches them. Zero is invalid.
//
//dtogen:validate Validate
type UsageDefinition struct {
	// Purpose is required nonempty exact UTF-8, open beyond the established
	// UsagePurpose constants. No trimming, fallback, or normalization is applied.
	Purpose string `cbor:"p"`
	// RunID optionally correlates a positive run component within the enclosing
	// history. Nil with nil RequestID means session-level work. Zero is invalid.
	RunID *uint64 `cbor:"r,omitempty" view:"manual"`
	// RequestID optionally correlates a positive request component within RunID;
	// it requires RunID. Nil means no request correlation. Admission checks both
	// references against preceding facts, not the currently active run.
	RequestID *uint64 `cbor:"q,omitempty" view:"manual"`
}

// Validate checks exact purpose syntax and relative reference shape, not history
// scope, allocation, membership or lifecycle.
func (v UsageDefinition) Validate() error {
	if v.Purpose == "" || !utf8.ValidString(v.Purpose) {
		return errors.New("usage purpose requires nonempty UTF-8")
	}
	if v.RunID != nil && *v.RunID == 0 {
		return errors.New("usage run ID must be positive")
	}
	if v.RequestID != nil && (v.RunID == nil || *v.RequestID == 0) {
		return errors.New("usage request requires positive run and request IDs")
	}
	return nil
}
