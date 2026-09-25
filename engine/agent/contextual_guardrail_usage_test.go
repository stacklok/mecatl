package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type guardrailUsageReviewer struct {
	mu         sync.Mutex
	calls      []ReviewJob
	usage      session.AuxiliaryUsage
	assessment ReviewAssessment
	err        error
	entered    chan struct{}
	release    <-chan struct{}
}

func (r *guardrailUsageReviewer) Review(_ context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req.Job)
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	assessment := r.assessment
	if assessment == "" {
		assessment = ReviewAcceptable
	}
	return ToolReviewResult{Assessment: assessment}, r.usage, r.err
}

func (*guardrailUsageReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, _ bool) (bool, bool) {
	return job == ReviewJobAction || job == ReviewJobInbound, true
}

type guardrailUsagePolicy struct{}

func (guardrailUsagePolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (guardrailUsagePolicy) Learn(session.SessionID, session.ToolCall) {}

type guardrailUsageTool struct {
	name     string
	readOnly bool
}

func (t guardrailUsageTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (t guardrailUsageTool) ReadOnly() bool { return t.readOnly }
func (guardrailUsageTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

func testGuardrailUsage(tokens int) session.AuxiliaryUsage {
	u := session.Usage{InputTokens: tokens}
	return session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindRouter: {Total: u, Models: map[string]session.Usage{"provider/guardrail-model": u}},
	}}
}

func runGuardrailUsageSession(t *testing.T, reviewer ToolReviewer, calls ...session.ToolCall) *session.Session {
	t.Helper()
	cat := tool.NewCatalog()
	for _, call := range calls {
		cat.MustRegister(guardrailUsageTool{name: call.Name, readOnly: true})
	}
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...), mockllm.TextTurn("done")), Catalog: cat, Policy: guardrailUsagePolicy{}, ToolReviewer: reviewer})
	env := memEnv("/ws")
	sess := session.New("guardrail-usage", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	for range eng.Run(t.Context(), sess, env, RunRequest{Text: "inspect"}).Events() {
	}
	return sess
}

func TestContextualGuardrailUsage_Scenario1_ReviewsRecordOnReviewedSession(t *testing.T) {
	reviewer := &guardrailUsageReviewer{usage: testGuardrailUsage(3)}
	sess := runGuardrailUsageSession(t, reviewer, session.NewToolCall("read", "Read", []byte(`{"path":"a"}`)))
	if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 6 {
		t.Fatalf("guardrail input tokens = %d, want action + inbound = 6", got.InputTokens)
	}
	if got := sess.TokenUsageSnapshot()[session.UsageKindGuardrail].Models["provider/guardrail-model"]; got.InputTokens != 6 {
		t.Fatalf("guardrail model usage = %+v, want action + inbound attributed to provider/guardrail-model", got)
	}
	if got := sess.UsageFor(session.UsageKindRouter); got != (session.Usage{}) {
		t.Fatalf("reviewer usage leaked into router: %+v", got)
	}
}

func TestContextualGuardrailUsage_Scenario1_RetryAndTerminalUsageCountedOnce(t *testing.T) {
	reviewer := &guardrailUsageReviewer{usage: testGuardrailUsage(2), err: errors.New("partial review")}
	sess := runGuardrailUsageSession(t, reviewer, session.NewToolCall("read", "Read", []byte(`{"path":"a"}`)))
	if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 2 {
		t.Fatalf("terminal partial usage = %d, want exactly once", got.InputTokens)
	}
	zero := runGuardrailUsageSession(t, &guardrailUsageReviewer{}, session.NewToolCall("zero", "Read", []byte(`{"path":"a"}`)))
	if got := zero.UsageFor(session.UsageKindGuardrail); got != (session.Usage{}) {
		t.Fatalf("zero-usage reviewer fabricated usage: %+v", got)
	}
}

func TestContextualGuardrailUsage_Scenario1_ComposedWorkerRecordsOwnUsage(t *testing.T) {
	reviewer := &guardrailUsageReviewer{usage: testGuardrailUsage(4)}
	root := newReviewRoot(reviewer, nil, nil)
	root.establishInstructions(nil)
	root.refreshTasks([]session.Message{{Role: session.RoleUser, Text: "root task", UserPromptProvenance: session.UserPromptProvenancePrincipal}})
	cat := tool.NewCatalog()
	cat.MustRegister(guardrailUsageTool{name: "Read", readOnly: true})
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"path":"a"}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: guardrailUsagePolicy{}, Role: "subagent"})
	env := memEnv("/ws")
	worker := session.New("worker", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	for range eng.Run(t.Context(), worker, env, RunRequest{Text: "inspect", reviewRoot: root, reviewIsolated: true}).Events() {
	}
	if got := worker.UsageFor(session.UsageKindGuardrail); got.InputTokens != 8 {
		t.Fatalf("worker guardrail usage = %d, want 8", got.InputTokens)
	}
}

func TestContextualGuardrailUsage_Scenario2_OrderedDrainRecordsCompletedReviews(t *testing.T) {
	reviewer := &guardrailUsageReviewer{usage: testGuardrailUsage(1)}
	sess := runGuardrailUsageSession(t, reviewer,
		session.NewToolCall("first", "Read", []byte(`{"path":"a"}`)),
		session.NewToolCall("second", "Grep", []byte(`{"path":"a","pattern":"x"}`)))
	if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 4 {
		t.Fatalf("ordered action/inbound drain usage = %d, want 4", got.InputTokens)
	}
}

func TestContextualGuardrailUsage_Scenario2_CancellationDrainsCompletedUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	releaseReviews := make(chan struct{})
	reviewer := &guardrailUsageReviewer{
		usage: testGuardrailUsage(5), assessment: ReviewProhibited,
		entered: make(chan struct{}, 2), release: releaseReviews,
	}
	cat := tool.NewCatalog()
	cat.MustRegister(guardrailUsageTool{name: "Read", readOnly: true})
	cat.MustRegister(guardrailUsageTool{name: "Grep", readOnly: true})
	calls := []session.ToolCall{
		session.NewToolCall("first", "Read", []byte(`{"path":"a"}`)),
		session.NewToolCall("second", "Grep", []byte(`{"path":"a","pattern":"x"}`)),
	}
	eng := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(calls...)), Catalog: cat, Policy: guardrailUsagePolicy{}, ToolReviewer: reviewer, Interactive: true})
	env := memEnv("/ws")
	sess := session.New("cancelled", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range eng.Run(ctx, sess, env, RunRequest{Text: "inspect"}).Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Guardrail != nil {
				cancel()
			}
		}
	}()
	// The action assessments fan out before ordered resolution. Hold them until both
	// have entered, so cancellation of the first ordered ask must drain both usages.
	<-reviewer.entered
	<-reviewer.entered
	close(releaseReviews)
	<-done
	if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 10 {
		t.Fatalf("completed read-batch cancellation usage = %d, want both action reviews once", got.InputTokens)
	}
}

func TestContextualGuardrailUsage_Scenario2_LateUsageIsDropped(t *testing.T) {
	diag := &auxiliaryRecordingDiagnostics{}
	eng := NewEngine(Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Diagnostics: diag})
	sess := session.New("late", session.ModeDefault, memEnv("/ws").Ref(), session.Limits{}, time.Unix(0, 0))
	runReady := make(chan *Run, 1)
	releaseRun := make(chan struct{})
	prepared := eng.prepareRun(t.Context(), sess, RunRequest{}, session.Usage{}, func(_ context.Context, r *Run) {
		runReady <- r
		<-releaseRun
	})
	run, transition := prepared.Start()
	if transition != PreparedRunStarted {
		t.Fatalf("Start transition = %q", transition)
	}
	r := <-runReady
	close(releaseRun)
	for range run.Events() {
	}

	// The callback may outlive the run, but its ownership fence must prevent both
	// ledger mutation and unbounded diagnostics after the lifecycle has closed.
	r.recordAuxiliaryUsageWhileActive(t.Context(), sess, session.UsageKindGuardrail, testGuardrailUsage(7))
	r.recordAuxiliaryUsageWhileActive(t.Context(), sess, session.UsageKindGuardrail, testGuardrailUsage(7))
	if got := sess.UsageFor(session.UsageKindGuardrail); got != (session.Usage{}) {
		t.Fatalf("late usage persisted after ownership loss: %+v", got)
	}
	if got := diag.count("late auxiliary usage dropped after parent run ended"); got != 1 {
		t.Fatalf("late-drop diagnostics = %d, want one bounded line", got)
	}
}
