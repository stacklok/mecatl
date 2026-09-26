package jevguardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestNativeDecisionMapsToReviewAssessment(t *testing.T) {
	for _, tc := range []struct {
		choice string
		want   agent.ReviewAssessment
	}{
		{"clean", agent.ReviewAcceptable},
		{"action_redirection", agent.ReviewProhibited},
		{"unresolved", agent.ReviewUnresolved},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				probabilities := map[string]float64{"clean": 0.01, "action_redirection": 0.01, "inbound_redirection": 0.01, "unresolved": 0.01}
				probabilities[tc.choice] = 0.97
				encoded, _ := json.Marshal(probabilities)
				_, _ = fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"contextual-guardrail":{"type":"choice","choice":%q,"probabilities":%s,"confidence":0.96}},"usage":{"input_tokens":3,"output_tokens":1}}`, tc.choice, encoded)
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
			if err != nil || result.Assessment != tc.want || tc.want == agent.ReviewProhibited && (len(result.Concerns) != 1 || result.Concerns[0].SourceRef != "call") {
				t.Fatalf("choice=%s result=%+v err=%v", tc.choice, result, err)
			}
		})
	}
}

func TestFailClosedWithoutCompleteContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	driver, err := New("test-key", Model, srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	req := agent.ToolReviewRequest{ReviewID: "r", Job: agent.ReviewJobAction, EffectiveCall: session.NewToolCall("c", "Shell", json.RawMessage(`{"command":"echo ok"}`)), Caller: agent.ReviewCaller{Role: "main"}, PrincipalFacts: []agent.ReviewPrincipalFact{{Kind: "genuine_user_task", Statement: "print ok", PositiveVerdict: true}}, PrincipalFactsComplete: true, EvidenceComplete: true, TrajectoryComplete: true}
	for _, mutate := range []func(*agent.ToolReviewRequest){
		func(r *agent.ToolReviewRequest) { r.PrincipalFacts = nil },
		func(r *agent.ToolReviewRequest) { r.PrincipalFactsComplete = false },
		func(r *agent.ToolReviewRequest) { r.TrajectoryComplete = false },
		func(r *agent.ToolReviewRequest) { r.EvidenceComplete = false },
		func(r *agent.ToolReviewRequest) { r.Job = agent.ReviewJobPermission },
		func(r *agent.ToolReviewRequest) {
			r.EffectiveCall.Args = json.RawMessage(`{"content":"` + strings.Repeat("a", 17000) + `"}`)
		},
		func(r *agent.ToolReviewRequest) {
			r.Evidence = []agent.ReviewEvidenceMeta{{Handle: "missing", Version: "v", Complete: true}}
		},
	} {
		candidate := req
		mutate(&candidate)
		result, err := driver.Review(context.Background(), candidate, nil, nil)
		if err == nil || result.Assessment != agent.ReviewUnresolved {
			t.Fatalf("failed closed: %#v %v", result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("sent incomplete request: %d", calls.Load())
	}
}
