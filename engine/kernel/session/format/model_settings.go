package format

import (
	"errors"
	"unicode/utf8"
)

// ModelSettings retains explicit prepared model controls. Nil fields intentionally
// omit upstream controls; they never inherit defaults. Assignment shares pointers;
// Clone detaches them. Zero is valid and omits all controls. Preparation resolves
// preferences; consumers must not normalize or clamp retained settings.
//
// Design status: these initial two controls are intentionally minimal. Revisit
// additional controls and provider grouping at adapter integration; retained
// omission and exact values remain guaranteed.
//
//dtogen:include
//dtogen:validate Validate
type ModelSettings struct {
	// ReasoningEffort is an optional nonempty exact UTF-8 target-native label,
	// not a universal closed vocabulary. Nil intentionally omits the control.
	ReasoningEffort *string `cbor:"r,omitempty"`
	// MaxOutputTokens is an optional positive output-token limit. Nil intentionally
	// omits the control; zero and negative values are invalid when present.
	MaxOutputTokens *int64 `cbor:"m,omitempty"`
}

// Validate checks local control syntax, not target support or provider policy.
func (v ModelSettings) Validate() error {
	if v.ReasoningEffort != nil && (*v.ReasoningEffort == "" || !utf8.ValidString(*v.ReasoningEffort)) {
		return errors.New("reasoning effort requires a nonempty UTF-8 label")
	}
	if v.MaxOutputTokens != nil && *v.MaxOutputTokens <= 0 {
		return errors.New("maximum output tokens must be positive")
	}
	return nil
}
