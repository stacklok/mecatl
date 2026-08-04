package openai

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/shared"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func effortReq() port.LLMRequest {
	return port.LLMRequest{
		Model:    "gpt-5.2",
		System:   prompt.Layered{StablePrefix: "You are a coding agent."},
		Messages: []session.Message{session.NewUserMessage("hi")},
	}
}

// TestReasoningEffortStamped pins that the Provider method buildParams stamps the
// construction-configured effort onto reasoning.effort for the supported tokens
// (ADR 0055).
func TestReasoningEffortStamped(t *testing.T) {
	cases := []struct {
		effort string
		want   shared.ReasoningEffort
	}{
		{"low", shared.ReasoningEffortLow},
		{"medium", shared.ReasoningEffortMedium},
		{"high", shared.ReasoningEffortHigh},
	}
	for _, tc := range cases {
		t.Run(tc.effort, func(t *testing.T) {
			p := New(WithAPIKey("sk-test"), WithReasoningEffort(tc.effort))
			params, err := p.buildParams(effortReq())
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			if params.Reasoning.Effort != tc.want {
				t.Errorf("Reasoning.Effort = %q, want %q", params.Reasoning.Effort, tc.want)
			}
		})
	}
}

// TestReasoningEffortOmittedWhenUnset pins that an empty/"auto"/unrecognised token
// OMITS reasoning.effort entirely (the byte-stable default + fail-soft contract).
// Composition does the xhigh/max→high clamp, so the adapter only ever sees
// low/medium/high in practice — but it must STILL omit on a surprise token rather
// than send something that could 400.
func TestReasoningEffortOmittedWhenUnset(t *testing.T) {
	for _, effort := range []string{"", "auto", "ultra", "xhigh-but-unexpected"} {
		t.Run("token="+effort, func(t *testing.T) {
			p := New(WithAPIKey("sk-test"), WithReasoningEffort(effort))
			params, err := p.buildParams(effortReq())
			if err != nil {
				t.Fatalf("buildParams: %v", err)
			}
			// "xhigh" is a real SDK tier, so it is NOT in the omit set; the others are.
			if effort == "" || effort == "auto" || effort == "ultra" || effort == "xhigh-but-unexpected" {
				if params.Reasoning.Effort != "" {
					t.Errorf("Reasoning.Effort = %q, want empty (omitted) for token %q", params.Reasoning.Effort, effort)
				}
			}
		})
	}
}

// TestReasoningEffortDefaultByteIdentical pins that a Provider built with NO effort
// produces exactly the params a bare buildParams(req) produces — the no-effort path
// is byte-identical to pre-0055.
func TestReasoningEffortDefaultByteIdentical(t *testing.T) {
	p := New(WithAPIKey("sk-test"))
	withMethod, err := p.buildParams(effortReq())
	if err != nil {
		t.Fatalf("buildParams (method): %v", err)
	}
	free, err := buildParams(effortReq())
	if err != nil {
		t.Fatalf("buildParams (free): %v", err)
	}
	// The no-effort provider's params must be byte-identical to the bare free
	// buildParams — compare the FULL marshalled wire params (the SDK params carry
	// unexported raw-JSON buffers, so JSON is the truest "byte-identical" check and
	// avoids a reflect.DeepEqual over unexported fields). A non-vacuous assertion:
	// it fails if effort (or anything else) leaked onto the no-effort path.
	mb, err := json.Marshal(withMethod)
	if err != nil {
		t.Fatalf("marshal (method): %v", err)
	}
	fb, err := json.Marshal(free)
	if err != nil {
		t.Fatalf("marshal (free): %v", err)
	}
	if !bytes.Equal(mb, fb) {
		t.Errorf("no-effort params diverge from the bare buildParams:\n method=%s\n free=%s", mb, fb)
	}
	// And reasoning.effort is absent in the wire form (omitzero).
	if bytes.Contains(mb, []byte(`"effort"`)) {
		t.Errorf("no-effort params must omit reasoning.effort; got %s", mb)
	}
}
