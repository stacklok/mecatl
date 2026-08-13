package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// newServiceWithStore builds a Service over the given (shared) store, so two
// Services can be pointed at the same durable store to simulate a restart.
func newServiceWithStore(t *testing.T, store port.SessionStore) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestAutoResumeFromStore creates a session through one Service backed by a
// jsonlstore, then builds a NEW Service over the SAME store dir (simulating a
// process restart) and confirms GetSession resolves the session by loading it
// from the store.
func TestAutoResumeFromStore(t *testing.T) {
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}

	// Process 1: create and persist a session.
	svc1 := newServiceWithStore(t, store)
	sess, err := svc1.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Process 2: a brand-new Service + store over the same dir (restart).
	store2, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore reopen: %v", err)
	}
	svc2 := newServiceWithStore(t, store2)

	got, err := svc2.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession after restart: %v", err)
	}
	if got.ID != sess.ID || got.Workspace != "/ws" || got.Mode != session.ModeDefault {
		t.Fatalf("loaded session mismatch: %+v", got)
	}
	if got.Limits.MaxTurns != 3 {
		t.Fatalf("limits not round-tripped: %+v", got.Limits)
	}
}

// TestApproveFallsBackToStore confirms Approve/Cancel for a session present only
// in the store (no in-flight run, e.g. after a restart) return ErrNoActiveRun,
// while an unknown id returns ErrNotFound.
func TestApproveFallsBackToStore(t *testing.T) {
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}

	svc1 := newServiceWithStore(t, store)
	sess, err := svc1.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Restart: new Service, no in-flight runs.
	store2, _ := jsonlstore.New(dir)
	svc2 := newServiceWithStore(t, store2)

	// Known session, no live run -> ErrNoActiveRun.
	if err := svc2.Approve(context.Background(), sess.ID, "ask-1", session.VerdictAllowOnce); !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("Approve known/runless = %v, want ErrNoActiveRun", err)
	}
	if err := svc2.Cancel(context.Background(), sess.ID); !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("Cancel known/runless = %v, want ErrNoActiveRun", err)
	}

	// Unknown session -> ErrNotFound.
	if err := svc2.Approve(context.Background(), "does-not-exist", "ask-1", session.VerdictAllowOnce); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Approve unknown = %v, want ErrNotFound", err)
	}
}

// TestSetModePersistsAndValidates exercises the SetMode seam: it changes an idle
// session's mode and persists it, rejects an empty mode (ErrInvalidArgument) and
// an unknown session (ErrNotFound).
func TestSetModePersistsAndValidates(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := svc.SetMode(context.Background(), sess.ID, session.ModePlan)
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got.Mode != session.ModePlan {
		t.Fatalf("returned mode = %q, want plan", got.Mode)
	}
	// Persisted: a fresh load reflects the change.
	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.Mode != session.ModePlan {
		t.Fatalf("persisted mode = %q, want plan", reloaded.Mode)
	}

	// Empty mode -> ErrInvalidArgument.
	if _, err := svc.SetMode(context.Background(), sess.ID, ""); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("SetMode empty = %v, want ErrInvalidArgument", err)
	}
	// Unknown session -> ErrNotFound.
	if _, err := svc.SetMode(context.Background(), "nope", session.ModePlan); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("SetMode unknown = %v, want ErrNotFound", err)
	}
}

// TestLoadSessionReopensCompleted confirms LoadSession reopens a completed
// session to idle (preserving history) so it can run again, and returns
// ErrNotFound for an unknown id.
func TestLoadSessionReopensCompleted(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive it to a completed terminal state and persist that snapshot.
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := svc.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (reopened)", loaded.State)
	}

	if _, err := svc.LoadSession(context.Background(), "missing"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("LoadSession unknown = %v, want ErrNotFound", err)
	}
}

// blockingTool signals it has started, then blocks until the run's context is
// cancelled — so a run can be driven to StateCancelled mid-dispatch (after the
// assistant tool-call is recorded, before its result).
type blockingTool struct{ started chan struct{} }

func (*blockingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "Read: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*blockingTool) ReadOnly() bool { return true }
func (b *blockingTool) Execute(ctx context.Context, _ session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	close(b.started)
	<-ctx.Done()
	return session.ToolResult{}, ctx.Err()
}

var _ tool.Tool = (*blockingTool)(nil)

// newServiceWithEngine builds a Service over a memstore whose engine uses the
// given LLM + catalog, so a run can be driven to a real cancelled state through
// the Service surface.
func newServiceWithEngine(t *testing.T, llm port.LLMProvider, cat *tool.Catalog) (*server.Service, port.SessionStore) {
	t.Helper()
	store := memstore.New()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

// TestStartRunContentRecoversCancelledSession is THE regression test: a run is
// driven to StateCancelled (blocking tool + Cancel), then a SECOND StartRunContent
// on the same session must NOT return the "record user prompt … from cancelled"
// wedge error and must produce a terminal result.
func TestStartRunContentRecoversCancelledSession(t *testing.T) {
	bt := &blockingTool{started: make(chan struct{})}
	cat := tool.NewCatalog()
	cat.MustRegister(bt)
	// Turn-1: a tool call to the blocking tool. Turn-2: a clean end-of-turn.
	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		mockllm.ChunksTurn(mockllm.TextChunk("all done"), mockllm.DoneChunk(session.StopEndTurn)),
	)
	svc, _ := newServiceWithEngine(t, llm, cat)

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), sess.ID, "look at a.go", nil)
	if err != nil {
		t.Fatalf("first StartRunContent: %v", err)
	}
	<-bt.started
	run.Cancel()
	drainRun(t, run)

	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.State != session.StateCancelled {
		t.Fatalf("after cancel state = %q, want cancelled", reloaded.State)
	}

	// The regression: a second prompt must be accepted, not wedged.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "second prompt", nil)
	if err != nil {
		t.Fatalf("second StartRunContent returned error (wedge?): %v", err)
	}
	var sawResult bool
	for ev := range run2.Events() {
		if ev.Type == session.EvResult {
			sawResult = true
			if ev.Result.Stop != session.StopEndTurn {
				t.Fatalf("second run stop = %q, want end_turn", ev.Result.Stop)
			}
		}
	}
	if !sawResult {
		t.Fatalf("second run produced no terminal result")
	}
}

// TestLoadSessionRecoversCancelledViaInterrupt confirms a persisted cancelled
// session is recovered to StateIdle (via Interrupt) and re-persisted.
func TestLoadSessionRecoversCancelledViaInterrupt(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive to a cancelled terminal state with an orphaned tool call, persist it.
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{session.NewToolCall("c1", "Read", nil)}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := sess.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := svc.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (interrupted)", loaded.State)
	}
	// History was repaired: the orphaned c1 now has a synthetic error result.
	var found bool
	for _, m := range loaded.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && m.ToolResult.IsError {
			found = true
		}
	}
	if !found {
		t.Fatalf("interrupted history missing synthetic result for c1")
	}
	// Persisted: a fresh load reflects StateIdle.
	persisted, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.State != session.StateIdle {
		t.Fatalf("persisted state = %q, want idle", persisted.State)
	}
}

// TestLoadSessionRecoversFailedViaRecover confirms a persisted FAILED session
// (the store round-trip case, e.g. after a process restart) is recovered to
// StateIdle via Recover and re-persisted as idle (issue #51). This is the
// inverse of the pre-#51 assertion that a failed session stayed StateFailed.
func TestLoadSessionRecoversFailedViaRecover(t *testing.T) {
	store := memstore.New()
	svc := newServiceWithStore(t, store)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
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

	loaded, err := svc.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (recovered)", loaded.State)
	}
	// History preserved across the recovery (context not lost).
	if len(loaded.Conversation.Messages) == 0 {
		t.Fatalf("recovered session lost its conversation history")
	}
	// Re-persisted: a fresh load reflects StateIdle.
	persisted, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.State != session.StateIdle {
		t.Fatalf("persisted state = %q, want idle", persisted.State)
	}
}

// echoTool is a trivially-succeeding read-only tool, used to record REAL tool
// work in a turn that precedes a provider failure. Innocuous by design (the
// no-destructive-literals rule).
type echoTool struct{}

func (*echoTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "Read: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*echoTool) ReadOnly() bool { return true }
func (*echoTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(in.ID, "file contents"), nil
}

var _ tool.Tool = (*echoTool)(nil)

// TestStartRunContentRecoversFailedSession is THE regression test for issue
// #51, the failed-session sibling of TestStartRunContentRecoversCancelledSession:
// a run is driven to StateFailed by a GENUINE mid-stream provider error
// (mockllm.ErrorTurn — a real iterator error, the transient-5xx shape, NOT a
// provider-reported StopError on a clean ChunkDone), which the loop maps to
// terminate(StopError) → session.Fail(). A SECOND StartRunContent on the same
// session must then NOT return the `RecordUserPrompt from "failed"` wedge error
// and must produce a terminal result.
//
// Nothing here is model-facing: the recovery is harness-internal (the model
// simply sees a normal next turn with the prior conversation intact — no tool
// surface, no handle, no result-text change). And recovery makes retry
// POSSIBLE, not guaranteed: a permanent-cause failure would simply fail again,
// which is acceptable.
func TestStartRunContentRecoversFailedSession(t *testing.T) {
	llm := mockllm.New(
		// Turn-1: a genuine mid-stream provider failure (transient-outage shape).
		mockllm.ErrorTurn(errors.New("upstream 502: bad gateway")),
		// Turn-2 (the retry, post-recovery): a clean end-of-turn.
		mockllm.TextTurn("recovered and done"),
	)
	svc, _ := newServiceWithEngine(t, llm, tool.NewCatalog())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), sess.ID, "first prompt", nil)
	if err != nil {
		t.Fatalf("first StartRunContent: %v", err)
	}
	var firstStop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult {
			firstStop = ev.Result.Stop
		}
	}
	if firstStop != session.StopError {
		t.Fatalf("first run stop = %q, want %q (terminal failure)", firstStop, session.StopError)
	}
	// Proof the stream error actually Fail()ed the session (not a clean terminal).
	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.State != session.StateFailed {
		t.Fatalf("after stream error state = %q, want failed", reloaded.State)
	}

	// The regression: a second prompt must be accepted, not wedged.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "second prompt", nil)
	if err != nil {
		t.Fatalf("second StartRunContent returned error (wedge?): %v", err)
	}
	var sawResult bool
	for ev := range run2.Events() {
		if ev.Type == session.EvResult {
			sawResult = true
			if ev.Result.Stop != session.StopEndTurn {
				t.Fatalf("second run stop = %q, want end_turn", ev.Result.Stop)
			}
		}
	}
	if !sawResult {
		t.Fatalf("second run produced no terminal result")
	}
}

// TestStartRunContentRecoversFailedSessionAfterToolWork is the observed-live
// shape: real tool work happens (turn-1 tool call + result recorded), THEN the
// provider fails terminally mid-run (turn-2 stream error) — and the retry after
// recovery must keep the earlier work's history (context not lost).
func TestStartRunContentRecoversFailedSessionAfterToolWork(t *testing.T) {
	cat := tool.NewCatalog()
	cat.MustRegister(&echoTool{})
	llm := mockllm.New(
		// Run-1, model call 1: a tool call (real work, recorded + answered).
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		// Run-1, model call 2: a genuine mid-stream provider failure.
		mockllm.ErrorTurn(errors.New("upstream 503: service unavailable")),
		// Run-2 (post-recovery retry): a clean end-of-turn.
		mockllm.TextTurn("picked up where we left off"),
	)
	svc, _ := newServiceWithEngine(t, llm, cat)

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "look at a.go", nil)
	if err != nil {
		t.Fatalf("first StartRunContent: %v", err)
	}
	drainRun(t, run)

	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.State != session.StateFailed {
		t.Fatalf("after stream error state = %q, want failed", reloaded.State)
	}

	run2, err := svc.StartRunContent(context.Background(), sess.ID, "carry on", nil)
	if err != nil {
		t.Fatalf("second StartRunContent returned error (wedge?): %v", err)
	}
	var sawResult bool
	for ev := range run2.Events() {
		if ev.Type == session.EvResult {
			sawResult = true
			if ev.Result.Stop != session.StopEndTurn {
				t.Fatalf("second run stop = %q, want end_turn", ev.Result.Stop)
			}
		}
	}
	if !sawResult {
		t.Fatalf("second run produced no terminal result")
	}
	// The turn-1 tool work survived recovery: its result is still on history.
	final, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession final: %v", err)
	}
	var foundToolResult bool
	for _, m := range final.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && !m.ToolResult.IsError {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatalf("turn-1 tool result for c1 missing after recovery (context lost)")
	}
}

// TestRecoverAdversarialMidDispatchFailure is the adversarial shape: the run
// failed with an IN-FLIGHT unanswered tool call on the trailing assistant
// message (the mid-DISPATCH failure shape — RecordAssistant succeeded but the
// results never landed — the only real loop path that orphans on failure; a
// mid-STREAM failure discards the partial turn instead, see
// TestRecoverMidStreamFailureReplayIsPaired). loadAndReopen must recover the
// session AND repair the history — the synthetic error result closes the
// orphan so the replay passes ValidateToolPairing (no provider 400) — and the
// repaired history must reach the PROVIDER intact: the post-recovery run's
// observed port.LLMRequest carries the failure-accurate synthetic close-out
// (never the cancellation wording: no user cancelled anything) and is
// pairing-valid.
func TestRecoverAdversarialMidDispatchFailure(t *testing.T) {
	var mu sync.Mutex
	var reqs []port.LLMRequest
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			mu.Lock()
			defer mu.Unlock()
			reqs = append(reqs, r)
		})},
		// The post-recovery retry: a clean end.
		mockllm.TextTurn("retried fine"),
	)
	svc, store := newServiceWithEngine(t, llm, tool.NewCatalog())
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive to a failed terminal state with an orphaned tool call, persist it.
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	calls := []session.ToolCall{session.NewToolCall("c1", "Read", nil)}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", calls)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := sess.Fail(); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := svc.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.State != session.StateIdle {
		t.Fatalf("loaded state = %q, want idle (recovered)", loaded.State)
	}
	// History was repaired: the orphaned c1 now has a synthetic error result
	// with the FAILURE-accurate wording — the cancellation text would falsely
	// attribute a user action (the childAutoDenyMessage accuracy discipline).
	const wantCloseOut = "tool call aborted: the run failed before this call's result was recorded"
	var found bool
	for _, m := range loaded.Conversation.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && m.ToolResult.IsError {
			found = true
			if m.ToolResult.Content != wantCloseOut {
				t.Fatalf("synthetic close-out content = %q, want %q", m.ToolResult.Content, wantCloseOut)
			}
		}
	}
	if !found {
		t.Fatalf("recovered history missing synthetic result for c1")
	}
	// The repaired history is provider-replayable.
	if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
		t.Fatalf("recovered history fails tool pairing: %v", err)
	}

	// And the production repair output reaches the PROVIDER: run the recovered
	// session and assert on the OBSERVED request the model was actually sent.
	run, err := svc.StartRunContent(context.Background(), sess.ID, "carry on", nil)
	if err != nil {
		t.Fatalf("StartRunContent after recovery: %v", err)
	}
	drainRun(t, run)
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) == 0 {
		t.Fatalf("no LLMRequest observed for the post-recovery run")
	}
	req := reqs[len(reqs)-1]
	if err := session.ValidateToolPairing(req.Messages); err != nil {
		t.Fatalf("observed replayed request fails tool pairing: %v", err)
	}
	var replayed bool
	for _, m := range req.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && m.ToolResult.IsError {
			replayed = true
			if m.ToolResult.Content != wantCloseOut {
				t.Fatalf("replayed synthetic close-out = %q, want %q", m.ToolResult.Content, wantCloseOut)
			}
		}
	}
	if !replayed {
		t.Fatalf("observed request missing the synthetic close-out for c1")
	}
}

// TestStartRunContentRecoversAcrossRepeatedFailures pins the documented
// "a permanent-cause failure re-fails cleanly" claim: fail → recover → fail
// AGAIN → recover → succeed. The second failure must land in StateFailed just
// as cleanly as the first (no half-recovered wedge), and the third prompt must
// still be accepted and complete.
func TestStartRunContentRecoversAcrossRepeatedFailures(t *testing.T) {
	llm := mockllm.New(
		mockllm.ErrorTurn(errors.New("upstream 502: bad gateway")),
		mockllm.ErrorTurn(errors.New("upstream 502: bad gateway, still")),
		mockllm.TextTurn("third time lucky"),
	)
	svc, _ := newServiceWithEngine(t, llm, tool.NewCatalog())
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// failRun starts a run that must be ACCEPTED (the prior recovery worked) and
	// end in a terminal StopError, landing the session back in StateFailed.
	failRun := func(label, prompt string) {
		t.Helper()
		run, err := svc.StartRunContent(context.Background(), sess.ID, prompt, nil)
		if err != nil {
			t.Fatalf("%s StartRunContent: %v", label, err)
		}
		var stop session.StopReason
		for ev := range run.Events() {
			if ev.Type == session.EvResult {
				stop = ev.Result.Stop
			}
		}
		if stop != session.StopError {
			t.Fatalf("%s stop = %q, want %q", label, stop, session.StopError)
		}
		loaded, err := svc.GetSession(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("%s GetSession: %v", label, err)
		}
		if loaded.State != session.StateFailed {
			t.Fatalf("%s state = %q, want failed (a re-failure lands cleanly)", label, loaded.State)
		}
	}

	failRun("first run", "first prompt")
	failRun("second run (after first recovery)", "second prompt")

	// Third prompt: recovered again, and this time the provider cooperates.
	run3, err := svc.StartRunContent(context.Background(), sess.ID, "third prompt", nil)
	if err != nil {
		t.Fatalf("third StartRunContent returned error (wedge?): %v", err)
	}
	var sawResult bool
	for ev := range run3.Events() {
		if ev.Type == session.EvResult {
			sawResult = true
			if ev.Result.Stop != session.StopEndTurn {
				t.Fatalf("third run stop = %q, want end_turn", ev.Result.Stop)
			}
		}
	}
	if !sawResult {
		t.Fatalf("third run produced no terminal result")
	}
}

// TestRecoverMidStreamFailureReplayIsPaired drives a failure through the REAL
// loop with a PARTIALLY-STREAMED turn — text and a tool-call chunk reach the
// loop, THEN the stream errors — then recovers and re-runs with a request
// observer. It pins, in one pass:
//   - mockllm.ErrorTurn's chunks-THEN-error ordering through the real consumer
//     (the partial text's message.delta is observed on run-1);
//   - the loop's discard semantics: runTurn returns an EMPTY message on a
//     stream error, so the partially-streamed tool call c2 is NEVER recorded —
//     a mid-stream failure cannot orphan a tool call by itself (the orphaning
//     failure shape is mid-DISPATCH, covered by
//     TestRecoverAdversarialMidDispatchFailure);
//   - the production repair input/output at the provider boundary: the
//     post-recovery OBSERVED port.LLMRequest passes ValidateToolPairing,
//     replays the answered c1 pair, and carries no phantom c2.
func TestRecoverMidStreamFailureReplayIsPaired(t *testing.T) {
	cat := tool.NewCatalog()
	cat.MustRegister(&echoTool{})
	var mu sync.Mutex
	var reqs []port.LLMRequest
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			mu.Lock()
			defer mu.Unlock()
			reqs = append(reqs, r)
		})},
		// Run-1, model call 1: real tool work (recorded + answered).
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))),
		// Run-1, model call 2: a partially-streamed turn, then the error.
		mockllm.ErrorTurn(errors.New("upstream 502: bad gateway"),
			mockllm.TextChunk("partial answer"),
			mockllm.ToolCallChunk(session.NewToolCall("c2", "Read", json.RawMessage(`{"path":"b.go"}`)))),
		// Run-2 (post-recovery): a clean end.
		mockllm.TextTurn("after recovery"),
	)
	svc, _ := newServiceWithEngine(t, llm, cat)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), sess.ID, "look at a.go", nil)
	if err != nil {
		t.Fatalf("first StartRunContent: %v", err)
	}
	var sawPartialDelta bool
	var firstStop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvMessageDelta && ev.Text == "partial answer" {
			sawPartialDelta = true
		}
		if ev.Type == session.EvResult {
			firstStop = ev.Result.Stop
		}
	}
	// Chunks were yielded BEFORE the error (ErrorTurn's ordering contract).
	if !sawPartialDelta {
		t.Fatalf("partial text delta not observed before the stream error (ErrorTurn must yield chunks first)")
	}
	if firstStop != session.StopError {
		t.Fatalf("first run stop = %q, want %q", firstStop, session.StopError)
	}
	reloaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if reloaded.State != session.StateFailed {
		t.Fatalf("after stream error state = %q, want failed", reloaded.State)
	}
	// Discard semantics: the partially-streamed turn was NOT recorded, so no
	// assistant message carries c2 and the failed history is already paired.
	for _, m := range reloaded.Conversation.Messages {
		for _, c := range m.ToolCalls {
			if c.ID == "c2" {
				t.Fatalf("partially-streamed tool call c2 was recorded; a failed stream must discard the partial turn")
			}
		}
	}

	run2, err := svc.StartRunContent(context.Background(), sess.ID, "carry on", nil)
	if err != nil {
		t.Fatalf("second StartRunContent returned error (wedge?): %v", err)
	}
	drainRun(t, run2)

	// The post-recovery request the model ACTUALLY received is pairing-valid,
	// replays the real turn-1 work, and carries no phantom from the discarded turn.
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 3 {
		t.Fatalf("observed %d LLMRequests, want 3 (two in run-1, one in run-2)", len(reqs))
	}
	req := reqs[len(reqs)-1]
	if err := session.ValidateToolPairing(req.Messages); err != nil {
		t.Fatalf("observed replayed request fails tool pairing: %v", err)
	}
	var sawC1Result bool
	for _, m := range req.Messages {
		if m.Role == session.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "c1" && !m.ToolResult.IsError {
			sawC1Result = true
		}
		for _, c := range m.ToolCalls {
			if c.ID == "c2" {
				t.Fatalf("observed request replays the discarded partial tool call c2")
			}
		}
	}
	if !sawC1Result {
		t.Fatalf("observed request missing the turn-1 tool result for c1 (context lost)")
	}
}
