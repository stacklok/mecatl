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

type typedHealthFailure agent.ReviewFailureCode

func (typedHealthFailure) Error() string { return "HOSTILE_HEALTH_SENTINEL" }
func (e typedHealthFailure) GuardrailReviewFailureCode() agent.ReviewFailureCode {
	return agent.ReviewFailureCode(e)
}

func (r *healthSequenceReviewer) Review(context.Context, agent.ToolReviewRequest, agent.ReviewEvidenceSource) (agent.ToolReviewResult, error) {
	i := r.calls
	r.calls++
	return r.results[i], r.errs[i]
}

func TestPermissionReviewEligibilityRequiresEnforcingMatchingCoverage(t *testing.T) {
	call := session.NewToolCall("c", "Shell", []byte(`{"command":"echo $(zap) > out"}`))
	for _, tc := range []struct {
		name string
		spec modelhook.RuleSpec
		want bool
	}{
		{"enforcing match", modelhook.RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}, true},
		{"advisory", modelhook.RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: "advisory"}, false},
		{"unmatched", modelhook.RuleSpec{Match: "Read", Phases: []string{"pre"}, Mode: "block"}, false},
		{"skipped read-only", modelhook.RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: "block", SkipReadOnlyShell: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{tc.spec})
			if !ok {
				t.Fatal("compile rules")
			}
			r := &guardrailActionReviewer{rules: rules}
			if got := r.GuardrailPermissionReviewEligible(call); got != tc.want {
				t.Fatalf("eligible=%v want %v", got, tc.want)
			}
		})
	}

	rules, _ := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Shell", Phases: []string{"pre"}, Mode: "block", SkipReadOnlyShell: true}})
	readOnly := session.NewToolCall("c", "Shell", []byte(`{"command":"git status"}`))
	if (&guardrailActionReviewer{rules: rules}).GuardrailPermissionReviewEligible(readOnly) {
		t.Fatal("skipped read-only Shell became permission-review eligible")
	}
}

func TestGuardrailRouteHealthTracksClosedFailureCodes(t *testing.T) {
	codes := []agent.ReviewFailureCode{
		agent.ReviewFailureProviderFailure,
		agent.ReviewFailureTimeout,
		agent.ReviewFailureBlankAssessment,
		agent.ReviewFailureMalformedAssessment,
		agent.ReviewFailureInvalidAssessment,
		agent.ReviewFailureMissingSubmit,
		agent.ReviewFailureEvidenceFailure,
	}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			health := &guardrailRouteHealth{}
			base := &healthSequenceReviewer{results: []agent.ToolReviewResult{{Assessment: agent.ReviewUnresolved}}, errs: []error{typedHealthFailure(code)}}
			rules, ok := compileGuardrailRules(Config{}, []modelhook.RuleSpec{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}})
			if !ok {
				t.Fatal("compile rules")
			}
			reviewer := &guardrailActionReviewer{base: base, rules: rules, health: health}
			_, _ = reviewer.Review(context.Background(), agent.ToolReviewRequest{Job: agent.ReviewJobAction, EffectiveCall: session.NewToolCall("c", "Shell", []byte(`{"command":"write"}`))}, nil)
			if seen, inspection, _, got := health.snapshot(); !seen || inspection != "operational_failure" || got != code {
				t.Fatalf("health = seen:%t inspection:%q code:%q, want %q", seen, inspection, got, code)
			}
		})
	}
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
	if seen, _, _, _ := health.snapshot(); seen {
		t.Fatal("fresh checker route must be not-yet-checked")
	}

	req := agent.ToolReviewRequest{Job: agent.ReviewJobAction, EffectiveCall: session.NewToolCall("c1", "Shell", []byte(`{"command":"write"}`))}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if seen, inspection, assessment, code := health.snapshot(); !seen || inspection != "operational_failure" || assessment != "" || code != agent.ReviewFailureProviderFailure {
		t.Fatalf("outage health = %v %q %q %q", seen, inspection, assessment, code)
	}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if _, inspection, assessment, code := health.snapshot(); inspection != "complete" || assessment != "unresolved" || code != "" {
		t.Fatalf("completed unresolved health = %q %q", inspection, assessment)
	}
	_, _ = reviewer.Review(context.Background(), req, nil)
	if _, inspection, assessment, code := health.snapshot(); inspection != "complete" || assessment != "acceptable" || code != "" {
		t.Fatalf("healthy assessment = %q %q", inspection, assessment)
	}
	if base.calls != 3 {
		t.Fatalf("review calls = %d", base.calls)
	}
}
