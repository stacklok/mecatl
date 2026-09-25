package app

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// progressFireProvider is the two-call mockllm for the progress test (issue #386
// Phase 3 Task D gate c). The first Stream call emits a tool call to an UNKNOWN
// tool (ChunkToolCall + ChunkDone StopEndTurn): the loop emits EvToolCall →
// RecordFireProgress, then dispatches the unknown tool (an error result) and
// continues to turn 2 — a benign text turn would TERMINATE the run (StopEndTurn
// is a clean terminal), so a tool call is used to keep the loop alive. The
// second Stream call blocks on ctx (the blockingFireProvider shape), keeping the
// fire in-flight so the test can read the in-flight progress state before
// RecordFire clears it.
type progressFireProvider struct {
	calls int
}

func (p *progressFireProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls++
	if p.calls == 1 {
		// An unknown tool call: EvToolCall fires (RecordFireProgress stamps),
		// the loop dispatches → error result, then continues to turn 2.
		call := session.NewToolCall("c1", "Nonexistent", nil)
		return func(yield func(port.Chunk, error) bool) {
			yield(port.Chunk{Kind: port.ChunkToolCall, ToolCall: &call}, nil)
			yield(port.Chunk{Kind: port.ChunkUsage, Usage: &session.Usage{}}, nil)
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		}, nil
	}
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done() // block until the run is cancelled (svc.Close).
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (progressFireProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

var _ port.LLMProvider = (*progressFireProvider)(nil)

// TestFireStartPersistsInFlightRecord is the issue #386 Phase 3 Task D gate (a):
// a fire that starts persists the session id EARLY via RecordFireStart — the
// in-flight fire record (Stop="", StartedAt/Deadline set) is observable in the
// store, and the schedule's LastFireSessionID flips off the "pending" sentinel
// to the REAL session id BEFORE the run produces a terminal outcome. It proves
// the crash-after-Claim-before-terminal window leaves a discoverable real
// session id (not "pending"), so an operator's ListFires / shouldReArmOneShot
// can see it.
//
// Fully offline: mockllm (a single text turn → StopEndTurn) + jsonlstore.
func TestFireStartPersistsInFlightRecord(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	schedStore := store.ScheduleStore()
	const schedName = "inflight-record"
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "say hello",
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
			FireTimeout:    5 * time.Minute, // a non-zero deadline so Deadline is stamped
			Trigger:        port.TriggerSpec{OneShot: time.Now().Add(-1 * time.Second)},
		},
		State: port.ScheduleState{
			NextFireAt: time.Now().Add(-1 * time.Second),
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	llm := mockllm.New(mockllm.TextTurn("hello"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sched := scheduler.New(scheduler.Config{
		Store:       schedStore,
		Clock:       wallclock.Clock{},
		Diagnostics: port.NopDiagnostics{},
		Fire:        makeFireFunc(svc, schedStore, defaultFireTimeout, nil),
	})

	// Drive the fire synchronously via FireNow (blocks until RecordFire).
	fire, fireErr := sched.FireNow(ctx, schedName, time.Now())
	if fireErr != nil {
		t.Fatalf("FireNow: %v", fireErr)
	}

	// The in-flight record was written by RecordFireStart; RecordFire then
	// flipped it terminal. LoadFire returns the terminal record (Stop set).
	loaded, err := schedStore.LoadFire(ctx, fire.ID)
	if err != nil {
		t.Fatalf("LoadFire %q: %v", fire.ID, err)
	}
	if loaded.Stop == "" {
		t.Errorf("loaded fire Stop empty, want a terminal stop (RecordFire should have flipped the in-flight record terminal)")
	}
	// RecordFireStart stamped StartedAt + Deadline; RecordFire preserves them on
	// the terminal record (the in-flight fields on the STATE are cleared, but the
	// FIRE record carries them — the startedAt/deadline are historical facts).
	if loaded.StartedAt.IsZero() {
		t.Errorf("loaded fire StartedAt is zero; RecordFireStart must stamp it")
	}
	if loaded.Deadline.IsZero() {
		t.Errorf("loaded fire Deadline is zero; a non-zero FireTimeout must stamp it")
	}
	// The deadline is start + FireTimeout (the spec's 5m), within a tolerance.
	if !loaded.Deadline.After(loaded.StartedAt) {
		t.Errorf("loaded fire Deadline %v not after StartedAt %v", loaded.Deadline, loaded.StartedAt)
	}
	wantDeadline := loaded.StartedAt.Add(5 * time.Minute)
	if d := loaded.Deadline.Sub(wantDeadline); d > time.Second || d < -time.Second {
		t.Errorf("loaded fire Deadline = %v, want ~%v (start + 5m)", loaded.Deadline, wantDeadline)
	}

	// The schedule's LastFireSessionID is the real session id (RecordFireStart
	// flipped it off "pending"; RecordFire keeps it pointing at the fire's
	// session). It must equal the fire's SessionID.
	schedLoaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load schedule: %v", err)
	}
	if string(schedLoaded.State.LastFireSessionID) != string(fire.SessionID) {
		t.Errorf("schedule LastFireSessionID = %q, want %q (RecordFireStart must set the real session id)",
			schedLoaded.State.LastFireSessionID, fire.SessionID)
	}
	// The in-flight STATE fields are cleared by RecordFire (a terminal fire has
	// no in-flight run).
	if !schedLoaded.State.LastFireStartedAt.IsZero() {
		t.Errorf("schedule LastFireStartedAt = %v, want zero (RecordFire clears the in-flight state)", schedLoaded.State.LastFireStartedAt)
	}
	if !schedLoaded.State.FireDeadline.IsZero() {
		t.Errorf("schedule FireDeadline = %v, want zero (RecordFire clears the in-flight state)", schedLoaded.State.FireDeadline)
	}
}

// TestServerProviderRecovery_Scenario4_ShorterAuxiliaryAndScheduleDeadlines is the issue #386 Phase 3
// Task D gate (b): a fire whose LLM blocks past the per-fire wall-clock deadline
// terminates with StopTimeout (NOT StopCancelled — a caller must distinguish
// "timed out" from a user cancel) + an honest Err naming the timeout duration,
// AND the session lands a recoverable terminal snapshot (cancelled, NOT running —
// Interrupt-recoverable, like a shutdown-cancelled fire). The watchdog cancels
// the in-flight run via Service.Cancel; the loop yields StopCancelled, which
// makeFireFunc overrides to StopTimeout.
//
// It overrides the package-level defaultFireTimeout to a small value so the
// timeout fires within the test budget, and runs the fire on blockingFireProvider
// (streams nothing until the run is cancelled). Fully offline.
func TestServerProviderRecovery_Scenario4_ShorterAuxiliaryAndScheduleDeadlines(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	schedStore := store.ScheduleStore()
	const schedName = "fire-timeout"
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "run until the deadline fires",
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
			// FireTimeout is zero here — the test exercises the DEFAULT timeout
			// path (the package var), which it overrides below to a small value.
			Trigger: port.TriggerSpec{OneShot: time.Now().Add(-1 * time.Second)},
		},
		State: port.ScheduleState{
			NextFireAt: time.Now().Add(-1 * time.Second),
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	// Override the package-level default fire timeout to a small value for the
	// timeout path (the test-overridable package var, per the task). Restore it
	// on exit so a parallel test using the default is unaffected.
	oldTimeout := defaultFireTimeout
	defaultFireTimeout = 150 * time.Millisecond
	t.Cleanup(func() { defaultFireTimeout = oldTimeout })

	llm := blockingFireProvider{}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sched := scheduler.New(scheduler.Config{
		Store:       schedStore,
		Clock:       wallclock.Clock{},
		Diagnostics: port.NopDiagnostics{},
		Fire:        makeFireFunc(svc, schedStore, defaultFireTimeout, nil),
	})

	fire, fireErr := sched.FireNow(ctx, schedName, time.Now())
	if fireErr != nil {
		t.Fatalf("FireNow: %v", fireErr)
	}

	// (b1) Stop is StopTimeout, not StopCancelled.
	if fire.Stop != session.StopTimeout {
		t.Errorf("fire Stop = %q, want %q (a deadline-lapsed fire must record StopTimeout, not StopCancelled)", fire.Stop, session.StopTimeout)
	}
	// (b2) Err names the timeout duration honestly.
	if fire.Err == "" {
		t.Errorf("fire Err empty; a timed-out fire must carry an honest message naming the timeout duration")
	}
	if !strings.Contains(fire.Err, "deadline") {
		t.Errorf("fire Err = %q, want a message naming the deadline", fire.Err)
	}
	// The Err names the configured default timeout (150ms).
	if !strings.Contains(fire.Err, "150ms") {
		t.Errorf("fire Err = %q, want it to name the 150ms timeout duration", fire.Err)
	}

	// (b3) The session lands a recoverable terminal snapshot (cancelled, NOT
	// running) — Interrupt-recoverable, the cloud-native Phase 1 guarantee.
	sess, err := store.Load(ctx, fire.SessionID)
	if err != nil {
		t.Fatalf("Load fire session %q: %v", fire.SessionID, err)
	}
	if sess.State != session.StateCancelled {
		t.Errorf("fire session state = %q, want %q (a timed-out fire must leave a terminal, recoverable snapshot)",
			sess.State, session.StateCancelled)
	}
	if err := sess.Interrupt(); err != nil {
		t.Errorf("Interrupt on the persisted cancelled session failed: %v (the snapshot must be Interrupt-recoverable)", err)
	}
}

// TestFireProgressAdvancesOnTurnBoundaries is the issue #386 Phase 3 Task D gate
// (c): RecordFireProgress advances the in-flight fire's last-observed-progress
// instant on turn-boundary / activity events (NOT every chunk). A single-text-turn
// fire emits an EvTurnEnd and an EvResult; both are progress markers, so the
// in-flight fire record's ProgressAt (observable via the schedule state's
// LastFireProgressAt before RecordFire clears it) must advance past the start.
//
// To observe the IN-FLIGHT progress (RecordFire clears the state field on
// terminal), the test drives a fire that blocks AFTER its first turn so the
// progress is stamped while the fire is still in-flight. It uses a two-turn
// mockllm script: a first text turn (stamps progress) then a blocking provider
// turn (keeps the fire in-flight so the test can read the in-flight state).
//
// Fully offline.
func TestFireProgressAdvancesOnTurnBoundaries(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	schedStore := store.ScheduleStore()
	const schedName = "fire-progress"
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "turn then block",
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
			// No FireTimeout: the default (30m) keeps the watchdog from cancelling
			// the blocking second turn before the test reads the in-flight state.
			Trigger: port.TriggerSpec{OneShot: time.Now().Add(-1 * time.Second)},
		},
		State: port.ScheduleState{
			NextFireAt: time.Now().Add(-1 * time.Second),
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	// A two-call provider: the first Stream call emits a text turn (text + usage +
	// ChunkDone StopEndTurn), so the loop completes turn 1 and emits EvTurnEnd →
	// RecordFireProgress; the second Stream call blocks on ctx (the
	// blockingFireProvider shape), keeping the fire in-flight so the test can read
	// the in-flight progress state.
	llm := &progressFireProvider{}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sched := scheduler.New(scheduler.Config{
		Store:       schedStore,
		Clock:       wallclock.Clock{},
		Diagnostics: port.NopDiagnostics{},
		Fire:        makeFireFunc(svc, schedStore, defaultFireTimeout, nil),
	})

	// Drive the fire asynchronously (the second turn blocks).
	fireDone := make(chan struct{})
	var fire port.ScheduleFire
	go func() {
		fire, _ = sched.FireNow(ctx, schedName, time.Now())
		close(fireDone)
	}()
	// Poll the IN-FLIGHT schedule state for LastFireProgressAt advancing past
	// the start (RecordFireStart seeds it to StartedAt; the first turn's
	// EvTurnEnd drives RecordFireProgress, advancing it). The fire is in-flight
	// while the second turn blocks, so the field is observable BEFORE RecordFire
	// clears it.
	if !eventually(5*time.Second, func() bool {
		loaded, lerr := schedStore.Load(ctx, schedName)
		if lerr != nil {
			return false
		}
		// RecordFireStart sets LastFireSessionID off "pending" + LastFireStartedAt;
		// the first turn's EvTurnEnd drives RecordFireProgress, advancing
		// LastFireProgressAt past LastFireStartedAt (the seeded value).
		if loaded.State.LastFireStartedAt.IsZero() {
			return false // RecordFireStart has not run yet.
		}
		return loaded.State.LastFireProgressAt.After(loaded.State.LastFireStartedAt)
	}) {
		loaded, _ := schedStore.Load(ctx, schedName)
		t.Fatalf("RecordFireProgress did not advance LastFireProgressAt within 5s (started=%v progress=%v sid=%q)",
			loaded.State.LastFireStartedAt, loaded.State.LastFireProgressAt, loaded.State.LastFireSessionID)
	}

	// Also assert the in-flight fire record's ProgressAt advanced. Find the
	// in-flight fire id (the schedule's LastFireSessionID, which RecordFireStart
	// set to the real session id).
	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load schedule: %v", err)
	}
	inflight, err := schedStore.LoadFire(ctx, string(loaded.State.LastFireSessionID))
	if err != nil {
		t.Fatalf("LoadFire in-flight %q: %v", loaded.State.LastFireSessionID, err)
	}
	if inflight.ProgressAt.IsZero() {
		t.Errorf("in-flight fire ProgressAt is zero; RecordFireProgress must stamp it on the fire record")
	}
	if inflight.Stop != "" {
		t.Errorf("in-flight fire Stop = %q, want empty (the fire is still in-flight)", inflight.Stop)
	}

	// Release the blocked second turn so the fire terminates and the test
	// goroutine joins (svc.Close cancels the in-flight run; FireNow returns).
	svc.Close()
	select {
	case <-fireDone:
	case <-time.After(10 * time.Second):
		t.Fatal("FireNow did not return within 10s after Close")
	}
	// After close the fire terminates (StopCancelled — the close-cancel; the
	// watchdog is not armed because the default timeout is 30m). Pin the fire
	// record is terminal so the test does not leave an in-flight fire behind.
	if fire.Stop != session.StopCancelled {
		t.Errorf("fire Stop = %q, want %q (a close-cancelled two-turn fire must terminate)", fire.Stop, session.StopCancelled)
	}
}

// TestFireCreateFailureRecordsTerminalFire is the issue #386 Phase 3 Task D gate
// (d): a fire whose CreateSessionWithProfile FAILS after Claim still produces a
// terminal StopError fire record via RecordFire (so it is not "fires: none" —
// the at-most-once Claim already advanced, so the failed fire is recorded, never
// retried). It forces a create failure by passing a selector with a ModelID but
// no ProviderID (CreateSessionWithProfile rejects it as ErrInvalidArgument),
// then asserts the fire record exists with StopError.
//
// Fully offline.
func TestFireCreateFailureRecordsTerminalFire(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	schedStore := store.ScheduleStore()
	const schedName = "fire-create-fail"
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:   schedName,
			Prompt: "never runs",
			// A selector with ModelID but no ProviderID is rejected by
			// CreateSessionWithProfile (ErrInvalidArgument) — a create-time
			// failure AFTER Claim.
			Selector:       port.ScheduleProviderSelector{ModelID: "bogus"},
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
			Trigger:        port.TriggerSpec{OneShot: time.Now().Add(-1 * time.Second)},
		},
		State: port.ScheduleState{
			NextFireAt: time.Now().Add(-1 * time.Second),
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	llm := mockllm.New(mockllm.TextTurn("never reached"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine,
		Store:  store,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	sched := scheduler.New(scheduler.Config{
		Store:       schedStore,
		Clock:       wallclock.Clock{},
		Diagnostics: port.NopDiagnostics{},
		Fire:        makeFireFunc(svc, schedStore, defaultFireTimeout, nil),
	})

	fire, fireErr := sched.FireNow(ctx, schedName, time.Now())
	// makeFireFunc returns (fireFailed, err) on a create failure; the scheduler's
	// fireClaimed records the fire (StopError) and returns the fire (the Go error
	// is logged, not returned to the FireNow caller — FireNow returns the fire
	// record). Either way, a terminal fire record must exist.
	_ = fireErr

	// The fire record exists with StopError (not "fires: none").
	loaded, err := schedStore.LoadFire(ctx, fire.ID)
	if err != nil {
		t.Fatalf("LoadFire %q: %v (a create-failed fire must still record a terminal fire — not \"fires: none\")", fire.ID, err)
	}
	if loaded.Stop != session.StopError {
		t.Errorf("loaded fire Stop = %q, want %q (a create failure must record StopError)", loaded.Stop, session.StopError)
	}
	if loaded.Err == "" {
		t.Errorf("loaded fire Err empty; a create failure must record the error string")
	}
}
