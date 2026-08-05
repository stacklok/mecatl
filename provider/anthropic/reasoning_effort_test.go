package anthropic

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/stacklok/mecatl/engine/port"
)

// TestOutputConfigEffortStamped pins that the construction-configured effort is
// stamped onto output_config.effort for ALL FIVE neutral tiers (Anthropic
// identity-maps them — ADR 0055).
func TestOutputConfigEffortStamped(t *testing.T) {
	cases := []struct {
		effort string
		want   sdk.OutputConfigEffort
	}{
		{"low", sdk.OutputConfigEffortLow},
		{"medium", sdk.OutputConfigEffortMedium},
		{"high", sdk.OutputConfigEffortHigh},
		{"xhigh", sdk.OutputConfigEffortXhigh},
		{"max", sdk.OutputConfigEffortMax},
	}
	for _, tc := range cases {
		t.Run(tc.effort, func(t *testing.T) {
			p := New(WithAPIKey("sk-test"), WithMaxTokens(16000), WithReasoningEffort(tc.effort))
			params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			if params.OutputConfig.Effort != tc.want {
				t.Errorf("OutputConfig.Effort = %q, want %q", params.OutputConfig.Effort, tc.want)
			}
		})
	}
}

// TestOutputConfigEffortOmittedWhenUnset pins that an empty/"auto"/unrecognised
// token OMITS output_config.effort (the byte-stable default + fail-soft contract).
func TestOutputConfigEffortOmittedWhenUnset(t *testing.T) {
	for _, effort := range []string{"", "auto", "ultra"} {
		t.Run("token="+effort, func(t *testing.T) {
			p := New(WithAPIKey("sk-test"), WithMaxTokens(16000), WithReasoningEffort(effort))
			params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			if params.OutputConfig.Effort != "" {
				t.Errorf("OutputConfig.Effort = %q, want empty (omitted) for token %q", params.OutputConfig.Effort, effort)
			}
		})
	}
}

// TestEffortAndThinkingCoexist pins that reasoning effort is INDEPENDENT of the
// extended-thinking config — setting effort must NOT clear the thinking config and
// vice versa (ADR 0055: both ride the same request). claude-sonnet-4-6 is an
// adaptive-thinking model, so the Thinking union carries an adaptive variant.
func TestEffortAndThinkingCoexist(t *testing.T) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000), WithReasoningEffort("high"))
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if params.OutputConfig.Effort != sdk.OutputConfigEffortHigh {
		t.Errorf("OutputConfig.Effort = %q, want high", params.OutputConfig.Effort)
	}
	// Thinking must still be set (adaptive for sonnet-4-6); a zero/empty union here
	// would mean setting effort clobbered the thinking config.
	if params.Thinking.OfAdaptive == nil && params.Thinking.OfEnabled == nil {
		t.Error("Thinking config is empty alongside effort — the two axes must coexist independently")
	}
}
