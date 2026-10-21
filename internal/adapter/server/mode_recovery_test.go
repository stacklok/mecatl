package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// parkAwaitingInMode drives a default session to a durable pending Write ask
// on a loose deployment, then rewrites the stored mode to model a snapshot
// parked under a deployment that admitted stored.
func parkAwaitingInMode(t *testing.T, store port.SessionStore, stored session.PermissionMode) (session.SessionID, string) {
	t.Helper()
	id, askID, _ := parkAwaitingLogged(t, store, nil, stored)
	return id, askID
}

// parkAwaitingLogged is parkAwaitingInMode that also records the parked run's
// events, through its permission.ask, into log (when non-nil), modelling the
// relay's durable log of a run that parked and then died with its ask still
// open. It returns the parked run's id as well.
func parkAwaitingLogged(t *testing.T, store port.SessionStore, log port.EventLog, stored session.PermissionMode) (session.SessionID, string, string) {
	t.Helper()
	ctx := context.Background()
	var ran atomic.Int64
	loose := newAskingService(t, store, permstore.New(), &ran,
		mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))), false)
	sess, err := loose.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := loose.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askID, runID string
	for ev := range run.Events() {
		if askID != "" {
			continue // the dying run's tail never reaches the durable log
		}
		if log != nil {
			if err := log.Append(ctx, sess.ID, ev); err != nil {
				t.Fatalf("seed event log: %v", err)
			}
		}
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askID, runID = ev.Ask.AskID, ev.RunID
			loose.Persist(ctx, sess.ID)
			run.Cancel()
		}
	}
	loose.FinishRun(sess.ID, run)
	if askID == "" || runID == "" {
		t.Fatalf("the run never raised a run-stamped permission ask (ask %q run %q)", askID, runID)
	}
	parked, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	parked.Mode = stored
	if err := store.Save(ctx, parked); err != nil {
		t.Fatal(err)
	}
	return sess.ID, askID, runID
}

// recoveryService is a restarted deployment over store with the given ceiling
// and checker; lease, when non-nil, is shared with a peer.
// log, when non-nil, is the durable event log.
func recoveryService(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ran *atomic.Int64, ceiling session.PermissionMode, checker bool, lease port.SessionLease, log port.EventLog) *server.Service {
	t.Helper()
	return newRecoveryService(t, store, llm, ran, ceiling, checker, lease, "recovering", log)
}

// recoveryServiceAs is recoveryService with an explicit lease owner, so two
// replicas can share one lease backend.
func recoveryServiceAs(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ran *atomic.Int64, ceiling session.PermissionMode, checker bool, lease port.SessionLease, owner string) *server.Service {
	t.Helper()
	return newRecoveryService(t, store, llm, ran, ceiling, checker, lease, owner, memstore.NewEventLog())
}

func newRecoveryService(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ran *atomic.Int64, ceiling session.PermissionMode, checker bool, lease port.SessionLease, owner string, log port.EventLog) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: ran})
	cfg := server.Config{
		Engine:                     agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model", Store: store}),
		Store:                      store,
		Now:                        func() time.Time { return time.Unix(0, 0) },
		MaxAllowedMode:             ceiling,
		EnforcingCheckerConfigured: checker,
		EventLog:                   log,
	}
	if lease != nil {
		cfg.SessionLease, cfg.LeaseOwner, cfg.LeaseTTL, cfg.LeaseRenewInterval = lease, owner, time.Minute, time.Hour
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestSetModeDowngradeRecoversRefusedAwaitingSession pins option B's recovery:
// a stored session awaiting an approval in a mode the restarted server refuses
// is unrecoverable by approval, and a SetMode to an admitted lower mode cancels
// the pending turn without any provider or tool work and applies the mode in
// one save; the next StartRun then runs in the new mode.
func TestSetModeDowngradeRecoversRefusedAwaitingSession(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	id, askID := parkAwaitingInMode(t, store, session.ModeYolo)

	var ran atomic.Int64
	llm := mockllm.New(mockllm.TextTurn("continued"))
	svc := recoveryService(t, store, llm, &ran, session.ModeAccept, true, nil, nil)

	_, err := svc.ApproveRun(ctx, id, askID, session.VerdictAllowOnce, "")
	if !errors.Is(err, server.ErrStoredModeNotAdmitted) || !strings.Contains(err.Error(), "SetMode to an admitted lower mode") {
		t.Fatalf("ApproveRun err = %v, want ErrStoredModeNotAdmitted naming the SetMode downgrade", err)
	}
	// A client offers the recovery on this stable code alone, not on every
	// FailedPrecondition the verdict path can return.
	if got := server.ClassifyErrorCodeForTest(err); got != "session_mode_not_admitted" {
		t.Fatalf("ApproveRun error code = %q, want session_mode_not_admitted", got)
	}

	got, err := svc.SetMode(ctx, id, session.ModeDefault)
	if err != nil {
		t.Fatalf("SetMode(default) on refused awaiting session: %v", err)
	}
	if got.Mode != session.ModeDefault || got.State != session.StateCancelled {
		t.Fatalf("SetMode returned mode %q state %q, want default/cancelled", got.Mode, got.State)
	}
	if llm.Calls() != 0 || ran.Load() != 0 {
		t.Fatalf("recovery ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
	}
	kept, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := kept.PendingAsk(); pending || kept.Mode != session.ModeDefault || kept.State != session.StateCancelled {
		t.Fatalf("stored after recovery: mode %q state %q pending %t", kept.Mode, kept.State, pending)
	}
	if _, err := svc.ApproveRun(ctx, id, askID, session.VerdictAllowOnce, ""); err == nil {
		t.Fatal("the cancelled ask still resumed")
	}

	run, err := svc.StartRun(ctx, id, "continue")
	if err != nil {
		t.Fatalf("StartRun after recovery: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(id, run)
	if llm.Calls() != 1 || ran.Load() != 0 {
		t.Fatalf("next run: provider calls %d, tool executions %d; want 1, 0", llm.Calls(), ran.Load())
	}
	after, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode != session.ModeDefault || after.State != session.StateCompleted {
		t.Fatalf("after next run: mode %q state %q, want default/completed", after.Mode, after.State)
	}
}

// TestSetModeOnAwaitingSessionStillRefusedOutsideRecovery pins that only the
// narrow recovery widens SetMode: every other awaiting SetMode is refused with
// the pending ask intact and no work run.
func TestSetModeOnAwaitingSessionStillRefusedOutsideRecovery(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		stored  session.PermissionMode
		ceiling session.PermissionMode
		checker bool
		target  session.PermissionMode
		peer    bool
		want    error
	}{
		{"stored mode still admitted", session.ModeAccept, session.ModeAccept, true, session.ModeDefault, false, server.ErrInvalidArgument},
		{"target above ceiling", session.ModeYolo, session.ModeAccept, true, session.ModeAuto, false, server.ErrInvalidArgument},
		{"target not lower", session.ModeAuto, session.ModeYolo, false, session.ModeYolo, false, server.ErrInvalidArgument},
		{"peer holds the lease", session.ModeYolo, session.ModeAccept, true, session.ModeDefault, true, server.ErrSessionLeasedElsewhere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			log := memstore.NewEventLog()
			id, askID, _ := parkAwaitingLogged(t, store, log, tc.stored)
			parkedLog := readEventLog(t, log, id)
			var lease port.SessionLease
			if tc.peer {
				shared := memlease.New(wallclock.Clock{}, time.Minute)
				if _, err := shared.Acquire(ctx, id, "peer"); err != nil {
					t.Fatalf("peer Acquire: %v", err)
				}
				lease = shared
			}
			var ran atomic.Int64
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			svc := recoveryService(t, store, llm, &ran, tc.ceiling, tc.checker, lease, log)

			if _, err := svc.SetMode(ctx, id, tc.target); !errors.Is(err, tc.want) {
				t.Fatalf("SetMode(%s) err = %v, want %v", tc.target, err, tc.want)
			}
			if llm.Calls() != 0 || ran.Load() != 0 {
				t.Fatalf("refusal ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
			}
			kept, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if pending, ok := kept.PendingAsk(); kept.State != session.StateAwaiting || !ok || pending.AskID != askID || kept.Mode != tc.stored {
				t.Fatalf("refusal changed the session: state %q mode %q pending %t", kept.State, kept.Mode, ok)
			}
			if got := readEventLog(t, log, id); !reflect.DeepEqual(got, parkedLog) {
				t.Fatalf("refusal appended %d event(s) to the durable log, want none", len(got)-len(parkedLog))
			}
		})
	}
}

// crashOrphanInMode stores a session as a crash leaves it mid-turn: running in
// stored, with an assistant tool call that never got a result and no process
// driving it. It returns the session id and the unanswered call's id.
func crashOrphanInMode(t *testing.T, store port.SessionStore, stored session.PermissionMode) (session.SessionID, session.ToolCallID) {
	t.Helper()
	ctx := context.Background()
	var ran atomic.Int64
	loose := recoveryService(t, store, mockllm.New(), &ran, session.ModeYolo, true, nil, nil)
	created, err := loose.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, err := store.Load(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`))
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	sess.Mode = stored
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return sess.ID, call.ID
}

// TestSetModeRecoversRefusedCrashOrphanedRunningSession pins the recovery for
// a session a crash left running in a mode the restarted server refuses.
// StartRun refuses it before its crash-orphan repair, so without this nothing
// short of the background stale sweep could unstick it. Once the lease proves
// no server drives the run, SetMode to an admitted mode abandons the dead turn
// to idle without provider or tool work and applies the mode in one save; the
// next StartRun then runs in the new mode.
func TestSetModeRecoversRefusedCrashOrphanedRunningSession(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	id, callID := crashOrphanInMode(t, store, session.ModeYolo)

	var ran atomic.Int64
	llm := mockllm.New(mockllm.TextTurn("continued"))
	lease := memlease.New(wallclock.Clock{}, time.Minute)
	svc := recoveryService(t, store, llm, &ran, session.ModeAccept, true, lease, nil)

	_, err := svc.StartRun(ctx, id, "continue")
	if !errors.Is(err, server.ErrStoredModeNotAdmitted) || !strings.Contains(err.Error(), "SetMode to an admitted mode ends the interrupted turn") {
		t.Fatalf("StartRun err = %v, want ErrStoredModeNotAdmitted naming the SetMode recovery", err)
	}

	got, err := svc.SetMode(ctx, id, session.ModeDefault)
	if err != nil {
		t.Fatalf("SetMode(default) on refused crash-orphaned session: %v", err)
	}
	if got.Mode != session.ModeDefault || got.State != session.StateIdle {
		t.Fatalf("SetMode returned mode %q state %q, want default/idle", got.Mode, got.State)
	}
	if llm.Calls() != 0 || ran.Load() != 0 {
		t.Fatalf("recovery ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
	}
	kept, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Mode != session.ModeDefault || kept.State != session.StateIdle {
		t.Fatalf("stored after recovery: mode %q state %q, want default/idle", kept.Mode, kept.State)
	}
	msgs := kept.Conversation.Messages
	if last := msgs[len(msgs)-1]; last.ToolResult == nil || last.ToolResult.CallID != callID || !last.ToolResult.IsError {
		t.Fatalf("the unanswered call was not closed out: last message %+v", last)
	}

	run, err := svc.StartRun(ctx, id, "continue")
	if err != nil {
		t.Fatalf("StartRun after recovery: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(id, run)
	if llm.Calls() != 1 || ran.Load() != 0 {
		t.Fatalf("next run: provider calls %d, tool executions %d; want 1, 0", llm.Calls(), ran.Load())
	}
	after, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode != session.ModeDefault || after.State != session.StateCompleted {
		t.Fatalf("after next run: mode %q state %q, want default/completed", after.Mode, after.State)
	}
}

// TestSetModeOnRunningSessionStillRefusedOutsideRecovery pins that only the
// narrow recovery widens SetMode on a running session: an admitted stored mode
// is left for StartRun's repair, an inadmissible target is refused, and a peer
// holding the lease (the run may still be live there) blocks the abandon. Each
// refusal leaves the running snapshot unchanged and runs no work.
func TestSetModeOnRunningSessionStillRefusedOutsideRecovery(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		stored  session.PermissionMode
		ceiling session.PermissionMode
		target  session.PermissionMode
		peer    bool
		want    error
	}{
		{"stored mode still admitted", session.ModeAccept, session.ModeAccept, session.ModeDefault, false, server.ErrInvalidArgument},
		{"target above ceiling", session.ModeYolo, session.ModeAccept, session.ModeAuto, false, server.ErrInvalidArgument},
		{"peer holds the lease", session.ModeYolo, session.ModeAccept, session.ModeDefault, true, server.ErrSessionLeasedElsewhere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			id, _ := crashOrphanInMode(t, store, tc.stored)
			lease := memlease.New(wallclock.Clock{}, time.Minute)
			if tc.peer {
				if _, err := lease.Acquire(ctx, id, "peer"); err != nil {
					t.Fatalf("peer Acquire: %v", err)
				}
			}
			var ran atomic.Int64
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			svc := recoveryService(t, store, llm, &ran, tc.ceiling, true, lease, nil)

			if _, err := svc.SetMode(ctx, id, tc.target); !errors.Is(err, tc.want) {
				t.Fatalf("SetMode(%s) err = %v, want %v", tc.target, err, tc.want)
			}
			if llm.Calls() != 0 || ran.Load() != 0 {
				t.Fatalf("refusal ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
			}
			kept, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if kept.State != session.StateRunning || kept.Mode != tc.stored {
				t.Fatalf("refusal changed the session: state %q mode %q", kept.State, kept.Mode)
			}
		})
	}
}

// TestSetModeRecoveryClosesTheParkedTurnForWatchers pins that the recovery is
// observable: after the snapshot save, the durable log gains, stamped with the
// parked run and the acting caller, a retract of the open ask, the cancellation
// close-out for the unanswered call, and result{cancelled}. A watch attached
// before SetMode and a live subscriber both receive them, and the log still
// folds to the committed snapshot instead of an open ask.
func TestSetModeRecoveryClosesTheParkedTurnForWatchers(t *testing.T) {
	store := memstore.New()
	log := memstore.NewEventLog()
	id, askID, runID := parkAwaitingLogged(t, store, log, session.ModeYolo)
	parkedLog := readEventLog(t, log, id)

	var ran atomic.Int64
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := recoveryService(t, store, llm, &ran, session.ModeAccept, true, nil, log)

	ctx, cancel := context.WithTimeout(session.WithPrincipal(context.Background(), alice), 15*time.Second)
	defer cancel()
	live, unsubscribe, err := svc.Subscribe(ctx, id)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	envelopes, err := svc.WatchSessionEvents(ctx, id, "", "")
	if err != nil {
		t.Fatalf("WatchSessionEvents: %v", err)
	}
	watched := make(chan []session.Event, 1)
	caughtUp := make(chan struct{})
	go func() {
		var got []session.Event
		defer func() { watched <- got }()
		for env, iterErr := range envelopes {
			if iterErr != nil {
				return
			}
			if env.Event == nil {
				close(caughtUp)
				continue
			}
			if env.Phase == server.WatchPhaseLive {
				got = append(got, *env.Event)
				if env.Event.Type == session.EvResult {
					return
				}
			}
		}
	}()
	select {
	case <-caughtUp:
	case <-ctx.Done():
		t.Fatal("watch never reached the live boundary")
	}

	got, err := svc.SetMode(ctx, id, session.ModeDefault)
	if err != nil {
		t.Fatalf("SetMode(default): %v", err)
	}
	if got.State != session.StateCancelled || got.Mode != session.ModeDefault {
		t.Fatalf("SetMode returned state %q mode %q, want cancelled/default", got.State, got.Mode)
	}
	if llm.Calls() != 0 || ran.Load() != 0 {
		t.Fatalf("recovery ran work: provider calls %d, tool executions %d", llm.Calls(), ran.Load())
	}

	all := readEventLog(t, log, id)
	tail := all[len(parkedLog):]
	wantTypes := []session.EventType{session.EvPermissionRetract, session.EvToolResult, session.EvResult}
	if len(tail) != len(wantTypes) {
		t.Fatalf("recovery appended %d event(s) %v, want %v", len(tail), eventTypes(tail), wantTypes)
	}
	for i, ev := range tail {
		if ev.Type != wantTypes[i] || ev.RunID != runID {
			t.Fatalf("appended[%d] = %s run %q, want %s run %q", i, ev.Type, ev.RunID, wantTypes[i], runID)
		}
		if ev.Actor == nil || *ev.Actor != *alice {
			t.Fatalf("appended[%d] actor = %+v, want the acting caller", i, ev.Actor)
		}
	}
	if tail[0].Ask == nil || tail[0].Ask.AskID != askID {
		t.Fatalf("retract = %+v, want ask %q", tail[0].Ask, askID)
	}
	if tr := tail[1].ToolResult; tr == nil || tr.CallID != "w1" || !tr.IsError || tr.Content != "tool call interrupted by cancellation" {
		t.Fatalf("close-out = %+v, want the cancellation close-out for w1", tr)
	}
	if res := tail[2].Result; res == nil || res.Stop != session.StopCancelled {
		t.Fatalf("result = %+v, want stop cancelled", res)
	}

	var streamed []session.Event
	for range wantTypes {
		select {
		case ev := <-live:
			streamed = append(streamed, ev)
		case <-ctx.Done():
			t.Fatalf("live subscriber received %v, want %v", eventTypes(streamed), wantTypes)
		}
	}
	if !reflect.DeepEqual(eventTypes(streamed), wantTypes) {
		t.Fatalf("live subscriber received %v, want %v", eventTypes(streamed), wantTypes)
	}
	if w := <-watched; !reflect.DeepEqual(eventTypes(w), wantTypes) || w[0].RunID != runID {
		t.Fatalf("watch delivered %v live, want %v for run %q", eventTypes(w), wantTypes, runID)
	}

	kept, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{ID: id, Mode: kept.Mode, EnvironmentRef: kept.EnvironmentRef, CreatedAt: kept.CreatedAt}, log.Read(context.Background(), id))
	if err != nil {
		t.Fatalf("Fold after recovery: %v", err)
	}
	foldedStop, _ := folded.RecordedStopReason()
	keptStop, _ := kept.RecordedStopReason()
	if _, pending := folded.PendingAsk(); pending || folded.State != kept.State || foldedStop != keptStop {
		t.Fatalf("folded state %q stop %q pending %t, want the snapshot's %q/%q with no ask", folded.State, foldedStop, pending, kept.State, keptStop)
	}
	// The log's close-out is exactly what the next run's Interrupt repairs the
	// snapshot's history with.
	if err := kept.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if !reflect.DeepEqual(conversationShape(folded.Conversation.Messages), conversationShape(kept.Conversation.Messages)) {
		t.Fatalf("folded history %v, want the interrupted snapshot's %v", conversationShape(folded.Conversation.Messages), conversationShape(kept.Conversation.Messages))
	}
}

func eventTypes(events []session.Event) []session.EventType {
	out := make([]session.EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

// conversationShape is the structural history a fold reconstructs: role, text,
// call ids, and tool results.
func conversationShape(msgs []session.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		line := string(m.Role) + ":" + m.Text
		for _, c := range m.ToolCalls {
			line += " call=" + string(c.ID)
		}
		if m.ToolResult != nil {
			line += " result=" + string(m.ToolResult.CallID) + "/" + m.ToolResult.Content
		}
		out = append(out, line)
	}
	return out
}
