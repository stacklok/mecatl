package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// writeAskTool is a mutating tool that asks (no floor allow) and records each
// execution into a shared counter, so a resume-from-awaiting test can prove
// exactly-once execution.
type writeAskTool struct{ ran *atomic.Int64 }

func (*writeAskTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Write", Description: "Write: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*writeAskTool) ReadOnly() bool { return false }
func (w *writeAskTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	w.ran.Add(1)
	return session.NewToolResult(in.ID, "wrote"), nil
}

var _ tool.Tool = (*writeAskTool)(nil)

// newAskingService builds a Service whose engine emits a single Write tool call
// (gated as Ask under ModeDefault) over the shared store. The shared *permstore so
// a learned allow-always survives across services; ran counts Write executions.
//
// engineSaves controls whether the ENGINE auto-saves at turn boundaries / terminals
// (Deps.Store): a "process that parks an ask then dies" must NOT let its run's
// cancel-driven terminal save overwrite the awaiting snapshot the relay persisted, so
// the parking service passes engineSaves=false (Service.Persist still writes via
// Config.Store). The RESUMING service passes engineSaves=true so the completed
// terminal is durable.
func newAskingService(t *testing.T, store port.SessionStore, ps *permstore.Memory, ran *atomic.Int64, llm port.LLMProvider, engineSaves bool, placement ...server.PlacementProvider) *server.Service {
	return newAskingServiceWithAwait(t, store, ps, ran, llm, engineSaves, nil, placement...)
}

func newAskingServiceWithAwait(t *testing.T, store port.SessionStore, ps *permstore.Memory, ran *atomic.Int64, llm port.LLMProvider, engineSaves bool, await func(context.Context, string, string) error, placement ...server.PlacementProvider) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: ran})
	var engineStore port.SessionStore
	if engineSaves {
		engineStore = store
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
		Store:   engineStore,
	})
	cfg := server.Config{
		Engine:             engine,
		Store:              store,
		AwaitContextWindow: await,

		Now: func() time.Time { return time.Unix(0, 0) },
	}
	if len(placement) > 0 {
		cfg.PlacementProvider = placement[0]
		cfg.PlacementScope = "test"
		cfg.SharedEngineRoot = "/ws"
	}
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// driveServiceToAwaiting starts a run on svc that will hit a Write ask, persists the
// awaiting session at the ask (modelling the relay's Persist-on-ask), captures the
// askID, then cancels svc's live run (modelling the parked run dying) and returns
// the askID. The store now holds a durable StateAwaiting snapshot.
func driveServiceToAwaiting(t *testing.T, svc *server.Service, id session.SessionID) string {
	t.Helper()
	run, err := svc.StartRun(context.Background(), id, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askID string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			svc.Persist(context.Background(), id) // durable awaiting snapshot
			run.Cancel()                          // the parked run "dies"
		}
	}
	svc.FinishRun(id, run)
	if askID == "" {
		t.Fatal("the run never raised a permission ask")
	}
	return askID
}

// TestApproveAfterRestartResumesAwaiting is the service-layer cloud-native Phase 2
// gate: an awaiting session whose process died is resumed by a DIFFERENT Service
// (over the same store) via Approve → resumeFromAwaiting; the pending Write executes
// EXACTLY ONCE and the resumed run reaches a clean StopEndTurn terminal.
func TestApproveAfterRestartResumesAwaiting(t *testing.T) {
	dir := t.TempDir()
	store1, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	ps1 := permstore.New()
	var ran1 atomic.Int64
	// svc1's engine: a Write tool call that parks awaiting (one turn only).
	svc1 := newAskingService(t, store1, ps1, &ran1,
		mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))), false)
	sess, err := svc1.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	askID := driveServiceToAwaiting(t, svc1, sess.ID)
	if ran1.Load() != 0 {
		t.Fatalf("Write executed %d time(s) pre-approval, want 0 (it must be parked at the ask)", ran1.Load())
	}

	// "Restart": a fresh Service + store over the SAME dir. Its engine has the
	// CONTINUATION turn (the tool call already happened pre-restart). A fresh ran2
	// counter proves the pending Write executes exactly once on THIS process.
	store2, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore reopen: %v", err)
	}
	ps2 := permstore.New()
	var ran2 atomic.Int64
	admissionErr := errors.New("context window unavailable")
	rejectAdmission := true
	var callbackSawPending atomic.Bool
	awaitWindow := func(context.Context, string, string) error {
		parked, loadErr := store2.Load(context.Background(), sess.ID)
		if loadErr == nil {
			pending, ok := parked.PendingAsk()
			callbackSawPending.Store(parked.State == session.StateAwaiting && ok && pending.AskID == askID)
		}
		if rejectAdmission {
			return admissionErr
		}
		return nil
	}
	continuationProvider := &providerContextCapture{provider: mockllm.New(mockllm.TextTurn("done after approval"))}
	svc2 := newAskingServiceWithAwait(t, store2, ps2, &ran2, continuationProvider, true, awaitWindow)
	forged := port.WithSessionID(port.WithRootSessionID(context.Background(), "forged-root"), "forged-active")

	if run, err := svc2.ApproveRun(forged, sess.ID, askID, session.VerdictAllowOnce, ""); !errors.Is(err, admissionErr) || run != nil {
		t.Fatalf("ApproveRun admission failure = run %v, err %v; want nil, %v", run, err, admissionErr)
	}
	if !callbackSawPending.Load() || ran2.Load() != 0 {
		t.Fatalf("admission callback ordering: pending=%t tool executions=%d", callbackSawPending.Load(), ran2.Load())
	}
	parked, err := store2.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := parked.PendingAsk()
	if parked.State != session.StateAwaiting || !ok || pending.AskID != askID {
		t.Fatalf("failed admission consumed pending approval: state=%s pending=%+v present=%t", parked.State, pending, ok)
	}
	rejectAdmission = false
	run, err := svc2.ApproveRun(forged, sess.ID, askID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatalf("ApproveRun after restart: %v", err)
	}
	if run == nil {
		t.Fatal("ApproveRun after restart returned nil run — the no-live-run rehydrate path did not fire")
	}
	var stop session.StopReason
	var writeResults int
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "w1" {
			writeResults++
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc2.FinishRun(sess.ID, run)
	continuationProvider.assertLast(t, sess.ID)

	if ran2.Load() != 1 {
		t.Fatalf("pending Write executed %d time(s) on resume, want EXACTLY 1", ran2.Load())
	}
	if writeResults != 1 {
		t.Fatalf("EvToolResult for w1 emitted %d time(s), want exactly 1", writeResults)
	}
	if stop != session.StopEndTurn {
		t.Fatalf("resumed run stop = %q, want %q", stop, session.StopEndTurn)
	}
	final, err := svc2.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession after resume: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("resumed session final state = %q, want completed", final.State)
	}
}

// TestApproveNonAwaitingYieldsNoActiveRun is the state-gate mutation oracle: a
// COMPLETED (terminal, non-awaiting) session resumed via Approve yields
// ErrNoActiveRun and is NOT re-entered. Dropping the State!=StateAwaiting gate in
// resumeFromAwaiting would let the completed session be re-entered (and fail this).
func TestApproveNonAwaitingYieldsNoActiveRun(t *testing.T) {
	dir := t.TempDir()
	store1, _ := jsonlstore.New(dir)
	ps := permstore.New()
	var ran atomic.Int64
	svc1 := newAskingService(t, store1, ps, &ran, mockllm.New(mockllm.TextTurn("done")), true)
	sess, err := svc1.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive it to a clean COMPLETED terminal and persist.
	run, err := svc1.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(t, run)
	svc1.FinishRun(sess.ID, run)

	store2, _ := jsonlstore.New(dir)
	svc2 := newAskingService(t, store2, permstore.New(), new(atomic.Int64), mockllm.New(mockllm.TextTurn("unreachable")), true)

	if err := svc2.Approve(context.Background(), sess.ID, "any-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("Approve on a COMPLETED session = %v, want ErrNoActiveRun (the state gate must hold terminal states terminal)", err)
	}
	// And an unknown session still yields ErrNotFound.
	if err := svc2.Approve(context.Background(), "no-such-session", "any-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Approve unknown = %v, want ErrNotFound", err)
	}
}

// TestApproveFailedSessionYieldsNoActiveRun is the failed-state sub-case of the
// non-awaiting state gate (the completed case is above; idle is covered by the engine
// suite). A persisted FAILED session resumed via Approve yields ErrNoActiveRun — a
// failed session is terminal-for-resume (it recovers only through a NEW prompt via
// loadAndReopen→Recover, never through the awaiting seam). It pins the gate's failed
// branch beyond branch identity.
func TestApproveFailedSessionYieldsNoActiveRun(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive it to a FAILED terminal state and persist that snapshot (no live run).
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := svc.Approve(context.Background(), sess.ID, "any-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("Approve on a FAILED session = %v, want ErrNoActiveRun (failed is terminal-for-resume; only a new prompt recovers it)", err)
	}
}

// TestApproveSameProcessUsesLiveRun confirms the SAME-PROCESS path is unchanged: a
// live registered run resolves the ask over its channel (ApproveRun returns a nil
// run — there is no new run), and the tool runs in the original run.
func TestApproveSameProcessUsesLiveRun(t *testing.T) {
	store := jsonlstoreMust(t)
	ps := permstore.New()
	var ran atomic.Int64
	svc := newAskingService(t, store, ps, &ran,
		mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`))),
			mockllm.TextTurn("done"),
		), true)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var sameProcess bool
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			// The live run is registered, so ApproveRun routes over the channel and
			// returns (nil, nil) — NOT a new resumed run.
			resumed, aerr := svc.ApproveRun(context.Background(), sess.ID, ev.Ask.AskID, session.VerdictAllowOnce, "")
			if aerr != nil {
				t.Errorf("ApproveRun (same-process): %v", aerr)
			}
			if resumed == nil {
				sameProcess = true
			} else {
				t.Error("ApproveRun on a LIVE run returned a new resumed run — the same-process channel path regressed")
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, run)
	if !sameProcess {
		t.Fatal("the same-process approve path never fired")
	}
	if ran.Load() != 1 {
		t.Fatalf("Write executed %d time(s) via the same-process approve, want 1", ran.Load())
	}
	if stop != session.StopEndTurn {
		t.Fatalf("same-process run stop = %q, want %q", stop, session.StopEndTurn)
	}
}

// TestConcurrentApproveAfterRestartExecutesOnce holds a winning resume in provisional
// admission while a concurrent stale control waits on resumeMu. The unqualified
// allow resumes and executes exactly once; after promotion the stale deny is
// rejected instead of being delivered to the newer run.
func TestConcurrentApproveAfterRestartExecutesOnce(t *testing.T) {
	dir := t.TempDir()
	store1, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	var ran1 atomic.Int64
	svc1 := newAskingService(t, store1, permstore.New(), &ran1,
		mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))), false)
	sess, err := svc1.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	askID := driveServiceToAwaiting(t, svc1, sess.ID)

	// "Restart": a fresh Service over the SAME store. The stale concurrent
	// control must never reach this continuation; ran2 is the exactly-once oracle.
	store2, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore reopen: %v", err)
	}
	var ran2 atomic.Int64
	resumeEntered := make(chan struct{})
	releaseResume := make(chan struct{})
	placement := admissionPlacementProvider{resolve: func(ctx context.Context, ref session.EnvironmentRef) (tool.Environment, error) {
		close(resumeEntered)
		select {
		case <-releaseResume:
			return tool.MustEnvironment(ref, memfs.NewWorkspace(ref.ID), memledger.New(), nil), nil
		case <-ctx.Done():
			return tool.Environment{}, ctx.Err()
		}
	}}
	svc2 := newAskingService(t, store2, permstore.New(), &ran2,
		mockllm.New(mockllm.TextTurn("done one"), mockllm.TextTurn("done two")), true, placement)

	// Hold the winning resume after it installs its provisional runState but before
	// promotion. The concurrent stale control must wait for that exact resumeMu
	// transaction; once the winner promotes its live run, the recheck must reject
	// the stale run id before it can deliver its deny verdict.
	type approvalResult struct {
		name string
		run  *agent.Run
		err  error
	}
	results := make(chan approvalResult, 2)
	go func() {
		run, err := svc2.ApproveRun(context.Background(), sess.ID, askID, session.VerdictAllowOnce, "")
		results <- approvalResult{name: "unqualified allow", run: run, err: err}
	}()
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("awaiting resume admission did not reach placement reattach")
	}
	go func() {
		run, err := svc2.ApproveRun(context.Background(), sess.ID, askID, session.VerdictDeny, "stale-run-id")
		results <- approvalResult{name: "stale deny", run: run, err: err}
	}()
	select {
	case result := <-results:
		t.Fatalf("approval escaped while awaiting resume was provisional: %s: %v", result.name, result.err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseResume)

	var winner *agent.Run
	for range 2 {
		result := <-results
		switch result.name {
		case "unqualified allow":
			if result.err != nil {
				t.Fatalf("ApproveRun(%s) = %v, want nil", result.name, result.err)
			}
			if result.run == nil {
				t.Fatalf("ApproveRun(%s) returned nil run, want the resumed run", result.name)
			}
			winner = result.run
		case "stale deny":
			if !errors.Is(result.err, server.ErrStaleRunControl) {
				t.Fatalf("ApproveRun(%s) = %v, want ErrStaleRunControl", result.name, result.err)
			}
			if result.run != nil {
				t.Fatalf("ApproveRun(%s) returned a run; stale controls must not apply", result.name)
			}
		default:
			t.Fatalf("unexpected approval result %q", result.name)
		}
	}

	// Drain the one resumed run. The unqualified legacy control succeeds exactly
	// once; the stale deny is rejected before it can reach that live run.
	drainRun(t, winner)
	svc2.FinishRun(sess.ID, winner)

	if ran2.Load() != 1 {
		t.Fatalf("pending Write executed %d time(s) under concurrent Approve, want EXACTLY 1 (the per-session resume lock must serialize the spawn)", ran2.Load())
	}
	final, err := svc2.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession after concurrent resume: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("resumed session final state = %q, want completed", final.State)
	}
}

func jsonlstoreMust(t *testing.T) port.SessionStore {
	t.Helper()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	return store
}
