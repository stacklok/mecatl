package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// planApprovalService builds a Service configured for plan-approval testing:
// Interactive:true (so PresentPlan surfaces a plan ask), PresentPlan registered
// in the catalog, and a mockllm scripted with the given turns.
func planApprovalService(t *testing.T, llm *mockllm.Provider, rules []governance.Rule) *server.Service {
	t.Helper()
	store := memstore.New()
	cat := tool.NewCatalog()
	cat.MustRegister(agent.NewPresentPlanTool())
	engine := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     cat,
		Policy:      permpolicy.NewPolicy(rules, nil),
		Model:       "test-model",
		Interactive: true,
		Store:       store, // auto-save terminals so the resumed run's StateCompleted persists for the continuation
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// parkPlanAsk starts a plan-approval run that calls PresentPlan and returns the
// AskID when the plan ask surfaces. It does NOT cancel the run (the run stays alive
// blocked on the ask) but DOES deregister it via FinishRun so ApprovePlan's
// LookupRun check passes. The returned cleanup function cancels the parked run and
// waits for its goroutine to finish — call it after ApprovePlan has resolved the
// session (the cancelled run will auto-save StateCancelled, but by then the test's
// assertions on the persisted session are already complete).
func parkPlanAsk(t *testing.T, svc *server.Service, sessID session.SessionID) (askID string, cleanup func()) {
	t.Helper()
	r, err := svc.StartRunContent(context.Background(), sessID, "plan a task", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	askCh := make(chan string, 1)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for ev := range r.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin() == session.AskOriginPlan {
				svc.Persist(context.Background(), sessID)
				// Persist then deregister: the store has StateAwaiting, and the
				// in-flight registry no longer holds this run, so ApprovePlan's
				// LookupRun returns false.
				svc.FinishRun(sessID, r)
				select {
				case askCh <- ev.Ask.AskID:
				default:
				}
				// Now the goroutine blocks on the ask (r.Events() blocks until a
				// verdict arrives or the run is cancelled). The caller will cancel
				// the run via cleanup after ApprovePlan finishes.
			}
		}
	}()
	select {
	case a := <-askCh:
		cleanup := func() {
			r.Cancel()
			select {
			case <-drainDone:
			case <-time.After(5 * time.Second):
				// FIX 6 (flake): a timeout here means Cancel didn't unblock the
				// parked-ask drain goroutine — a wedge. Make it a LOUD test
				// failure, not a silent no-op, so a stuck goroutine is caught.
				t.Fatalf("parkPlanAsk: drain goroutine did not finish within 5s of Cancel (wedged)")
			}
			svc.FinishRun(sessID, r)
		}
		var once sync.Once
		finish := func() { once.Do(cleanup) }
		t.Cleanup(finish)
		return a, finish
	case <-drainDone:
		t.Fatal("the run drained without surfacing a plan-approval EvPermissionAsk")
		return "", nil
	}
}

func awaitLivePlanAsk(t *testing.T, r *agent.Run, wantCall session.ToolCallID, wantPlan string) (string, []session.Event) {
	t.Helper()
	var seen []session.Event
	for ev := range r.Events() {
		seen = append(seen, ev)
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil || ev.Ask.Origin() != session.AskOriginPlan {
			continue
		}
		ask := ev.Ask
		if ask.Tool != "PresentPlan" || ask.Call != wantCall || ask.Origin() != session.AskOriginPlan {
			t.Fatalf("fresh plan ask = %+v, want Tool=PresentPlan Call=%q Origin=AskOriginPlan", ask, wantCall)
		}
		var args struct {
			Plan string `json:"plan"`
		}
		if err := json.Unmarshal(ask.Args, &args); err != nil {
			t.Fatalf("decode fresh plan ask args %q: %v", ask.Args, err)
		}
		if args.Plan != wantPlan {
			t.Fatalf("fresh plan ask plan = %q, want %q", args.Plan, wantPlan)
		}
		return ask.AskID, seen
	}
	t.Fatal("the run drained without surfacing a plan-approval EvPermissionAsk")
	return "", nil
}

func cleanupStartedRun(t *testing.T, svc *server.Service, sessID session.SessionID, r *agent.Run) func() {
	t.Helper()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			r.Cancel()
			for range r.Events() {
			}
			svc.FinishRun(sessID, r)
		})
	}
	finish := func() {
		once.Do(func() {
			svc.FinishRun(sessID, r)
		})
	}
	t.Cleanup(cleanup)
	return finish
}

// drainApprovedEvents drains a Service-returned event channel (from ApprovePlan)
// into a slice, recording the terminal StopReason.
func drainApprovedEvents(t *testing.T, events <-chan session.Event) []session.Event {
	t.Helper()
	var out []session.Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

// hasResultWithStop reports whether evs contains an EvResult with the given stop reason.
func hasResultWithStop(evs []session.Event, stop session.StopReason) bool {
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == stop {
			return true
		}
	}
	return false
}

// indexOfStopEvent returns the index of the FIRST EvResult carrying the given
// stop reason in evs, or -1 if none. Used by the plan-approval tests to assert
// the resumed run's StopPlanApproved precedes the continuation run's
// StopEndTurn in the merged event stream (FIX 5 ordering).
func indexOfStopEvent(evs []session.Event, stop session.StopReason) int {
	for i, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == stop {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// ApprovePlan service tests
// ---------------------------------------------------------------------------

// TestApprovePlanAllowOnceFlipsModeAndRunsExecution is the headline test: the
// service parks a plan ask, ApprovePlan(ModeDefault) resolves it, and the merged
// stream carries a StopPlanApproved result for the resumed run AND a StopEndTurn
// result for the continuation execution run. The session mode flips to ModeDefault.
func TestApprovePlanAllowOnceFlipsModeAndRunsExecution(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("all tasks executed successfully"),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	askID, finishParked := parkPlanAsk(t, svc, sess.ID)
	defer finishParked()

	events, err := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, "")
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	evs := drainApprovedEvents(t, events)

	// The merged stream must contain the resumed run's terminal (StopPlanApproved)
	// and the continuation run's terminal (StopEndTurn). FIX 5 (ordering): the
	// resumed run's StopPlanApproved MUST precede the continuation run's
	// StopEndTurn — the plan-approval run terminates BEFORE the continuation
	// execution run starts (not just two independent booleans).
	idxPlanApproved := indexOfStopEvent(evs, session.StopPlanApproved)
	idxEndTurn := indexOfStopEvent(evs, session.StopEndTurn)
	if idxPlanApproved < 0 {
		t.Fatal("merged stream must contain a StopPlanApproved result from the resumed plan-approval run")
	}
	if idxEndTurn < 0 {
		t.Fatal("merged stream must contain a StopEndTurn result from the continuation execution run")
	}
	if idxPlanApproved >= idxEndTurn {
		t.Fatalf("StopPlanApproved (idx %d) must PRECEDE StopEndTurn (idx %d) in the merged stream", idxPlanApproved, idxEndTurn)
	}

	// The session mode must have flipped to default.
	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("mode after ApprovePlan(AllowOnce) = %q, want %q", sess.Mode, session.ModeDefault)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("state after ApprovePlan = %q, want completed", sess.State)
	}

	// The continuation run's first user message must be PlanApprovedProceedText.
	foundProceed := false
	for _, msg := range sess.Conversation.Messages {
		if msg.Role == session.RoleUser && strings.Contains(msg.Text, agent.PlanApprovedProceedText) {
			foundProceed = true
			break
		}
	}
	if !foundProceed {
		t.Fatal("continuation run's first user message must contain PlanApprovedProceedText")
	}

	// Verify askID is present in the events (the ask was resolved).
	if askID == "" {
		t.Fatal("askID must not be empty")
	}
}

// TestApprovePlanAllowAlwaysFlipsToAcceptEdits asserts that ApprovePlan(ModeAccept)
// flips the session mode to ModeAccept (accept-edits).
func TestApprovePlanAllowAlwaysFlipsToAcceptEdits(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"x"}`)),
		mockllm.TextTurn("done"),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, finishParked := parkPlanAsk(t, svc, sess.ID)
	defer finishParked()

	events, err := svc.ApprovePlan(context.Background(), sess.ID, session.ModeAccept, "")
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	_ = drainApprovedEvents(t, events)

	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModeAccept {
		t.Fatalf("mode after ApprovePlan(ModeAccept) = %q, want %q", sess.Mode, session.ModeAccept)
	}
}

// TestApprovePlanDenyIterates asserts that ApprovePlan(ModePlan) keeps the session
// in plan mode, does NOT start a continuation run, and the resumed run terminates
// CLEANLY with StopPlanIterate (issue #206 iterate UX fix: the run ENDS so the
// operator's next typed prompt drives the revision — the model does NOT continue
// iterating with no operator input). A follow-up StartRunContent then drives the
// revision on the operator's typed feedback (the operator-types-feedback path).
func TestApprovePlanDenyIterates(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"original"}`)),
		// Reached only after the operator supplies feedback in a new run.
		mockllm.ToolCallTurn(call("c2", "PresentPlan", `{"plan":"revised"}`)),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	firstAskID, finishParked := parkPlanAsk(t, svc, sess.ID)
	defer finishParked()

	events, err := svc.ApprovePlan(context.Background(), sess.ID, session.ModePlan, "")
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	evs := drainApprovedEvents(t, events)

	// Session must stay in ModePlan (no flip, no continuation run).
	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModePlan {
		t.Fatalf("mode after ApprovePlan(deny) = %q, want %q (no flip)", sess.Mode, session.ModePlan)
	}

	// The resumed run must terminate with StopPlanIterate (the iterate pause), NOT
	// StopPlanApproved (no approval happened) and NOT StopEndTurn (the model did NOT
	// continue iterating in-turn — the run ends so the operator types feedback).
	if !hasResultWithStop(evs, session.StopPlanIterate) {
		t.Fatalf("deny path must emit StopPlanIterate (pause for operator feedback); stops seen = %v", stopReasonsOf(evs))
	}
	if hasResultWithStop(evs, session.StopPlanApproved) {
		t.Fatal("deny path must NOT emit StopPlanApproved — no approval happened")
	}
	if hasResultWithStop(evs, session.StopEndTurn) {
		t.Fatal("deny path must NOT emit StopEndTurn — the model must NOT continue iterating in-turn (the run pauses for operator feedback)")
	}

	// A deny tool result for "c1" (PresentPlan) must be present in the session
	// conversation, teaching the model the plan was not approved.
	foundDeny := false
	for _, msg := range sess.Conversation.Messages {
		if msg.Role == session.RoleTool && msg.ToolResult != nil &&
			msg.ToolResult.CallID == "c1" &&
			msg.ToolResult.IsError && strings.Contains(msg.ToolResult.Content, "denied") {
			foundDeny = true
			break
		}
	}
	if !foundDeny {
		t.Fatal("deny must have recorded an error tool result for the PresentPlan call")
	}

	// The deny-path run must reach a CLEAN terminal (StateCompleted), not wedge or
	// end in an error/cancelled state.
	if sess.State != session.StateCompleted {
		t.Fatalf("deny-path session state = %q, want %q (clean terminal — pause for operator feedback)", sess.State, session.StateCompleted)
	}

	// A later user message starts a second run. The revised plan must be presented
	// through a NEW call and fresh ask; no automatic model turn ran after denial.
	cont, cerr := svc.StartRunContent(context.Background(), sess.ID, "the plan needs to handle the edge case", nil)
	if cerr != nil {
		t.Fatalf("follow-up StartRunContent (operator feedback): %v", cerr)
	}
	contCleanup := cleanupStartedRun(t, svc, sess.ID, cont)
	secondAskID, _ := awaitLivePlanAsk(t, cont, "c2", "revised")
	svc.Persist(context.Background(), sess.ID)
	if secondAskID == firstAskID {
		t.Fatalf("re-presentation reused ask id %q; want a fresh gated review", secondAskID)
	}
	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession at fresh ask: %v", err)
	}
	if sess.Mode != session.ModePlan || sess.State != session.StateAwaiting {
		t.Fatalf("fresh ask session = mode %q state %q, want plan/awaiting", sess.Mode, sess.State)
	}

	// A real verdict on the fresh ask is still required to leave plan mode.
	if err := svc.Approve(context.Background(), sess.ID, secondAskID, session.VerdictAllowOnce); err != nil {
		t.Fatalf("Approve fresh plan ask: %v", err)
	}
	contEvs := drainApprovedEvents(t, cont.Events())
	if !hasResultWithStop(contEvs, session.StopPlanApproved) {
		t.Fatalf("fresh approval must reach StopPlanApproved; stops seen = %v", stopReasonsOf(contEvs))
	}
	contCleanup()
	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession after fresh approval: %v", err)
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("mode after fresh approval = %q, want %q", sess.Mode, session.ModeDefault)
	}
}

func TestCancelledPlanReviewRecoversAndRequiresFreshAsk(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"original"}`)),
		mockllm.ToolCallTurn(call("c2", "PresentPlan", `{"plan":"unchanged"}`)),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	first, err := svc.StartRunContent(context.Background(), sess.ID, "plan a task", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	firstCleanup := cleanupStartedRun(t, svc, sess.ID, first)
	firstAskID, firstEvs := awaitLivePlanAsk(t, first, "c1", "original")
	first.Cancel()
	firstEvs = append(firstEvs, drainApprovedEvents(t, first.Events())...)
	firstCleanup()

	planAsks := 0
	approvals := 0
	cancelledResults := 0
	for _, ev := range firstEvs {
		switch ev.Type {
		case session.EvPermissionAsk:
			if ev.Ask != nil && ev.Ask.Origin() == session.AskOriginPlan && ev.Ask.Call == "c1" {
				planAsks++
			}
		case session.EvApproval:
			approvals++
		case session.EvResult:
			if ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				cancelledResults++
			}
		}
	}
	if planAsks != 1 {
		t.Fatalf("cancelled first run plan ask count = %d, want exactly 1", planAsks)
	}
	if approvals != 0 {
		t.Fatalf("cancelled first run approval count = %d, want 0", approvals)
	}
	if cancelledResults != 1 {
		t.Fatalf("cancelled first run StopCancelled result count = %d, want exactly 1", cancelledResults)
	}

	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession after cancel: %v", err)
	}
	if sess.Mode != session.ModePlan || sess.State != session.StateCancelled {
		t.Fatalf("cancelled review session = mode %q state %q, want plan/cancelled", sess.Mode, sess.State)
	}

	// StartRunContent owns cancelled-session recovery. A later user message may
	// present the unchanged plan, but it must do so through a new call and ask.
	second, err := svc.StartRunContent(context.Background(), sess.ID, "show me the plan again", nil)
	if err != nil {
		t.Fatalf("StartRunContent after cancellation: %v", err)
	}
	secondCleanup := cleanupStartedRun(t, svc, sess.ID, second)
	secondAskID, _ := awaitLivePlanAsk(t, second, "c2", "unchanged")
	svc.Persist(context.Background(), sess.ID)
	if secondAskID == firstAskID {
		t.Fatalf("post-cancellation presentation reused ask id %q", secondAskID)
	}
	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession at fresh ask: %v", err)
	}
	if sess.Mode != session.ModePlan || sess.State != session.StateAwaiting {
		t.Fatalf("post-cancellation fresh ask = mode %q state %q, want plan/awaiting", sess.Mode, sess.State)
	}
	if len(sess.Conversation.Messages) == 0 {
		t.Fatal("fresh plan ask has empty history")
	}
	// The final assistant message is the newly-pending c2 call. Everything before
	// it includes the interrupted c1 turn and must already be fully paired.
	prior := sess.Conversation.Messages[:len(sess.Conversation.Messages)-1]
	if err := session.ValidateToolPairing(prior); err != nil {
		t.Fatalf("recovered interrupted plan history is unpaired: %v", err)
	}
	foundInterrupted := false
	for _, msg := range prior {
		if msg.Role != session.RoleTool || msg.ToolResult == nil || msg.ToolResult.CallID != "c1" {
			continue
		}
		foundInterrupted = true
		if !msg.ToolResult.IsError || msg.ToolResult.Content != "tool call interrupted by cancellation" {
			t.Fatalf("c1 cancellation close-out = %+v, want error result %q", msg.ToolResult, "tool call interrupted by cancellation")
		}
	}
	if !foundInterrupted {
		t.Fatal("service recovery did not close out the cancelled PresentPlan call")
	}
	second.Cancel()
	_ = drainApprovedEvents(t, second.Events())
	secondCleanup()
}

// stopReasonsOf returns the set of stop reasons carried by EvResult events in evs.
func stopReasonsOf(evs []session.Event) []session.StopReason {
	var out []session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil {
			out = append(out, ev.Result.Stop)
		}
	}
	return out
}

// TestApprovePlanMidRunFailsPrecondition asserts that ApprovePlan returns an error
// (mapped to FAILED_PRECONDITION / 409) when a live run is in progress.
func TestApprovePlanMidRunFailsPrecondition(t *testing.T) {
	// Use a default-mode run (not plan) — any live run triggers the error.
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newService(t, llm, allowRules(), read)

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Run is live — ApprovePlan must fail.
	_, planErr := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, "")
	if planErr == nil {
		svc.FinishRun(sess.ID, run) // clean up the live run
		t.Fatal("ApprovePlan on a live run must return an error, got nil")
	}
	// Drain + finish so the live run doesn't leak (the test harness will finish).
	_ = drainServerRun(run)
	svc.FinishRun(sess.ID, run)
	if !errors.Is(planErr, server.ErrNotAwaitingPlan) {
		t.Fatalf("error = %v, want ErrNotAwaitingPlan", planErr)
	}
}

// TestApprovePlanNotAwaitingFails asserts that ApprovePlan on an idle session
// (never parked) returns ErrNotAwaitingPlan.
func TestApprovePlanNotAwaitingFails(t *testing.T) {
	llm := mockllm.New()
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Session is idle — never started a run.
	_, planErr := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, "")
	if planErr == nil {
		t.Fatal("ApprovePlan on an idle session must return an error, got nil")
	}
	if !errors.Is(planErr, server.ErrNotAwaitingPlan) {
		t.Fatalf("error = %v, want ErrNotAwaitingPlan", planErr)
	}
	if !strings.Contains(planErr.Error(), string(sess.ID)) {
		t.Fatalf("error must include the session id: %v", planErr)
	}
}

// TestApprovePlanNotPlanAskFails asserts that ApprovePlan on a session awaiting a
// NON-plan ask (a regular tool-permission ask) returns ErrNotAwaitingPlan.
func TestApprovePlanNotPlanAskFails(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	// nil rules => default is Ask for Write.
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	// Use the stock newService (Interactive: false), but we need the ask to surface.
	// Actually we DO need interactive for the ask to surface on a regular tool.
	cat := tool.NewCatalog()
	cat.MustRegister(write)
	engine := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     cat,
		Policy:      permpolicy.NewPolicy(nil, nil),
		Model:       "test-model",
		Interactive: true,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	r, err := svc.StartRunContent(context.Background(), sess.ID, "write", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	var parked bool
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin() == session.AskOriginNone {
			parked = true
			svc.Persist(context.Background(), sess.ID)
			// Deregister and cancel: the run must not stay live (ApprovePlan rejects
			// a live run, and the goroutine must not leak).
			svc.FinishRun(sess.ID, r)
			r.Cancel()
		}
	}
	if !parked {
		t.Fatal("the run never raised a non-plan permission ask")
	}

	// Now try ApprovePlan — must fail because the ask is not plan-originated.
	_, planErr := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, "")
	if planErr == nil {
		t.Fatal("ApprovePlan on a non-plan ask must return an error, got nil")
	}
	if !errors.Is(planErr, server.ErrNotAwaitingPlan) {
		t.Fatalf("error = %v, want ErrNotAwaitingPlan", planErr)
	}
}

// TestApprovePlanRecordsProceedMessage asserts that after an AllowOnce approval,
// the continuation run's first recorded user message is exactly the proceed text
// with the operator note appended.
func TestApprovePlanRecordsProceedMessage(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"do it"}`)),
		mockllm.TextTurn("done"),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, finishParked := parkPlanAsk(t, svc, sess.ID)
	defer finishParked()

	const operatorNote = "go ahead, approved by operations"
	events, err := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, operatorNote)
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	_ = drainApprovedEvents(t, events)

	sess, err = svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}

	// The continuation run's first user message must contain PlanApprovedProceedText
	// followed by the operator note.
	lastUserMsg := ""
	for _, msg := range sess.Conversation.Messages {
		if msg.Role == session.RoleUser {
			lastUserMsg = msg.Text
		}
	}
	if !strings.Contains(lastUserMsg, agent.PlanApprovedProceedText) {
		t.Fatalf("continuation run's user message must contain PlanApprovedProceedText; got %q", lastUserMsg)
	}
	if !strings.Contains(lastUserMsg, operatorNote) {
		t.Fatalf("continuation run's user message must contain the operator note %q", operatorNote)
	}
}

// ---------------------------------------------------------------------------
// HTTP SSE test
// ---------------------------------------------------------------------------

// planApproveSSEBody is the JSON body for POST /v1/sessions/{id}/plan:approve.
type planApproveSSEBody struct {
	TargetMode string `json:"target_mode"`
	Note       string `json:"note"`
}

// approvePlanHTTP POSTs the target_mode and note and drains the SSE stream.
func approvePlanHTTP(t *testing.T, srv *httptest.Server, id string, targetMode, note string) []*mecatlv1.Event {
	t.Helper()
	body, _ := json.Marshal(planApproveSSEBody{TargetMode: targetMode, Note: note})
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/plan:approve", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST plan:approve: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	return parseSSE(t, bufio.NewReader(resp.Body))
}

// TestApprovePlanHTTPSSE drives the full HTTP path: create a session, park a plan
// ask via the Service API, then call plan:approve via HTTP and assert the merged
// SSE stream carries both StopPlanApproved and StopEndTurn terminal events.
func TestApprovePlanHTTPSSE(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("execution complete"),
	)
	svc := planApprovalService(t, llm, allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSessionWithMode(t, srv, session.ModePlan)

	// Park the plan ask using the Service API (reliable; bypasses HTTP SSE complexity).
	_, finishParked := parkPlanAsk(t, svc, session.SessionID(id))
	defer finishParked()

	// Now call plan:approve via HTTP.
	events := approvePlanHTTP(t, srv, id, "default", "approved")

	if !hasTypeProto(events, "result") {
		t.Fatalf("SSE stream missing result events: %v", protoTypesOf(events))
	}
	// FIX 5 (ordering): StopPlanApproved MUST precede StopEndTurn in the merged
	// SSE stream — the resumed run terminates BEFORE the continuation run starts.
	idxPlanApproved := indexOfProtoStop(events, "plan_approved")
	idxEndTurn := indexOfProtoStop(events, "end_turn")
	if idxPlanApproved < 0 {
		t.Fatal("SSE stream must contain a result with stop=plan_approved (the resumed run's terminal)")
	}
	if idxEndTurn < 0 {
		t.Fatal("SSE stream must contain a result with stop=end_turn (the continuation run's terminal)")
	}
	if idxPlanApproved >= idxEndTurn {
		t.Fatalf("plan_approved (idx %d) must PRECEDE end_turn (idx %d) in the SSE stream", idxPlanApproved, idxEndTurn)
	}

	// Mode must have flipped.
	sess, err := svc.GetSession(context.Background(), session.SessionID(id))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("mode after HTTP ApprovePlan = %q, want %q", sess.Mode, session.ModeDefault)
	}
}

// createHTTPSessionWithMode creates a session via HTTP with a given mode.
func createHTTPSessionWithMode(t *testing.T, srv *httptest.Server, mode session.PermissionMode) string {
	t.Helper()
	var modeStr string
	switch mode {
	case session.ModePlan:
		modeStr = "plan"
	case session.ModeAccept:
		modeStr = "accept_edits"
	default:
		modeStr = "default"
	}
	body := strings.NewReader(`{"mode":"` + modeStr + `"}`)
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out.SessionID
}

// hasTypeProto reports whether any proto Event has the given type string.
func hasTypeProto(evs []*mecatlv1.Event, ty string) bool {
	for _, e := range evs {
		if e.GetType() == ty {
			return true
		}
	}
	return false
}

// indexOfProtoStop returns the index of the FIRST proto result Event carrying
// the given stop reason string, or -1 if none. Used by the plan-approval tests
// to assert StopPlanApproved precedes StopEndTurn in the merged stream (FIX 5
// ordering).
func indexOfProtoStop(evs []*mecatlv1.Event, stop string) int {
	for i, e := range evs {
		if e.GetType() == "result" && e.GetResult() != nil && e.GetResult().GetStop() == stop {
			return i
		}
	}
	return -1
}

// protoTypesOf returns the type strings of proto events.
func protoTypesOf(evs []*mecatlv1.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.GetType()
	}
	return out
}

// ---------------------------------------------------------------------------
// gRPC streaming test
// ---------------------------------------------------------------------------

// recvAllApprovePlanEvents drains an ApprovePlan stream to EOF.
func recvAllApprovePlanEvents(t *testing.T, stream mecatlv1.HarnessService_ApprovePlanClient) []*mecatlv1.Event {
	t.Helper()
	var out []*mecatlv1.Event
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, ev)
	}
}

// TestApprovePlanGRPCStreaming drives the full gRPC path: create a session, park
// a plan ask, call the ApprovePlan streaming RPC, and assert the streamed events
// contain both the resumed run's terminal and the continuation run's terminal.
func TestApprovePlanGRPCStreaming(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"plan"}`)),
		mockllm.TextTurn("done"),
	)
	svc := planApprovalService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
		Mode: mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sid := cs.GetSessionId()

	// Park the plan ask. We use the Service directly since parkPlanAsk is cleaner
	// than re-implementing the gRPC Converse flow for a single-tool-call.
	_, finishParked := parkPlanAsk(t, svc, session.SessionID(sid))
	defer finishParked()

	stream, err := client.ApprovePlan(ctx, &mecatlv1.ApprovePlanRequest{
		SessionId:  sid,
		TargetMode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
		Note:       "gtg",
	})
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	evs := recvAllApprovePlanEvents(t, stream)

	// FIX 5 (ordering): StopPlanApproved MUST precede StopEndTurn in the merged
	// gRPC stream — the resumed run terminates BEFORE the continuation run starts.
	idxPlanApproved := indexOfProtoStop(evs, "plan_approved")
	idxEndTurn := indexOfProtoStop(evs, "end_turn")
	if idxPlanApproved < 0 {
		t.Fatal("gRPC stream must contain a result with stop=plan_approved")
	}
	if idxEndTurn < 0 {
		t.Fatal("gRPC stream must contain a result with stop=end_turn")
	}
	if idxPlanApproved >= idxEndTurn {
		t.Fatalf("plan_approved (idx %d) must PRECEDE end_turn (idx %d) in the gRPC stream", idxPlanApproved, idxEndTurn)
	}

	// Mode must have flipped.
	sess, err := svc.GetSession(ctx, session.SessionID(sid))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModeDefault {
		t.Fatalf("mode after gRPC ApprovePlan = %q, want %q", sess.Mode, session.ModeDefault)
	}
}

// TestApprovePlanGRPCMidRunFailsPrecondition asserts that the gRPC ApprovePlan
// maps ErrNotAwaitingPlan to codes.FailedPrecondition. Error arrives on Recv()
// (server-streaming RPC: the initial call opens the stream, the handler error
// closes it).
func TestApprovePlanGRPCMidRunFailsPrecondition(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	release := make(chan struct{})
	entered := make(chan struct{})
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			close(entered)
			<-release
		}),
	}, mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules(), read)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sid := session.SessionID(cs.GetSessionId())

	run, err := svc.StartRun(ctx, sid, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	<-entered // run blocked inside provider call

	apStream, apErr := client.ApprovePlan(ctx, &mecatlv1.ApprovePlanRequest{
		SessionId:  cs.GetSessionId(),
		TargetMode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	})
	close(release)
	_ = drainServerRun(run)
	svc.FinishRun(sid, run)

	// gRPC server-streaming: initial call succeeds (stream open), error on Recv().
	if apErr != nil {
		t.Fatalf("ApprovePlan initial call: %v (expected nil, error on Recv())", apErr)
	}
	_, recvErr := apStream.Recv()
	if recvErr == nil {
		t.Fatal("gRPC ApprovePlan on a live run must return an error on Recv(), got nil")
	}
	if code := status.Code(recvErr); code != codes.FailedPrecondition {
		t.Fatalf("gRPC error code = %v (%v), want FailedPrecondition", code, recvErr)
	}
}

// TestApprovePlanGRPCNotFound asserts that ApprovePlan on an unknown session
// returns NotFound. Error arrives on Recv() (server-streaming RPC).
func TestApprovePlanGRPCNotFound(t *testing.T) {
	svc := planApprovalService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	apStream, apErr := client.ApprovePlan(ctx, &mecatlv1.ApprovePlanRequest{
		SessionId:  "never-created",
		TargetMode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	})
	if apErr != nil {
		t.Fatalf("ApprovePlan initial call: %v (expected nil, error on Recv())", apErr)
	}
	_, recvErr := apStream.Recv()
	if recvErr == nil {
		t.Fatal("gRPC ApprovePlan on an unknown session must return an error on Recv(), got nil")
	}
	if code := status.Code(recvErr); code != codes.NotFound {
		t.Fatalf("gRPC error code = %v (%v), want NotFound", code, recvErr)
	}
}

// ---------------------------------------------------------------------------
// HTTP error-code tests (FIX 5)
// ---------------------------------------------------------------------------

// approvePlanHTTPStatus POSTs the plan:approve body and returns the HTTP status
// code + body (for the error paths that do NOT open an SSE stream — the handler
// writes a JSON error via writeServiceError before any SSE framing).
func approvePlanHTTPStatus(t *testing.T, srv *httptest.Server, id, targetMode string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(planApproveSSEBody{TargetMode: targetMode})
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/plan:approve", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST plan:approve: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// TestApprovePlanHTTPMidRun409 asserts that ApprovePlan via HTTP on a LIVE
// mid-run session returns 409 Conflict (ErrNotAwaitingPlan → 409).
func TestApprovePlanHTTPMidRun409(t *testing.T) {
	// A default-mode run with a read-only tool that blocks (so the run stays live).
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	release := make(chan struct{})
	entered := make(chan struct{})
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			close(entered)
			<-release
		}),
	}, mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules(), read)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSessionWithMode(t, srv, session.ModePlan)
	run, err := svc.StartRun(context.Background(), session.SessionID(id), "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	<-entered // run blocked inside provider call — it is live.

	code, body := approvePlanHTTPStatus(t, srv, id, "default")
	close(release)
	_ = drainServerRun(run)
	svc.FinishRun(session.SessionID(id), run)

	if code != http.StatusConflict {
		t.Fatalf("HTTP ApprovePlan on a live run: status = %d, want %d (409 Conflict); body=%s", code, http.StatusConflict, body)
	}
}

// TestApprovePlanHTTPIdle409 asserts that ApprovePlan via HTTP on an IDLE
// session (never parked) returns 409 Conflict.
func TestApprovePlanHTTPIdle409(t *testing.T) {
	svc := planApprovalService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSessionWithMode(t, srv, session.ModePlan)

	code, body := approvePlanHTTPStatus(t, srv, id, "default")
	if code != http.StatusConflict {
		t.Fatalf("HTTP ApprovePlan on an idle session: status = %d, want %d (409 Conflict); body=%s", code, http.StatusConflict, body)
	}
}

// TestApprovePlanHTTPNotPlanAsk409 asserts that ApprovePlan via HTTP on a
// session awaiting a NON-plan ask returns 409 Conflict.
func TestApprovePlanHTTPNotPlanAsk409(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	cat := tool.NewCatalog()
	cat.MustRegister(write)
	engine := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     cat,
		Policy:      permpolicy.NewPolicy(nil, nil),
		Model:       "test-model",
		Interactive: true,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSessionWithMode(t, srv, session.ModeDefault)
	r, err := svc.StartRunContent(context.Background(), session.SessionID(id), "write", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	var parked bool
	for ev := range r.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.Origin() == session.AskOriginNone {
			parked = true
			svc.Persist(context.Background(), session.SessionID(id))
			svc.FinishRun(session.SessionID(id), r)
			r.Cancel()
		}
	}
	if !parked {
		t.Fatal("the run never raised a non-plan permission ask")
	}

	code, body := approvePlanHTTPStatus(t, srv, id, "default")
	if code != http.StatusConflict {
		t.Fatalf("HTTP ApprovePlan on a non-plan ask: status = %d, want %d (409 Conflict); body=%s", code, http.StatusConflict, body)
	}
}

// TestApprovePlanHTTPNotFound404 asserts that ApprovePlan via HTTP on an
// unknown session returns 404 Not Found.
func TestApprovePlanHTTPNotFound404(t *testing.T) {
	svc := planApprovalService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	code, body := approvePlanHTTPStatus(t, srv, "never-created", "default")
	if code != http.StatusNotFound {
		t.Fatalf("HTTP ApprovePlan on an unknown session: status = %d, want %d (404 Not Found); body=%s", code, http.StatusNotFound, body)
	}
}
