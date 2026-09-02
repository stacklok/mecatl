package server_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
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
)

// TestStartRunContentAbandonsStaleRunningSession is the Step 3 regression test
// for issue #475: a session whose LAST persisted snapshot is StateRunning with
// a trailing, unanswered tool_use (the confirmed crash-orphan shape — nothing
// ever observed a cancellation or a failure) must be repaired by the run-entry
// funnel itself, not merely accepted-then-400. The assertion that actually
// matters is on the request BUILT for the repaired session's next turn: it
// must carry no unpaired tool_use, which is exactly what would have made the
// real provider reject the call with an HTTP 400.
func TestStartRunContentAbandonsStaleRunningSession(t *testing.T) {
	var mu sync.Mutex
	var captured []port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(req port.LLMRequest) {
			mu.Lock()
			captured = append(captured, req)
			mu.Unlock()
		}),
	}, mockllm.TextTurn("continuing"))

	svc, store := newServiceWithEngine(t, llm, tool.NewCatalog())

	// NOTE: this id deliberately does NOT carry a delegation-child prefix
	// (subagent-/parallel-/team-) — StartRunContent now rejects those outright
	// (see TestStartRunContentRejectsDelegationChildSessionID), so a crash-
	// orphaned TOP-LEVEL session is what this repair path actually needs to
	// handle here.
	// crashOrphanedSession (settle_test.go) builds the exact StateRunning
	// crash-orphan shape (issue #475): no Cancel, no Fail observed it.
	const id session.SessionID = "crash-orphan-475"
	if err := store.Save(context.Background(), crashOrphanedSession(t, id)); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), id, "continue please", nil)
	if err != nil {
		t.Fatalf("StartRunContent on a crash-orphaned running session: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(id, run)

	mu.Lock()
	defer mu.Unlock()
	if len(captured) == 0 {
		t.Fatal("no request reached the provider")
	}
	if err := session.ValidateToolPairing(captured[0].Messages); err != nil {
		t.Fatalf("the request built for the repaired session carries unpaired tool_use (the real HTTP-400 bug): %v", err)
	}

	reloaded, err := svc.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession after repair: %v", err)
	}
	if reloaded.State != session.StateCompleted {
		t.Fatalf("after the repaired run, state = %q, want completed", reloaded.State)
	}
}

// twoStageProvider streams a FIXED first turn (its Chunks), then on every
// subsequent call blocks until ctx is cancelled — so a run can be driven past
// one real turn (durably saved) and left live/blocked in a second one, letting
// a test observe a genuinely in-flight run whose persisted snapshot already
// reads StateRunning.
type twoStageProvider struct {
	first []port.Chunk
	calls atomic.Int32
	caps  port.ProviderCapabilities
}

func (p *twoStageProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	n := p.calls.Add(1)
	if n == 1 {
		chunks := p.first
		return func(yield func(port.Chunk, error) bool) {
			for _, c := range chunks {
				if !yield(c, nil) {
					return
				}
			}
		}, nil
	}
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (p *twoStageProvider) Capabilities() port.ProviderCapabilities { return p.caps }

var _ port.LLMProvider = (*twoStageProvider)(nil)

// countingStore wraps a port.SessionStore, counting Save calls so a test can
// assert the repair path never writes on top of a genuinely live run.
type countingStore struct {
	inner port.SessionStore
	mu    sync.Mutex
	saves int
}

func (c *countingStore) Save(ctx context.Context, s *session.Session) error {
	c.mu.Lock()
	c.saves++
	c.mu.Unlock()
	return c.inner.Save(ctx, s)
}

func (c *countingStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return c.inner.Load(ctx, id)
}

func (c *countingStore) saveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saves
}

var _ port.SessionStore = (*countingStore)(nil)

// waitForState polls store for id to reach want, failing the test if it never
// does within the deadline. It exists because turn-1's e.save (the point the
// persisted snapshot first reflects a genuine StateRunning) happens on a
// goroutine the test does not otherwise synchronize with.
func waitForState(t *testing.T, store port.SessionStore, id session.SessionID, want session.State) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		sess, err := store.Load(context.Background(), id)
		if err == nil && sess.State == want {
			return
		}
		select {
		case <-deadline:
			got := session.State("<load error>")
			if sess != nil {
				got = sess.State
			}
			t.Fatalf("session %q never reached state %q (last seen %q, err=%v)", id, want, got, err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestStartRunContentLeavesLiveRunningSessionAlone is the negative-test half of
// Step 3: a session with a GENUINELY live registered run for the same id must
// never be "repaired" by the new StateRunning branch in StartRunContent — that
// would race (or clobber) the live run's own history. It proves the guard
// investigated for Step 3 (IsLive checked before Abandon) actually fires: no
// extra Store.Save beyond the live run's own turn-1 save, and a clear
// ErrFailedPrecondition instead of a second dispatch.
func TestStartRunContentLeavesLiveRunningSessionAlone(t *testing.T) {
	call := session.NewToolCall("call-a", "unknown_tool", nil)
	prov := &twoStageProvider{first: mockllm.ToolCallTurn(call).Chunks}

	// Built directly (rather than via newServiceWithEngine) so the counting
	// wrapper is the SAME store instance the engine's Deps.Store and the
	// Service's Config.Store both close over — an accurate save count requires
	// it to be in place from construction, not layered on after the fact.
	cs := &countingStore{inner: memstore.New()}
	engine := agent.NewEngine(agent.Deps{
		LLM:     prov,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   cs,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      cs,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := svc.StartRunContent(context.Background(), sess.ID, "go", nil)
	if err != nil {
		t.Fatalf("first StartRunContent: %v", err)
	}
	t.Cleanup(func() {
		run.Cancel()
		drainRun(t, run)
		svc.FinishRun(sess.ID, run)
	})

	// Wait for turn-1 (the unknown-tool call, answered immediately) to land and
	// be saved — the store now genuinely reads StateRunning while the run is
	// blocked in turn 2's Stream call.
	waitForState(t, cs, sess.ID, session.StateRunning)
	if !svc.IsLive(sess.ID) {
		t.Fatal("precondition: the first run must still be live (blocked in turn 2)")
	}
	savesBeforeSecondCall := cs.saveCount()

	_, err = svc.StartRunContent(context.Background(), sess.ID, "second, concurrent prompt", nil)
	if !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("second StartRunContent on a LIVE running session = %v, want ErrFailedPrecondition", err)
	}
	if got := cs.saveCount(); got != savesBeforeSecondCall {
		t.Fatalf("Store.Save called %d times by the refused repair attempt, want %d (no extra write)", got, savesBeforeSecondCall)
	}
}

// TestStartRunContentRejectsDelegationChildSessionID is the ship-blocker fix
// for the panel-review finding on issue #475's Step 3: a caller with no
// special privilege — just the SAME access the parent session's owner already
// has — can learn a child's id from the `agentId:`/`Team id:` result trailer
// or InspectSubagent/InspectMember's MemberSessionID(teamID, member) scheme,
// then call the wire prompt endpoint (StartRunContent, or the HTTP/gRPC
// handlers that route to it) directly against THAT id. Service.IsLive is
// structurally blind to children (its own doc comment says so — a
// subagent/parallel/team child is driven inside its PARENT's in-process
// dispatch, never registered in s.runs), so the StateRunning crash-orphan
// repair a few lines above this test would see IsLive==false for a
// GENUINELY-LIVE child and proceed to Abandon() + Save its history out from
// under the parent — reintroducing, via direct wire access, exactly the
// dangling-tool_use/provider-400 hazard issue #475 exists to close.
//
// The fix rejects ANY subagent-/parallel-/team- prefixed id at the very top
// of StartRunContent, unconditionally — before loadAndReopen, before the
// lease/lock, before the StateRunning repair branch can even be reached. This
// test seeds a subagent-family session in the exact StateRunning
// crash-orphan shape the repair above targets and proves it is rejected
// outright, with ZERO extra Store.Save — wired through the SAME countingStore
// TestStartRunContentLeavesLiveRunningSessionAlone uses, so the "no store
// write occurred" claim is actually counted rather than merely inferred from
// the reloaded state (the reloaded-state check alone only proves no state
// change was PERSISTED as running→idle, not that Save was never called at
// all — e.g. a would-be no-op re-Save of the identical snapshot would pass it
// silently). The load-bearing assertion remains errors.Is(err,
// ErrInvalidArgument): without the guard, err would be nil and the test would
// fail there regardless of the save count.
func TestStartRunContentRejectsDelegationChildSessionID(t *testing.T) {
	for _, id := range []session.SessionID{
		"subagent-crash-orphan-475",
		"parallel-crash-orphan-475-0",
		"team-abc123-worker",
	} {
		t.Run(string(id), func(t *testing.T) {
			cs := &countingStore{inner: memstore.New()}
			llm := mockllm.New(mockllm.TextTurn("should never run"))
			engine := agent.NewEngine(agent.Deps{
				LLM:     llm,
				Catalog: tool.NewCatalog(),
				Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
				Model:   "test-model",
				Store:   cs,
			})
			svc, err := newPlacementTestService(server.Config{
				Engine:     engine,
				Store:      cs,
				Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
				Now:        func() time.Time { return time.Unix(0, 0) },
			})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}

			if err := cs.Save(context.Background(), crashOrphanedSession(t, id)); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			savesBeforeCall := cs.saveCount()

			_, err = svc.StartRunContent(context.Background(), id, "hijack this child session", nil)
			if !errors.Is(err, server.ErrInvalidArgument) {
				t.Fatalf("StartRunContent(%q) = %v, want ErrInvalidArgument", id, err)
			}
			if got := cs.saveCount(); got != savesBeforeCall {
				t.Fatalf("Store.Save called %d times by the rejected call, want %d (no extra write)", got, savesBeforeCall)
			}

			reloaded, err := svc.GetSession(context.Background(), id)
			if err != nil {
				t.Fatalf("GetSession after rejected call: %v", err)
			}
			if reloaded.State != session.StateRunning {
				t.Fatalf("rejected StartRunContent mutated the session: state = %q, want unchanged %q", reloaded.State, session.StateRunning)
			}
		})
	}
}
