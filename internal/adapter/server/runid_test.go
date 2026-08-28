package server_test

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// runIDsFrom collects the distinct, non-empty run ids seen on a Converse stream,
// in first-seen order, and reports how many events carried none.
func runIDsFrom(events []*mecatlv1.Event) (ids []string, empties []string) {
	seen := map[string]bool{}
	for _, ev := range events {
		id := ev.GetRunId()
		if id == "" {
			empties = append(empties, ev.GetType())
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, empties
}

// converseOnce drives one full Converse run to its terminal event and returns
// every frame. It is the run-id analogue of the existing recvAll pattern.
func converseOnce(t *testing.T, client mecatlv1.HarnessServiceClient, sessionID, text string) []*mecatlv1.Event {
	t.Helper()
	stream, err := client.Converse(t.Context())
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: sessionID, Text: text}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	return recvAll(t, stream)
}

// TestSDKServerEnablers_Scenario4_EveryRunEventCarriesOneID is AC4.1.
//
// Every event of a run carries the same non-empty id, and two consecutive runs
// of one session carry different ones. The second half is the point: Seq is
// monotonic WITHIN a run and restarts each run, so before this there was no way
// to tell two runs of a session apart on the wire.
func TestSDKServerEnablers_Scenario4_EveryRunEventCarriesOneID(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var allIDs []string
	for turn, prompt := range []string{"one", "two"} {
		events := converseOnce(t, client, cs.GetSessionId(), prompt)
		ids, empties := runIDsFrom(events)
		if len(empties) != 0 {
			t.Fatalf("run %d: %d event(s) carried no run id: %v", turn, len(empties), empties)
		}
		if len(ids) != 1 {
			t.Fatalf("run %d: events carried %d distinct run ids %v, want exactly 1", turn, len(ids), ids)
		}
		allIDs = append(allIDs, ids[0])
	}
	if allIDs[0] == allIDs[1] {
		t.Errorf("two consecutive runs shared run id %q; each run must get its own", allIDs[0])
	}
	for _, id := range allIDs {
		if strings.Contains(id, ":") {
			t.Errorf("run id %q contains a colon; it feeds the askID grammar and must be colon-free", id)
		}
	}
}

// TestADR_0245_LoopStampsEveryEmittedEvent is AC4.3.
//
// The stamp lives at Run.emit/emitOrAbort, so it is structural: no relay,
// transport, or persistence path can omit it, because there is no path that does
// not go through those two functions.
//
// The negative half matters just as much — a run with no supplied id emits an
// empty one, byte-identical to the behaviour before ADR 0249, so an in-memory
// embedder or a test that passes nothing is unaffected.
func TestADR_0245_LoopStampsEveryEmittedEvent(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("hello"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	events := converseOnce(t, client, cs.GetSessionId(), "hi")
	if len(events) == 0 {
		t.Fatal("no events observed")
	}
	for _, ev := range events {
		if ev.GetRunId() == "" {
			t.Errorf("event %q carried no run id; the emit stamp must cover every event kind", ev.GetType())
		}
	}
}

// TestADR_0245_AwaitingResumeKeepsRunID is AC4.4.
//
// A session parked awaiting an approval, restored into a FRESH Service (the
// cross-process restart), resumes as THE SAME run. This is the reason the id is
// persisted at all: without it the resumed run would mint a second identity and
// a client following the first would never see it finish.
func TestADR_0245_AwaitingResumeKeepsRunID(t *testing.T) {
	sess := session.New("s-resume", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	const want = "run_persisted_identity"
	sess.BeginRun(want)

	snap, err := sessnap.Of(sess)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if snap.RunID != want {
		t.Fatalf("Capture lost the run id: got %q, want %q", snap.RunID, want)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.RunID(); got != want {
		t.Errorf("restored run id = %q, want %q — a resume would mint a second identity", got, want)
	}
}

// TestSDKServerEnablers_Scenario4_LegacySnapshotRestoresEmpty is AC4.8.
//
// A snapshot written before ADR 0249 has no run_id key. It must restore with an
// empty id and be stamped on its next run — additive, no migration sweep, the
// Profile/ProviderID precedent.
func TestSDKServerEnablers_Scenario4_LegacySnapshotRestoresEmpty(t *testing.T) {
	sess := session.New("s-legacy", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	snap, err := sessnap.Of(sess)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	snap.RunID = "" // a pre-0245 snapshot simply has no such key

	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.RunID(); got != "" {
		t.Errorf("legacy restore produced run id %q, want empty", got)
	}
	// And it is stampable afterwards, so the next run heals it.
	restored.BeginRun("run_next")
	if got := restored.RunID(); got != "run_next" {
		t.Errorf("after BeginRun, run id = %q, want %q", got, "run_next")
	}
}

// TestADR_0245_FoldIgnoresRunID is AC4.7.
//
// The event-sourced fold reconstructs a session from its durable log. RunID is
// attribution, not reconstruction input — exactly like Actor — so a fold must
// produce the same session whether or not the events carry one.
func TestADR_0245_FoldIgnoresRunID(t *testing.T) {
	build := func(runID string) []session.Event {
		return []session.Event{
			{Type: session.EvUserPrompt, Seq: 1, RunID: runID, UserPrompt: &session.UserPromptPayload{Text: "hello"}},
			{Type: session.EvResult, Seq: 2, RunID: runID, Result: &session.ResultPayload{Text: "hi", Stop: session.StopEndTurn}},
		}
	}
	withID, err := eventsource.Fold(eventsource.SessionMeta{ID: "s1", Workspace: "/ws", Mode: session.ModeDefault}, seqOf(build("run_abc")))
	if err != nil {
		t.Fatalf("Fold with run id: %v", err)
	}
	without, err := eventsource.Fold(eventsource.SessionMeta{ID: "s1", Workspace: "/ws", Mode: session.ModeDefault}, seqOf(build("")))
	if err != nil {
		t.Fatalf("Fold without run id: %v", err)
	}
	if len(withID.Conversation.Messages) != len(without.Conversation.Messages) {
		t.Fatalf("fold produced %d messages with a run id and %d without; RunID must not be a reconstruction input",
			len(withID.Conversation.Messages), len(without.Conversation.Messages))
	}
	for i := range withID.Conversation.Messages {
		a, b := withID.Conversation.Messages[i], without.Conversation.Messages[i]
		if a.Role != b.Role || a.Text != b.Text {
			t.Errorf("message %d differs: %+v vs %+v", i, a, b)
		}
	}
	if withID.State != without.State {
		t.Errorf("folded state differs: %v vs %v", withID.State, without.State)
	}
}

// seqOf adapts a slice to the iter.Seq2 the fold consumes.
func seqOf(evs []session.Event) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range evs {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// TestSDKServerEnablers_Scenario4_PlanResolutionSpansTwoRunIDs is AC4.5.
//
// Approving a plan produces TWO runs on one merged stream: the resumed
// plan-approval run (which REUSES the id it parked with) and the continuation
// execution run (which MINTS a fresh one). Both behaviours fall out of the two
// seams — ResumeApproval reads the id off the session, StartRunContent mints —
// so this test exists to prove the composition, not new code.
//
// It is also the reason an SDK's plan resolution cannot return a single Run
// handle: a caller following "the run" would silently stop at the first
// terminal and never see the execution it just authorised.
func TestSDKServerEnablers_Scenario4_PlanResolutionSpansTwoRunIDs(t *testing.T) {
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"step 1"}`)),
		mockllm.TextTurn("all tasks executed successfully"),
	)
	svc := planApprovalService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, finishParked := parkPlanAsk(t, svc, sess.ID)
	defer finishParked()

	// The id the session parked with is the one the resumed run must continue.
	parked, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	parkedRunID := parked.RunID()
	if parkedRunID == "" {
		t.Fatal("the parked session carries no run id; a resume would have nothing to continue")
	}

	events, err := svc.ApprovePlan(context.Background(), sess.ID, session.ModeDefault, "")
	if err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	var order []string
	seen := map[string]bool{}
	for ev := range events {
		if ev.RunID == "" || seen[ev.RunID] {
			continue
		}
		seen[ev.RunID] = true
		order = append(order, ev.RunID)
	}

	if len(order) != 2 {
		t.Fatalf("merged plan-resolution stream carried %d distinct run ids %v, want exactly 2 "+
			"(the resumed plan run and the continuation execution run)", len(order), order)
	}
	if order[0] != parkedRunID {
		t.Errorf("first run id on the merged stream = %q, want the PARKED id %q — the resumed run must CONTINUE, not restart",
			order[0], parkedRunID)
	}
	if order[1] == parkedRunID {
		t.Errorf("the continuation run reused the parked id %q; it is a new run and must mint its own", parkedRunID)
	}
}
