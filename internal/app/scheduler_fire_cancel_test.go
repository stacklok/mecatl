package app

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// blockingFireProvider streams nothing until its ctx is cancelled, then ends the
// stream with StopCancelled — so a fire run on it stays live (StateRunning) until
// something cancels it (here Service.Close's run.Cancel). It mirrors the
// blockingProvider seam in internal/adapter/server/lease_test.go but lives in the
// app package so the fire-cancel test can drive makeFireFunc without crossing the
// package boundary.
type blockingFireProvider struct{}

func (blockingFireProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done() // block until the run is cancelled.
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (blockingFireProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

var _ port.LLMProvider = blockingFireProvider{}

// TestFireCancelledByShutdownLeavesRecoverableTerminal is the issue #388 Task #6
// gate: a scheduled fire whose run is cancelled by Service.Close (shutdown) must
// leave a RECOVERABLE TERMINAL session snapshot (cancelled, Interrupt-recoverable)
// in the durable store, AND RecordFire must set LastFireSessionID to the real
// fire/session id with Stop=StopCancelled — not leave the session StateRunning
// and the schedule LastFireSessionID "pending".
//
// It builds a *server.Service over a blocking provider (a fire run on it stays
// StateRunning until cancelled), wires the real makeFireFunc into a scheduler over
// a jsonlstore schedule store, drives a fire via FireNow (synchronous, blocks in
// the LLM call), cancels the in-flight run via Service.Close (Task #3's
// run.Cancel), then asserts:
//
//  1. the persisted fire session's State is cancelled (terminal + recoverable),
//     NOT running;
//  2. the schedule's LastFireSessionID is the real session id (not "pending");
//  3. the recorded fire's Stop is StopCancelled.
//
// The engine and the Service share the SAME jsonlstore, so the agent loop's own
// terminal save() (engine/agent terminate→save) AND the fire body's
// persistFireTerminalSnapshot both write the terminal snapshot — this variant
// proves the end-to-end end state. See TestFireCancelledPersistsViaFireBodyNotLoop
// for the variant that isolates the fire-body persist as the terminal writer.
//
// Fully offline: blockingFireProvider + jsonlstore, no network, no API key.
func TestFireCancelledByShutdownLeavesRecoverableTerminal(t *testing.T) {
	runFireCancelTest(t, true)
}

// TestFireCancelledPersistsViaFireBodyNotLoop isolates the issue #388 Task #6 fix:
// the fire body's persistFireTerminalSnapshot (Service.Persist from makeFireFunc
// after the Events loop) must persist the terminal snapshot EVEN WHEN the agent
// loop's own save() cannot. It wires the engine with a NIL store (so the loop's
// save() is a no-op — engine/agent loop.go's save() returns early when
// deps.Store == nil) while the Service keeps the real jsonlstore (so
// CreateSessionWithProfile persists the session and Service.Persist writes the
// terminal state). Without the fire-body persist, the durable session would stay
// StateRunning (the loop never persisted terminal) and the test fails; WITH the
// fix, persistFireTerminalSnapshot writes the cancelled snapshot before the fire
// returns.
//
// This models the real exit-race the fix closes: in production the loop's save()
// races process teardown after Service.Close, and a slow store may lose it; the
// fire-body persist makes the terminal snapshot durable BEFORE the fire returns
// (synchronized with the scheduler's Stop firesWG join / the FireNow caller).
func TestFireCancelledPersistsViaFireBodyNotLoop(t *testing.T) {
	runFireCancelTest(t, false)
}

// runFireCancelTest is the shared harness for the two fire-cancel variants. When
// shareStore is true the engine and Service use the same jsonlstore (the loop's
// save() and the fire-body persist both write terminal); when false the engine
// gets a NIL store so ONLY the fire-body persist can write the terminal snapshot,
// isolating the Task #6 fix.
func runFireCancelTest(t *testing.T, shareStore bool) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	schedStore := store.ScheduleStore()
	const schedName = "cancel-on-shutdown"
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "run until cancelled",
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
			Trigger:        port.TriggerSpec{OneShot: time.Now().Add(-1 * time.Second)}, // already due
		},
		State: port.ScheduleState{
			NextFireAt: time.Now().Add(-1 * time.Second), // due now (Claim's due-check needs NextFireAt <= now)
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	llm := blockingFireProvider{}
	// The engine's Store governs ONLY the loop's terminal save() (engine/agent
	// loop.go save()). A nil store makes that save a no-op, so the terminal
	// snapshot reaches the durable store ONLY via the fire body's
	// persistFireTerminalSnapshot (Service.Persist over svc.cfg.Store). The
	// shared-store variant lets both paths write (the end-to-end end state).
	engineStore := port.SessionStore(store)
	if !shareStore {
		engineStore = nil
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), permstore.New()),
		Model:   "test-model",
		Store:   engineStore,
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               store, // the Service store is ALWAYS the real one (create + Persist use it)
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sched := scheduler.New(scheduler.Config{
		Store:       schedStore,
		Clock:       wallclock.Clock{},
		Diagnostics: port.NopDiagnostics{},
		Fire:        makeFireFunc(svc, schedStore, defaultFireTimeout, nil),
	})
	// FireNow is leadership-gated; a nil-lease scheduler is always the leader, so
	// Start is not required to drive a manual fire. Do NOT call svc.Close in
	// Cleanup — the test calls it explicitly to simulate shutdown.

	// Drive the fire in a goroutine: FireNow is synchronous, so it blocks until
	// the run terminates. The blocking provider keeps the run live until the
	// service cancels it.
	fireDone := make(chan struct{})
	var fire port.ScheduleFire
	var fireErr error
	go func() {
		fire, fireErr = sched.FireNow(ctx, schedName, time.Now())
		close(fireDone)
	}()

	// Wait for the fire's run to be live (blocking in the LLM call) before
	// cancelling it. Without this, Close could race ahead of StartRunContent and
	// cancel a run that never started (a different failure mode than the one this
	// test targets). RecordFireStart (issue #386) flips LastFireSessionID off the
	// "pending" sentinel to the REAL session id before the run starts, so poll for
	// that (a non-pending, non-empty id means the fire path has reached
	// RecordFireStart and is about to enter StartRunContent), then a short
	// headroom for the run to enter Stream (StateRunning).
	if !eventually(5*time.Second, func() bool {
		loaded, lerr := schedStore.Load(ctx, schedName)
		if lerr != nil {
			return false
		}
		sid := string(loaded.State.LastFireSessionID)
		return sid != "" && sid != "pending"
	}) {
		t.Fatal("RecordFireStart did not stamp the real LastFireSessionID within 5s (fire did not start)")
	}
	time.Sleep(200 * time.Millisecond)

	// Simulate shutdown: Close cancels every in-flight run (Task #3). The fire's
	// blocking provider unblocks on ctx cancel and the run terminates.
	svc.Close()

	// Join the fire goroutine. FireNow returns once RecordFire has run.
	select {
	case <-fireDone:
	case <-time.After(10 * time.Second):
		t.Fatal("FireNow did not return within 10s after Close (run did not unwind)")
	}

	if fireErr != nil {
		t.Fatalf("FireNow returned error: %v", fireErr)
	}

	// (3) The recorded fire's stop is StopCancelled.
	if fire.Stop != session.StopCancelled {
		t.Errorf("fire stop = %q, want %q (a shutdown-cancelled fire must record StopCancelled)", fire.Stop, session.StopCancelled)
	}

	// (2) The schedule's LastFireSessionID is the real session id, not "pending".
	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load schedule after fire: %v", err)
	}
	sid := string(loaded.State.LastFireSessionID)
	if sid == "" || sid == "pending" {
		t.Fatalf("schedule LastFireSessionID = %q, want the real fire/session id (RecordFire must overwrite the pending sentinel)", sid)
	}
	if sid != string(fire.SessionID) {
		t.Errorf("schedule LastFireSessionID = %q, fire.SessionID = %q (they must match)", sid, fire.SessionID)
	}

	// (1) The persisted fire session is terminal (cancelled), NOT running.
	sess, err := store.Load(ctx, loaded.State.LastFireSessionID)
	if err != nil {
		t.Fatalf("Load fire session %q: %v", loaded.State.LastFireSessionID, err)
	}
	if sess.State != session.StateCancelled {
		t.Errorf("fire session state = %q, want %q (a shutdown-cancelled fire must leave a terminal, recoverable snapshot)",
			sess.State, session.StateCancelled)
	}
	// Recoverability: a cancelled session is Interrupt-recoverable (cancelled→idle).
	// Pin that the aggregate accepts Interrupt from the persisted cancelled state,
	// so a later loadAndReopen recovers it (the cloud-native Phase 1 guarantee).
	if err := sess.Interrupt(); err != nil {
		t.Errorf("Interrupt on the persisted cancelled session failed: %v (the snapshot must be Interrupt-recoverable)", err)
	}
}
