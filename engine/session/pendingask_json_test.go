package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
)

func TestPendingAskApprovalOriginRoundTrips(t *testing.T) {
	in := PendingAsk{AskID: "a1", Tool: "Shell", Call: "c1", Origin: ApprovalOriginHookGuardrail, Guardrail: &GuardrailPendingScope{ReviewID: "r1", Kind: GuardrailApprovalAction, GrantDigest: "opaque", SessionOnly: true, RepeatAvailable: true}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out PendingAsk
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Origin != ApprovalOriginHookGuardrail || out.Guardrail == nil || out.Guardrail.Kind != GuardrailApprovalAction {
		t.Fatalf("round trip = %+v", out)
	}
}

func TestPendingAskUnknownOriginOmitted(t *testing.T) {
	b, err := json.Marshal(PendingAsk{AskID: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"origin"`) {
		t.Fatalf("unknown origin must be omitted: %s", b)
	}
	var out PendingAsk
	if err := json.Unmarshal([]byte(`{"AskID":"a1"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Origin != ApprovalOriginUnknown {
		t.Fatalf("legacy origin = %q", out.Origin)
	}
}

func TestPendingAskRunLocalPolicyHintsAreNotSerialized(t *testing.T) {
	b, err := json.Marshal(PendingAsk{AskID: "a1", Origin: ApprovalOriginPermission, AskProvenance: governance.AskProvenanceConfigured})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "AskProvenance") {
		t.Fatalf("run-local hints serialized: %s", b)
	}
}
