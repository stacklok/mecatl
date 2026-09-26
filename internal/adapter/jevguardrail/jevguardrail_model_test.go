package jevguardrail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestDifferentResponseModelCannotClear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-other","answers":{"contextual-guardrail":{"type":"choice","choice":"clean","probabilities":{"clean":0.97,"action_redirection":0.01,"inbound_redirection":0.01,"unresolved":0.01},"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	driver, err := New("test-key", Model, srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	req := agent.ToolReviewRequest{ReviewID: "r", Job: agent.ReviewJobAction,
		EffectiveCall:          session.NewToolCall("c", "Shell", json.RawMessage(`{"command":"echo ok"}`)),
		Caller:                 agent.ReviewCaller{Role: "main"},
		PrincipalFacts:         []agent.ReviewPrincipalFact{{Kind: "genuine_user_task", Statement: "print ok", PositiveVerdict: true}},
		PrincipalFactsComplete: true, EvidenceComplete: true, TrajectoryComplete: true}
	result, err := driver.Review(context.Background(), req, nil, nil)
	if err != nil || result.Assessment != agent.ReviewUnresolved {
		t.Fatalf("mismatched response model cleared: %+v, err=%v", result, err)
	}
}
