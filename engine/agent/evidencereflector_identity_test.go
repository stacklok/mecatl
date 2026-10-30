package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReflectProjectionPreservesVerifiedReferences(t *testing.T) {
	in, _ := admittedInput(t)
	in.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	handle := "m:0"
	original, err := learning.ResolveEvidenceHandle(in, handle)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := learning.ProjectInput(in)
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.TextTurn(string(modelOutcome(learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise"}, handle))))
	reflector, err := agent.NewEvidenceReflector(provider, session.ProviderModelID{ProviderID: "test", ModelID: "model"}, nil, agent.ReflectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := reflector.ReflectProjection(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != learning.OutcomeProposed || len(out.Candidates) != 1 || !reflect.DeepEqual(out.Candidates[0].Evidence, []learning.EvidenceRef{original}) {
		t.Fatalf("lost canonical binding: %+v", out)
	}
	if err := learning.ResolveEvidence(in, out.Candidates[0].Evidence[0]); err != nil {
		t.Fatal(err)
	}
	projection.Messages[0].Text = "tampered"
	if _, _, err := reflector.ReflectProjection(context.Background(), projection); err == nil {
		t.Fatal("modified projection accepted")
	}
	wire, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var unbound learning.Projection
	if err := json.Unmarshal(wire, &unbound); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reflector.ReflectProjection(context.Background(), unbound); err == nil {
		t.Fatal("display JSON established authority")
	}
	if provider.Calls() != 1 {
		t.Fatalf("invalid projections called provider: %d", provider.Calls())
	}
}
