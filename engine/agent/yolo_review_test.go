package agent

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// heldFinalTurn delays the model's second call until release closes (or a
// safety timeout), so background reviews can publish before run end cancels them.
type heldFinalTurn struct {
	port.LLMProvider
	release chan struct{}
	calls   int
}

func (h *heldFinalTurn) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	h.calls++
	if h.calls == 2 {
		select {
		case <-h.release:
		case <-time.After(5 * time.Second):
		}
	}
	return h.LLMProvider.Stream(ctx, req)
}

// yoloReviewer reviews both jobs. optIn controls GuardrailYoloReviews; gate, when
// set, parks every Review until it is closed or the review context ends, and
// parked (when set) receives once per review that reached the gate.
type yoloReviewer struct {
	optIn  bool
	gate   chan struct{}
	parked chan struct{}
	result ToolReviewResult
	err    error

	mu        sync.Mutex
	reviews   int
	cancelled int
}

func (r *yoloReviewer) Review(ctx context.Context, _ ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, session.AuxiliaryUsage, error) {
	r.mu.Lock()
	r.reviews++
	r.mu.Unlock()
	if r.gate != nil {
		if r.parked != nil {
			r.parked <- struct{}{}
		}
		select {
		case <-r.gate:
		case <-ctx.Done():
			r.mu.Lock()
			r.cancelled++
			r.mu.Unlock()
			return ToolReviewResult{Assessment: ReviewUnresolved}, session.AuxiliaryUsage{}, ctx.Err()
		}
	}
	usage := session.Usage{InputTokens: 5, OutputTokens: 1}
	return r.result, session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindGuardrail: {Total: usage, Models: map[string]session.Usage{"checker/model": usage}},
	}}, r.err
}

func (*yoloReviewer) GuardrailReviewPolicy(string, ReviewJob, bool) (bool, bool) { return true, true }
func (r *yoloReviewer) GuardrailYoloReviews() bool                               { return r.optIn }

func (r *yoloReviewer) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reviews, r.cancelled
}

// runYoloReview drives one Act call in a yolo session. When hooks > 0 the final
// model turn waits until that many guardrail events have been delivered.
func runYoloReview(t *testing.T, reviewer *yoloReviewer, preparer ReviewEvidencePreparer, hooks int, onEvent func(session.Event)) ([]session.Event, int, *session.Session) {
	t.Helper()
	executions := 0
	catalog := tool.NewCatalog()
	catalog.MustRegister(revisionActionTool{executions: &executions})
	llm := &heldFinalTurn{LLMProvider: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Act", []byte(`{}`))), mockllm.TextTurn("done")), release: make(chan struct{})}
	switch {
	case reviewer.parked != nil:
		// Let the run end only once both reviews are parked in the checker.
		go func() {
			<-reviewer.parked
			<-reviewer.parked
			close(llm.release)
		}()
	case hooks == 0:
		close(llm.release)
	}
	engine := NewEngine(Deps{
		LLM:                    llm,
		Catalog:                catalog,
		Policy:                 terminalFailurePolicy{job: ReviewJobAction},
		ToolReviewer:           reviewer,
		ReviewEvidencePreparer: preparer,
	})
	env := memEnv("/ws")
	sess := session.New("yolo-review", session.ModeYolo, env.Ref(), session.Limits{}, time.Unix(0, 0))
	var events []session.Event
	seen := 0
	for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "act"}).Events() {
		events = append(events, ev)
		if ev.Hook != nil && ev.Hook.Guardrail != nil {
			if seen++; seen == hooks {
				close(llm.release)
			}
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	return events, executions, sess
}

func guardrailHooks(events []session.Event) []session.Event {
	var hooks []session.Event
	for _, ev := range events {
		if ev.Hook != nil && ev.Hook.Guardrail != nil {
			hooks = append(hooks, ev)
		}
	}
	return hooks
}

func TestYoloSkipsGuardrailReviewWithoutOptIn(t *testing.T) {
	reviewer := &yoloReviewer{result: ToolReviewResult{Assessment: ReviewProhibited}}
	events, executions, _ := runYoloReview(t, reviewer, nil, 0, nil)
	if reviews, _ := reviewer.counts(); reviews != 0 {
		t.Fatalf("reviews = %d, want 0: yolo without opt-in must not call the checker", reviews)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
	if hooks := guardrailHooks(events); len(hooks) != 0 {
		t.Fatalf("guardrail events = %+v, want none", hooks)
	}
}

func TestYoloOptInReviewsRunInBackgroundWithoutDelayingTheTool(t *testing.T) {
	gate := make(chan struct{})
	reviewer := &yoloReviewer{optIn: true, gate: gate, result: ToolReviewResult{Assessment: ReviewProhibited}}
	var once sync.Once
	// Safety release: a blocking review would otherwise hang the run; releasing it
	// late turns that regression into the ordering failure asserted below.
	safety := time.AfterFunc(5*time.Second, func() { once.Do(func() { close(gate) }) })
	defer safety.Stop()
	events, executions, sess := runYoloReview(t, reviewer, nil, 2, func(ev session.Event) {
		// The checker is still parked when the canonical result arrives; only then
		// is it released, so a blocking review would deadlock this run.
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "call" {
			once.Do(func() { close(gate) })
		}
	})
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
	resultAt, terminalAt := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "call" {
			if ev.ToolResult.IsError || ev.ToolResult.Content != "executed" {
				t.Fatalf("tool result = %+v, want the released original", ev.ToolResult)
			}
			resultAt = i
		}
		if ev.Type == session.EvResult {
			terminalAt = i
		}
	}
	if resultAt < 0 || terminalAt < 0 {
		t.Fatalf("missing tool result (%d) or terminal event (%d)", resultAt, terminalAt)
	}
	phases := map[string]string{}
	for i, ev := range events {
		if ev.Hook == nil || ev.Hook.Guardrail == nil {
			continue
		}
		if i < resultAt || i > terminalAt {
			t.Fatalf("guardrail event at %d, want between tool result %d and terminal %d", i, resultAt, terminalAt)
		}
		phases[ev.Hook.Phase] = ev.Hook.Guardrail.Disposition
	}
	if phases[string(governance.PhasePreToolUse)] != "pass_advisory" || phases[string(governance.PhasePostToolUse)] != "pass_advisory" {
		t.Fatalf("advisory dispositions = %v, want pass_advisory for both phases", phases)
	}
	if reviews, _ := reviewer.counts(); reviews != 2 {
		t.Fatalf("reviews = %d, want action and inbound", reviews)
	}
	if got := sess.TokenUsageSnapshot()[session.UsageKindGuardrail].Total; got.InputTokens != 10 {
		t.Fatalf("guardrail usage = %+v, want both background reviews recorded", got)
	}
}

func TestYoloOptInTerminalEvidenceFailureNeverFailsClosed(t *testing.T) {
	reviewer := &yoloReviewer{optIn: true, result: ToolReviewResult{Assessment: ReviewAcceptable}}
	events, executions, _ := runYoloReview(t, reviewer, failingEvidencePreparer{err: errors.New("evidence unavailable")}, 2, nil)
	if executions != 1 {
		t.Fatalf("executions = %d, want 1: yolo must not fail closed", executions)
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "call" && (ev.ToolResult.IsError || ev.ToolResult.Content != "executed") {
			t.Fatalf("tool result = %+v, want the released original", ev.ToolResult)
		}
		if ev.Type == session.EvPermissionAsk {
			t.Fatal("yolo must not surface a guardrail ask")
		}
	}
	hooks := guardrailHooks(events)
	if len(hooks) != 2 {
		t.Fatalf("guardrail events = %d, want an advisory failure per phase", len(hooks))
	}
	for _, ev := range hooks {
		if ev.Hook.Guardrail.ReasonCode != string(ReviewFailureEvidenceFailure) || ev.Hook.Guardrail.Disposition != "pass_advisory" {
			t.Fatalf("guardrail payload = %+v, want advisory evidence failure", ev.Hook.Guardrail)
		}
	}
}

func TestYoloBackgroundReviewsAreCancelledAtRunEnd(t *testing.T) {
	reviewer := &yoloReviewer{optIn: true, gate: make(chan struct{}), parked: make(chan struct{}, 2), result: ToolReviewResult{Assessment: ReviewAcceptable}}
	start := time.Now()
	events, executions, _ := runYoloReview(t, reviewer, nil, 0, nil)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("run took %s, want run end to cancel parked reviews at once", elapsed)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
	if reviews, cancelled := reviewer.counts(); reviews != 2 || cancelled != 2 {
		t.Fatalf("reviews=%d cancelled=%d, want both parked reviews cancelled at run end", reviews, cancelled)
	}
	if hooks := guardrailHooks(events); len(hooks) != 0 {
		t.Fatalf("guardrail events = %+v, want none from cancelled reviews", hooks)
	}
}

func TestBackgroundReviewsCapConcurrency(t *testing.T) {
	var b backgroundReviews
	release := make(chan struct{})
	for i := range maxBackgroundReviews {
		if _, ok := b.reserve(context.Background()); !ok {
			t.Fatalf("reserve %d refused below the cap", i)
		}
		b.start(func() { <-release })
	}
	if _, ok := b.reserve(context.Background()); ok {
		t.Fatal("reserve succeeded above maxBackgroundReviews")
	}
	close(release)
	b.wg.Wait()
	if _, ok := b.reserve(context.Background()); !ok {
		t.Fatal("finished reviews did not release their slots")
	}
	if b.start(func() {}); !b.drain() {
		t.Fatal("drain did not join the last review")
	}
}

type snapshotRecordingPreparer struct {
	mu        sync.Mutex
	snapshots []bool
}

func (p *snapshotRecordingPreparer) PrepareReviewEvidence(_ context.Context, prep ReviewEvidencePreparation) (PreparedReviewEvidence, error) {
	p.mu.Lock()
	p.snapshots = append(p.snapshots, prep.Snapshot)
	p.mu.Unlock()
	return PreparedReviewEvidence{Complete: true}, nil
}

func TestYoloBackgroundReviewsSnapshotEvidence(t *testing.T) {
	reviewer := &yoloReviewer{optIn: true, result: ToolReviewResult{Assessment: ReviewProhibited}}
	preparer := &snapshotRecordingPreparer{}
	events, _, _ := runYoloReview(t, reviewer, preparer, 2, nil)
	if len(guardrailHooks(events)) != 2 {
		t.Fatalf("guardrail events = %d, want both advisories", len(guardrailHooks(events)))
	}
	preparer.mu.Lock()
	defer preparer.mu.Unlock()
	if len(preparer.snapshots) != 2 || !preparer.snapshots[0] || !preparer.snapshots[1] {
		t.Fatalf("snapshot flags = %v, want both background preparations to snapshot", preparer.snapshots)
	}
}
