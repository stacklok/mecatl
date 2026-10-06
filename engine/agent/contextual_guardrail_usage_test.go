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
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type guardrailUsageReviewer struct {
	mu         sync.Mutex
	calls      []ReviewJob
	usage      session.AuxiliaryUsage
	usageFor   func(ToolReviewRequest) session.AuxiliaryUsage
	assessment ReviewAssessment
	err        error
	entered    chan struct{}
	release    <-chan struct{}
	releaseFor func(ToolReviewRequest) <-chan struct{}
	after      func(ToolReviewRequest)
}

func (r *guardrailUsageReviewer) Review(_ context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req.Job)
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	release := r.release
	if r.releaseFor != nil {
		release = r.releaseFor(req)
	}
	if release != nil {
		<-release
	}
	assessment := r.assessment
	if assessment == "" {
		assessment = ReviewAcceptable
	}
	usage := r.usage
	if r.usageFor != nil {
		usage = r.usageFor(req)
	}
	if r.after != nil {
		r.after(req)
	}
	return ToolReviewResult{Assessment: assessment}, usage, r.err
}

func (*guardrailUsageReviewer) GuardrailReviewPolicy(_ string, job ReviewJob, _ bool) (bool, bool) {
	return job == ReviewJobAction || job == ReviewJobInbound, true
}

type guardrailUsagePolicy struct{}

func (guardrailUsagePolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) port.PermissionResult {
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: governance.Allow}}
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
	callChunks := make([]port.Chunk, 0, len(calls)+2)
	for _, call := range calls {
		callChunks = append(callChunks, mockllm.ToolCallChunk(call))
	}
	callChunks = append(callChunks, mockllm.UsageChunk(session.Usage{InputTokens: 11, OutputTokens: 2}), mockllm.DoneChunk(session.StopEndTurn))
	eng := NewEngine(Deps{LLM: mockllm.New(
		mockllm.ChunksTurn(callChunks...),
		mockllm.ChunksTurn(mockllm.TextChunk("done"), mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 3}), mockllm.DoneChunk(session.StopEndTurn)),
	), Catalog: cat, Policy: guardrailUsagePolicy{}, ToolReviewer: reviewer})
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
	if got := sess.UsageFor(session.UsageKindMain); got != (session.Usage{InputTokens: 18, OutputTokens: 5}) {
		t.Fatalf("main usage = %+v, want only the two main model calls", got)
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
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstRelease) })
	releaseSecond := sync.OnceFunc(func() { close(secondRelease) })
	t.Cleanup(func() { releaseFirst(); releaseSecond(); cancel() })
	deadline := time.After(3 * time.Second)
	secondReturned := make(chan struct{})
	reviewer := &guardrailUsageReviewer{
		assessment: ReviewProhibited,
		entered:    make(chan struct{}, 2),
		usageFor: func(req ToolReviewRequest) session.AuxiliaryUsage {
			if req.Event.CallID == "first" {
				return testGuardrailUsage(2)
			}
			return testGuardrailUsage(7)
		},
		releaseFor: func(req ToolReviewRequest) <-chan struct{} {
			if req.Event.CallID == "first" {
				return firstRelease
			}
			return secondRelease
		},
		after: func(req ToolReviewRequest) {
			if req.Event.CallID == "second" {
				close(secondReturned)
			}
		},
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
	usageAtFirstAsk := make(chan int, 1)
	go func() {
		defer close(done)
		for ev := range eng.Run(ctx, sess, env, RunRequest{Text: "inspect"}).Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Guardrail != nil {
				usageAtFirstAsk <- sess.UsageFor(session.UsageKindGuardrail).InputTokens
				cancel()
			}
		}
	}()
	// Complete the second assessment first. Neither worker may mutate the
	// session before the dispatcher joins and drains records in call order.
	for range 2 {
		select {
		case <-reviewer.entered:
		case <-deadline:
			t.Fatal("parallel reviews did not both start")
		}
	}
	releaseSecond()
	select {
	case <-secondReturned:
	case <-deadline:
		t.Fatal("second review did not complete first")
	}
	earlyUsage := sess.UsageFor(session.UsageKindGuardrail)
	releaseFirst()
	select {
	case <-done:
	case <-deadline:
		t.Fatal("cancelled read batch did not finish")
	}
	if earlyUsage != (session.Usage{}) {
		t.Fatalf("out-of-order worker mutated session before drain: %+v", earlyUsage)
	}
	select {
	case got := <-usageAtFirstAsk:
		if got != 2 {
			t.Fatalf("guardrail usage at first ordered ask = %d, want first call's two tokens", got)
		}
	case <-deadline:
		t.Fatal("first ordered guardrail ask was not emitted")
	}
	if got := sess.UsageFor(session.UsageKindGuardrail); got.InputTokens != 9 {
		t.Fatalf("completed read-batch cancellation usage = %d, want 2+7 once", got.InputTokens)
	}
}

func TestContextualGuardrailUsage_Scenario2_SerialCancellationDrainsCompletedUsage(t *testing.T) {
	runReady := make(chan *Run, 1)
	reviewer := &guardrailUsageReviewer{
		usageFor: func(req ToolReviewRequest) session.AuxiliaryUsage {
			if req.Job == ReviewJobInbound {
				return testGuardrailUsage(11)
			}
			return testGuardrailUsage(3)
		},
		after: func(req ToolReviewRequest) {
			if req.Job == ReviewJobInbound {
				(<-runReady).Cancel() // completed serial review races requested cancellation
			}
		},
	}
	cat := tool.NewCatalog()
	cat.MustRegister(guardrailUsageTool{name: "Write", readOnly: false})
	eng := NewEngine(Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("write", "Write", []byte(`{}`))), mockllm.TextTurn("done")),
		Catalog: cat, Policy: guardrailUsagePolicy{}, ToolReviewer: reviewer,
	})
	env := memEnv("/ws")
	sess := session.New("serial-cancel", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	run := eng.Run(t.Context(), sess, env, RunRequest{Text: "write"})
	runReady <- run
	var sawCancelled bool
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatal("serial review did not trigger requested cancellation")
	}
	if got := sess.UsageFor(session.UsageKindGuardrail).InputTokens; got != 14 {
		t.Fatalf("completed serial action/inbound usage on cancellation = %d, want 3+11 once", got)
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
	r.recordGuardrailUsageWhileActive(t.Context(), sess, testGuardrailUsage(7))
	r.recordGuardrailUsageWhileActive(t.Context(), sess, testGuardrailUsage(7))
	if got := sess.UsageFor(session.UsageKindGuardrail); got != (session.Usage{}) {
		t.Fatalf("late usage persisted after run end: %+v", got)
	}
	if got := diag.count("late auxiliary usage dropped after parent run ended"); got != 1 {
		t.Fatalf("late-drop diagnostics = %d, want one bounded line", got)
	}
}

type permissionUsagePolicy struct{ calls int }

func (p *permissionUsagePolicy) Evaluate(_ context.Context, _ session.SessionID, _ session.PermissionMode, call session.ToolCall, _ tool.WorkspaceReader) port.PermissionResult {
	p.calls++
	effect := governance.Ask
	if call.Name == shellSystemTempToolName {
		effect = governance.Allow
	}
	return port.PermissionResult{Decision: governance.PermissionDecision{Effect: effect}, Usage: testGuardrailUsage(1)}
}
func (*permissionUsagePolicy) Learn(session.SessionID, session.ToolCall) {}

func TestPermissionEvaluationRecordsEveryResultBeforeDecision(t *testing.T) {
	policy := &permissionUsagePolicy{}
	env := memEnv("/ws")
	eng := NewEngine(Deps{Policy: policy})
	sess := session.New("permission-usage", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	call := session.NewToolCall("shell", tool.ShellToolName, []byte(`{"command":"pwd","temp_scope":"system"}`))
	run := &Run{diag: port.NopDiagnostics{}, auxiliaryUsageActive: true}
	for i := 1; i <= 2; i++ {
		if d := eng.permissionDecision(t.Context(), run, sess, env, call); d.Effect != governance.Ask {
			t.Fatalf("evaluation %d effect = %s", i, d.Effect)
		}
		if policy.calls != i*2 || sess.UsageFor(session.UsageKindGuardrail).InputTokens != i*2 {
			t.Fatalf("evaluation %d: calls=%d usage=%+v", i, policy.calls, sess.UsageFor(session.UsageKindGuardrail))
		}
	}
	run.revokeAuxiliaryUsageOwnership()
	_ = eng.permissionDecision(t.Context(), run, sess, env, call)
	if got := sess.UsageFor(session.UsageKindGuardrail).InputTokens; got != 4 {
		t.Fatalf("late usage = %d, want 4", got)
	}
	_ = eng.permissionDecision(t.Context(), nil, sess, env, call)
	if got := sess.UsageFor(session.UsageKindGuardrail).InputTokens; got != 4 {
		t.Fatalf("nil-run usage = %d, want 4", got)
	}
}
