package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReflectProjectionPreservesVerifiedReferences(t *testing.T) {
	current, _ := admittedInput(t)
	current.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	var legacy learning.Input
	raw, err := os.ReadFile("../learning/testdata/legacy-evidence-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Signals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	for name, in := range map[string]learning.Input{"v2": current, "frozen-v1": legacy} {
		t.Run(name, func(t *testing.T) {
			handle := "m:0"
			if name == "frozen-v1" {
				handle = "m:1"
			}
			original, err := learning.ResolveEvidenceHandle(in, handle)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := learning.ProjectVerifiedInput(in)
			if err != nil {
				t.Fatal(err)
			}
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				wire, _ := json.Marshal(req)
				if strings.Contains(string(wire), "provider-call-legacy") {
					t.Fatal("private binding entered model request")
				}
			})}, mockllm.TextTurn(string(modelOutcome(learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise"}, handle))))
			reflector, err := agent.NewEvidenceReflector(provider, session.ProviderModelID{ProviderID: "test", ModelID: "model"}, nil, agent.ReflectionLimits{})
			if err != nil {
				t.Fatal(err)
			}
			out, _, err := reflector.ReflectProjection(context.Background(), projection)
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != learning.OutcomeProposed || len(out.Candidates) != 1 || !reflect.DeepEqual(out.Candidates[0].Evidence, []learning.EvidenceRef{original}) {
				t.Fatalf("lost frozen binding: %+v", out)
			}
			if err := learning.ResolveEvidence(in, out.Candidates[0].Evidence[0]); err != nil {
				t.Fatal(err)
			}
			projection.Messages[0].Text = "tampered"
			if _, _, err := reflector.ReflectProjection(context.Background(), projection); err == nil {
				t.Fatal("modified projection accepted")
			}
			wire, _ := json.Marshal(projection)
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
		})
	}
}
