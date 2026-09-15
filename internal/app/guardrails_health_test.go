package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

type healthSequenceReviewer struct {
	results []agent.ToolReviewResult
	errs    []error
	calls   int
}

func (r *healthSequenceReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	i := r.calls
	r.calls++
	return r.results[i], r.errs[i]
}

func TestGuardrailRouteHealthTracksActualReviewsNotRuleSkips(t *testing.T) {
	health := &guardrailRouteHealth{}
	base := &healthSequenceReviewer{
		results: []agent.ToolReviewResult{{Assessment: agent.ReviewUnresolved}, {Assessment: agent.ReviewUnresolved}, {Assessment: agent.ReviewAcceptable}},
		errs:    []error{errors.New("provider unavailable secret=do-not-project"), nil, nil},
	}
	rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}, {Match: "Read", Phases: []string{"pre"}, Mode: "block", SkipReadOnlyShell: true}})
	if !ok {
		t.Fatal("compile rules")
	}
	reviewer := &guardrailActionReviewer{base: base, rules: rules, health: health}
	if seen, _, _ := health.snapshot(); seen {
		t.Fatal("fresh checker route must be not-yet-checked")
	}

	req := agent.ToolReviewRequest{Job: agent.ReviewJobAction, EffectiveCall: session.NewToolCall("c1", "Shell", []byte(`{"command":"write"}`))}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if seen, inspection, assessment := health.snapshot(); !seen || inspection != "operational_failure" || assessment != "" {
		t.Fatalf("outage health = %v %q %q", seen, inspection, assessment)
	}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if _, inspection, assessment := health.snapshot(); inspection != "complete" || assessment != "unresolved" {
		t.Fatalf("completed unresolved health = %q %q", inspection, assessment)
	}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if _, inspection, assessment := health.snapshot(); inspection != "complete" || assessment != "acceptable" {
		t.Fatalf("healthy assessment = %q %q", inspection, assessment)
	}
	if base.calls != 3 {
		t.Fatalf("review calls = %d", base.calls)
	}
}
