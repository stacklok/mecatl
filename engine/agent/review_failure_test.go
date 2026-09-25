package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type hostileReviewFailure struct{}

func (hostileReviewFailure) Error() string { return "HOSTILE_ERROR_SENTINEL<<<UNTRUSTED" }
func (hostileReviewFailure) GuardrailReviewFailureCode() ReviewFailureCode {
	return ReviewFailureCode("HOSTILE_CODE_SENTINEL")
}

func TestReviewFailureProjectionIsClosedAndDoesNotLeakErrorText(t *testing.T) {
	err := hostileReviewFailure{}
	payload := reviewMachinePayload(nil, ToolReviewRequest{ReviewID: "r", Job: ReviewJobAction}, ToolReviewResult{}, err, "deny")
	if payload.ReasonCode != string(ReviewFailureProviderFailure) {
		t.Fatalf("reason code = %q", payload.ReasonCode)
	}
	concern := reviewFailureConcern(reviewFailureCode(err))
	if strings.Contains(payload.ReasonCode+concern, "HOSTILE") || strings.Contains(payload.ReasonCode+concern, err.Error()) {
		t.Fatalf("hostile error crossed projection boundary: payload=%+v concern=%q", payload, concern)
	}
}

func TestReviewFailureProjectionUsesTypedCode(t *testing.T) {
	err := reviewFailure(ReviewFailureMissingSubmit)
	payload := reviewMachinePayload(nil, ToolReviewRequest{ReviewID: "r", Job: ReviewJobInbound}, ToolReviewResult{}, err, "hold")
	if payload.ReasonCode != string(ReviewFailureMissingSubmit) || payload.Assessment != string(ReviewUnresolved) {
		t.Fatalf("payload = %+v", payload)
	}
	var classified GuardrailReviewFailure
	if !errors.As(err, &classified) || classified.GuardrailReviewFailureCode() != ReviewFailureMissingSubmit {
		t.Fatalf("classification = %v", classified)
	}
	_ = session.HookPayload{Guardrail: payload}
}

type terminalFailurePolicy struct {
	job ReviewJob
}

func (terminalFailurePolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (terminalFailurePolicy) Learn(session.SessionID, session.ToolCall) {}
func (p terminalFailurePolicy) GuardrailReviewPolicy(_ string, job ReviewJob, operationalFailure bool) (bool, bool) {
	return job == p.job, !operationalFailure
}

type terminalFailureReviewer struct {
	job      ReviewJob
	reviews  int
	recorded error
}

func (r *terminalFailureReviewer) Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error) {
	r.reviews++
	return ToolReviewResult{Assessment: ReviewAcceptable}, nil
}
func (r *terminalFailureReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, operationalFailure bool) (bool, bool) {
	return job == r.job, !operationalFailure
}
func (r *terminalFailureReviewer) RecordGuardrailReviewFailure(_ ToolReviewResult, err error) {
	r.recorded = err
}

type failingEvidencePreparer struct{ err error }

func (p failingEvidencePreparer) PrepareReviewEvidence(context.Context, ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	return PreparedReviewEvidence{}, p.err
}

func runTerminalEvidenceFailure(t *testing.T, job ReviewJob) ([]session.Event, int, *terminalFailureReviewer) {
	t.Helper()
	executions := 0
	catalog := tool.NewCatalog()
	catalog.MustRegister(revisionActionTool{executions: &executions})
	reviewer := &terminalFailureReviewer{job: job}
	engine := NewEngine(Deps{
		LLM:                    mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")),
		Catalog:                catalog,
		Policy:                 terminalFailurePolicy{job: job},
		ToolReviewer:           reviewer,
		ReviewEvidencePreparer: failingEvidencePreparer{err: errors.New("HOSTILE_EVIDENCE_SENTINEL")},
	})
	env := memEnv("/ws")
	sess := session.New("terminal-evidence", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	var events []session.Event
	for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
		events = append(events, ev)
	}
	return events, executions, reviewer
}

func TestActionEvidencePreparationFailureIsTerminalAndMachineSafe(t *testing.T) {
	events, executions, reviewer := runTerminalEvidenceFailure(t, ReviewJobAction)
	if executions != 0 || reviewer.reviews != 0 || reviewer.recorded == nil {
		t.Fatalf("executions=%d reviews=%d recorded=%v", executions, reviewer.reviews, reviewer.recorded)
	}
	assertEvidenceFailureEvent(t, events, governance.PhasePreToolUse)
}

func TestInboundEvidencePreparationFailureOverridesCheckerDownWarn(t *testing.T) {
	events, executions, reviewer := runTerminalEvidenceFailure(t, ReviewJobInbound)
	if executions != 1 || reviewer.reviews != 0 || reviewer.recorded == nil {
		t.Fatalf("executions=%d reviews=%d recorded=%v", executions, reviewer.reviews, reviewer.recorded)
	}
	assertEvidenceFailureEvent(t, events, governance.PhasePostToolUse)
	for _, ev := range events {
		if ev.ToolResult != nil && !ev.ToolResult.IsError && ev.ToolResult.Content == "executed" {
			t.Fatal("terminal inbound preparation failure released the original result")
		}
	}
}

func assertEvidenceFailureEvent(t *testing.T, events []session.Event, phase governance.HookPhase) {
	t.Helper()
	for _, ev := range events {
		if ev.Hook == nil || ev.Hook.Guardrail == nil || ev.Hook.Phase != string(phase) {
			continue
		}
		payload := ev.Hook.Guardrail
		if payload.ReasonCode != string(ReviewFailureEvidenceFailure) || payload.Assessment != string(ReviewUnresolved) {
			t.Fatalf("guardrail payload = %+v", payload)
		}
		if strings.Contains(payload.ReasonCode+payload.Assessment+payload.Disposition, "HOSTILE") {
			t.Fatalf("raw preparation error leaked: %+v", payload)
		}
		return
	}
	t.Fatal("missing guardrail failure event")
}

type consumerFailureReviewer struct {
	job ReviewJob
	err error
}

func (r consumerFailureReviewer) Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error) {
	return ToolReviewResult{Assessment: ReviewUnresolved}, r.err
}
func (r consumerFailureReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, _ bool) (bool, bool) {
	return job == r.job, true
}

func TestActionAndInboundConsumersProjectClosedFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ReviewFailureCode
	}{
		{name: "typed", err: reviewFailure(ReviewFailureMissingSubmit), want: ReviewFailureMissingSubmit},
		{name: "hostile_unknown", err: hostileReviewFailure{}, want: ReviewFailureProviderFailure},
	} {
		for _, job := range []ReviewJob{ReviewJobAction, ReviewJobInbound} {
			t.Run(tc.name+"_"+string(job), func(t *testing.T) {
				executions := 0
				catalog := tool.NewCatalog()
				catalog.MustRegister(revisionActionTool{executions: &executions})
				details := &rootContractSink{}
				engine := NewEngine(Deps{
					LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")),
					Catalog: catalog, Policy: terminalFailurePolicy{job: job},
					ToolReviewer:  consumerFailureReviewer{job: job, err: tc.err},
					ReviewDetails: details,
				})
				env := memEnv("/ws")
				sess := session.New("consumer-failure", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
				var events []session.Event
				for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
					events = append(events, ev)
				}
				wantExecutions := 0
				phase := governance.PhasePreToolUse
				if job == ReviewJobInbound {
					wantExecutions = 1
					phase = governance.PhasePostToolUse
				}
				if executions != wantExecutions {
					t.Fatalf("executions=%d, want %d", executions, wantExecutions)
				}
				assertFailureCodeEvent(t, events, phase, tc.want)
				if details.got.SessionID != sess.ID || details.got.ReviewID == "" || details.got.Concern != reviewFailureConcern(tc.want) {
					t.Fatalf("failure detail = %+v, want closed concern for %q", details.got, tc.want)
				}
				if strings.Contains(details.got.Concern+details.got.SourceDisplay+details.got.NextAction, "HOSTILE") {
					t.Fatalf("raw failure leaked through detail sink: %+v", details.got)
				}
			})
		}
	}
}

func assertFailureCodeEvent(t *testing.T, events []session.Event, phase governance.HookPhase, want ReviewFailureCode) {
	t.Helper()
	for _, ev := range events {
		if ev.Hook == nil || ev.Hook.Guardrail == nil || ev.Hook.Phase != string(phase) {
			continue
		}
		payload := ev.Hook.Guardrail
		if payload.ReasonCode != string(want) || strings.Contains(payload.ReasonCode+payload.Assessment+payload.Disposition, "HOSTILE") {
			t.Fatalf("guardrail payload = %+v, want code %q without raw data", payload, want)
		}
		return
	}
	t.Fatal("missing guardrail failure event")
}
