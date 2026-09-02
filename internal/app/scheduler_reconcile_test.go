package app

import (
	"context"
	"crypto/sha256"
	"fmt"
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

// newReconcileTestService builds a *server.Service over a jsonlstore (the SAME
// store backs the engine, the Service, and the schedule store) with a mockllm
// engine, so the reconcile callback's GetSession/SessionStore.Save operate on
// the durable store the real makeFireFunc uses. It returns the store, the
// schedule store, the service, and a cleanup. It is the shared harness for the
// stale-fire reconcile composition tests (issue #386 Phase 4b).
func newReconcileTestService(t *testing.T) (store *jsonlstore.Store, schedStore port.ScheduleStore, svc *server.Service) {
	t.Helper()
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	_ = workspace

	s, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(mockllm.TextTurn("ok"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   s,
	})
	sv, err := newTestServerService(server.Config{
		Engine: engine,
		Store:  s,

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            s,
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(sv.Close)
	_ = ctx
	return s, s.ScheduleStore(), sv
}

// TestReconcileStaleFireAfterClaimSettles is the issue #386 Phase 4b gate for
// crash sub-case 1: a crashed process left LastFireSessionID == "pending"
// (Claim happened, RecordFireStart/RecordFire never did), and the fire is
// stale (LastFireAt older than the window). After the tick's reconcile scan
// hands it to makeReconcileStaleFire, the callback must RecordFire a terminal
// StopError fire (reconcileStaleFireMsgClaim), clear the in-flight
// ScheduleState fields, and overwrite LastFireSessionID off the pending
// sentinel. No session exists (none was ever created), so the callback does
// NOT touch the SessionStore.
//
// Fully offline: jsonlstore + mockllm, no network.
func TestReconcileStaleFireAfterClaimSettles(t *testing.T) {
	ctx := context.Background()
	// Use the real default staleFireWindow (defaultFireTimeout-scale + grace ≈
	// 35m); set LastFireAt 40m in the past so the fire is stale without
	// overriding the package var (the override seam is scheduler-package-
	// internal; this composition test uses the real default).

	store, schedStore, svc := newReconcileTestService(t)
	const literalName = "crash-claim"
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	digest := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	schedName := fmt.Sprintf("schedule/%x\x00%s", digest[:], literalName)
	now := time.Now()
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    schedName,
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
			Owner:   owner,
		},
		State: port.ScheduleState{
			NextFireAt:        now.Add(time.Hour), // already claimed
			Enabled:           true,
			LastFireAt:        now.Add(-40 * time.Minute), // stale (window ≈ 35m)
			LastFireSessionID: port.PendingFireSessionID,  // RecordFireStart never ran
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	clk := wallclock.Clock{}
	s := scheduler.New(scheduler.Config{
		Store:              schedStore,
		Lease:              nil, // no lease backend: window is the only oracle
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		ReconcileStaleFire: makeReconcileStaleFire(svc, schedStore, store),
	})
	s.RunOnceForTest(ctx)

	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// LastFireSessionID is cleared off the pending sentinel (RecordFire
	// overwrote it with the minted fire's SessionID, which is empty — no
	// session was ever created). An empty real id is distinguishable from a
	// live run (the singleton check skips empty), satisfying acceptance #7.
	sid := string(loaded.State.LastFireSessionID)
	if sid == "pending" {
		t.Fatalf("LastFireSessionID = %q, want it cleared off the pending sentinel (reconcile must RecordFire and clear the in-flight state)", sid)
	}
	// The in-flight state fields are cleared by RecordFire.
	if !loaded.State.LastFireStartedAt.IsZero() {
		t.Errorf("LastFireStartedAt = %v, want zero (RecordFire clears in-flight state)", loaded.State.LastFireStartedAt)
	}
	if !loaded.State.FireDeadline.IsZero() {
		t.Errorf("FireDeadline = %v, want zero (RecordFire clears in-flight state)", loaded.State.FireDeadline)
	}
	// A terminal StopError fire record exists with the honest claim-crash
	// message (retrievable via ListFires — the fire id is a minted "sched--"
	// id, distinct from the empty SessionID).
	fires, err := schedStore.ListFires(ctx, schedName)
	if err != nil {
		t.Fatalf("ListFires: %v", err)
	}
	var found port.ScheduleFire
	for _, f := range fires {
		if f.Stop == session.StopError && f.Err == reconcileStaleFireMsgClaim {
			found = f
			break
		}
	}
	if found.ID == "" {
		t.Fatalf("no terminal StopError fire with Err=%q found (reconcile must RecordFire a terminal fire)", reconcileStaleFireMsgClaim)
	}
	if strings.Contains(found.ID, fmt.Sprintf("%x", digest[:])) || strings.ContainsRune(found.ID, '\x00') {
		t.Fatalf("reconciled fire id leaked physical owner namespace: %q", found.ID)
	}
	if !strings.Contains(found.ID, literalName) {
		t.Fatalf("reconciled fire id = %q, want literal schedule name %q", found.ID, literalName)
	}
	if found.SessionID != "" {
		t.Errorf("reconciled fire SessionID = %q, want empty (no session was ever created for a crash-after-Claim fire)", found.SessionID)
	}
}

// TestReconcileStaleFireAfterSessionSettles is the issue #386 Phase 4b gate for
// crash sub-case 2: a crashed process left a real in-flight fire
// (LastFireSessionID is a "sched--" id, LastFireStartedAt set, the session
// snapshot StateRunning — the loop never persisted terminal) whose lease
// lapsed. After the tick's reconcile scan hands it to makeReconcileStaleFire,
// the callback must settle the session snapshot to StateCancelled
// (Interrupt-recoverable) AND RecordFire a terminal StopError fire
// (reconcileStaleFireMsgRun) so the schedule's in-flight state is cleared.
//
// Fully offline.
func TestReconcileStaleFireAfterSessionSettles(t *testing.T) {
	ctx := context.Background()

	store, schedStore, svc := newReconcileTestService(t)
	const schedName = "crash-run"
	now := time.Now()
	startAt := now.Add(-40 * time.Minute) // stale (window ≈ 35m)

	// The fire's session id IS the fire id (decision #7). Mint a sched-- id
	// and persist an in-flight session (StateRunning — the crashed process's
	// in-flight run) + an in-flight fire record (RecordFireStart's write).
	const sessID session.SessionID = "sched--crash-run-crashed"
	if err := store.Save(ctx, &session.Session{
		ID:             sessID,
		State:          session.StateRunning,
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: localDefaultPlacementID, Revision: localDefaultPlacementRevision},
		Conversation: &session.Conversation{
			Messages: []session.Message{session.NewUserMessage("crashed mid-run")},
		},
	}); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      schedName,
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "* * * * *"},
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:         now.Add(time.Hour),
			Enabled:            true,
			LastFireAt:         startAt,
			LastFireSessionID:  sessID,
			LastFireStartedAt:  startAt,
			LastFireProgressAt: startAt,
		},
	}); err != nil {
		t.Fatalf("Save schedule: %v", err)
	}
	if err := schedStore.RecordFireStart(ctx, schedName, port.ScheduleFire{
		ID:           string(sessID),
		ScheduleName: schedName,
		SessionID:    sessID,
		FiredAt:      startAt,
		StartedAt:    startAt,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}

	clk := wallclock.Clock{}
	s := scheduler.New(scheduler.Config{
		Store:              schedStore,
		Lease:              nil, // no lease backend: window is the only oracle
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		ReconcileStaleFire: makeReconcileStaleFire(svc, schedStore, store),
	})
	s.RunOnceForTest(ctx)

	// (1) The session snapshot is settled to StateCancelled (terminal +
	// Interrupt-recoverable), NOT running.
	sess, err := store.Load(ctx, sessID)
	if err != nil {
		t.Fatalf("Load session: %v", err)
	}
	if sess.State != session.StateCancelled {
		t.Errorf("session state = %q, want %q (the crashed run must be settled to cancelled, Interrupt-recoverable)",
			sess.State, session.StateCancelled)
	}
	if err := sess.Interrupt(); err != nil {
		t.Errorf("Interrupt on the settled cancelled session failed: %v (must be Interrupt-recoverable)", err)
	}

	// (2) The schedule's in-flight state is cleared by RecordFire.
	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load schedule: %v", err)
	}
	if !loaded.State.LastFireStartedAt.IsZero() {
		t.Errorf("LastFireStartedAt = %v, want zero (RecordFire clears in-flight state)", loaded.State.LastFireStartedAt)
	}
	if !loaded.State.FireDeadline.IsZero() {
		t.Errorf("FireDeadline = %v, want zero (RecordFire clears in-flight state)", loaded.State.FireDeadline)
	}

	// (3) The fire record is terminal StopError with the honest run-crash message.
	fire, err := schedStore.LoadFire(ctx, string(sessID))
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if fire.Stop != session.StopError {
		t.Errorf("fire Stop = %q, want %q", fire.Stop, session.StopError)
	}
	if fire.Err != reconcileStaleFireMsgRun {
		t.Errorf("fire Err = %q, want %q", fire.Err, reconcileStaleFireMsgRun)
	}
}

// TestReconcileStaleFireIdempotentOnTerminal asserts the reconcile callback is
// idempotent: a fire already terminal (a prior reconcile or a late RecordFire)
// is a no-op. Driving a second reconcile tick after the first settled the fire
// must NOT re-settle, re-cancel, or re-record — RecordFire is idempotent per
// fire id, and a terminal session snapshot is not re-cancelled (Cancel refuses
// a terminal state).
func TestReconcileStaleFireIdempotentOnTerminal(t *testing.T) {
	ctx := context.Background()

	store, schedStore, svc := newReconcileTestService(t)
	const schedName = "crash-idempotent"
	now := time.Now()
	startAt := now.Add(-40 * time.Minute) // stale (window ≈ 35m)

	const sessID session.SessionID = "sched--crash-idempotent"
	// Pre-settle the session: it is ALREADY cancelled (a prior reconcile or the
	// loop's last-gasp save landed the terminal snapshot). The reconcile
	// callback must NOT re-cancel a terminal session.
	if err := store.Save(ctx, &session.Session{
		ID:             sessID,
		State:          session.StateCancelled,
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: localDefaultPlacementID, Revision: localDefaultPlacementRevision},
		Conversation: &session.Conversation{
			Messages: []session.Message{session.NewUserMessage("already settled")},
		},
	}); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	// Pre-settle the fire record: it is ALREADY terminal (StopError from a
	// prior reconcile). RecordFire is idempotent per fire id, so a second
	// RecordFire is a no-op.
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      schedName,
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "* * * * *"},
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:         now.Add(time.Hour),
			Enabled:            true,
			LastFireAt:         startAt,
			LastFireSessionID:  sessID,
			LastFireStartedAt:  startAt, // still in-flight on the STATE (RecordFire did NOT run)
			LastFireProgressAt: startAt,
		},
	}); err != nil {
		t.Fatalf("Save schedule: %v", err)
	}
	// Persist the in-flight fire record, then a terminal one (a prior
	// reconcile's RecordFire). The terminal record wins (same id).
	if err := schedStore.RecordFireStart(ctx, schedName, port.ScheduleFire{
		ID:           string(sessID),
		ScheduleName: schedName,
		SessionID:    sessID,
		FiredAt:      startAt,
		StartedAt:    startAt,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}
	if err := schedStore.RecordFire(ctx, port.ScheduleFire{
		ID:           string(sessID),
		ScheduleName: schedName,
		SessionID:    sessID,
		FiredAt:      startAt,
		StartedAt:    startAt,
		Stop:         session.StopError,
		Err:          "prior reconcile",
	}); err != nil {
		t.Fatalf("RecordFire (prior): %v", err)
	}

	clk := wallclock.Clock{}
	s := scheduler.New(scheduler.Config{
		Store:              schedStore,
		Lease:              nil,
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		ReconcileStaleFire: makeReconcileStaleFire(svc, schedStore, store),
	})
	// Drive a reconcile tick — the fire is stale by the window, but the fire
	// record is ALREADY terminal, so the callback's RecordFire is a no-op and
	// the session is NOT re-cancelled (it is already terminal).
	s.RunOnceForTest(ctx)

	// The session is STILL cancelled (not re-cancelled; Cancel would refuse a
	// terminal state anyway, but the callback gates on !IsTerminal so it never
	// tries).
	sess, err := store.Load(ctx, sessID)
	if err != nil {
		t.Fatalf("Load session: %v", err)
	}
	if sess.State != session.StateCancelled {
		t.Errorf("session state = %q, want %q (idempotent reconcile must not change a terminal session)", sess.State, session.StateCancelled)
	}
	// The fire record is STILL the prior one (Err = "prior reconcile").
	fire, err := schedStore.LoadFire(ctx, string(sessID))
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if fire.Err != "prior reconcile" {
		t.Errorf("fire Err = %q, want %q (idempotent RecordFire must not overwrite a terminal fire)", fire.Err, "prior reconcile")
	}
}

// TestReconcileStaleFireLiveNotReconciled asserts a genuinely-live fire (lease
// held by the running process) is NOT reconciled at the composition level: the
// detector's isPriorFireLive trial-lease check returns overlap=true, so the
// reconcile scan skips it and the composition callback is never invoked. The
// session stays running and the fire stays in-flight (the crashed-process
// premise is false — the fire is genuinely running). It wires a held-lease
// backend (heldPriorFireLease, already used by the schedule-tool tests) so the
// live fire's session lease is held by another owner.
func TestReconcileStaleFireLiveNotReconciled(t *testing.T) {
	ctx := context.Background()

	store, schedStore, _ := newReconcileTestService(t)
	const schedName = "crash-live"
	now := time.Now()
	startAt := now.Add(-40 * time.Minute) // stale by the window, BUT the lease is held (live)

	const sessID session.SessionID = "sched--crash-live-running"
	// Persist a RUNNING session (the live fire's in-flight run) + an in-flight
	// fire record.
	if err := store.Save(ctx, &session.Session{
		ID:             sessID,
		State:          session.StateRunning,
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: localDefaultPlacementID, Revision: localDefaultPlacementRevision},
		Conversation: &session.Conversation{
			Messages: []session.Message{session.NewUserMessage("still running")},
		},
	}); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      schedName,
			Prompt:    "x",
			Trigger:   port.TriggerSpec{Cron: "* * * * *"},
			Singleton: true,
		},
		State: port.ScheduleState{
			NextFireAt:         now.Add(time.Hour),
			Enabled:            true,
			LastFireAt:         startAt,
			LastFireSessionID:  sessID,
			LastFireStartedAt:  startAt,
			LastFireProgressAt: startAt,
		},
	}); err != nil {
		t.Fatalf("Save schedule: %v", err)
	}
	if err := schedStore.RecordFireStart(ctx, schedName, port.ScheduleFire{
		ID:           string(sessID),
		ScheduleName: schedName,
		SessionID:    sessID,
		FiredAt:      startAt,
		StartedAt:    startAt,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}

	// A lease backend that holds sessID — the trial-lease acquire returns
	// ErrLeaseHeld (overlap=true), so the detector skips this fire.
	leaseBE := &heldPriorFireLease{
		held: map[session.SessionID]string{sessID: "owner-running"},
	}

	reconciled := 0
	clk := wallclock.Clock{}
	s := scheduler.New(scheduler.Config{
		Store:              schedStore,
		Lease:              leaseBE,
		LeaseOwner:         "owner-reconciler",
		Clock:              clk,
		TickInterval:       1 * time.Hour,
		MaxConcurrentFires: 4,
		ReconcileStaleFire: func(_ context.Context, _ port.Schedule) {
			reconciled++
		},
	})
	s.RunOnceForTest(ctx)

	if reconciled != 0 {
		t.Fatalf("reconciled = %d, want 0 (a genuinely-live fire whose lease is held must NOT be reconciled)", reconciled)
	}
	// The session is STILL running (not settled to cancelled).
	sess, err := store.Load(ctx, sessID)
	if err != nil {
		t.Fatalf("Load session: %v", err)
	}
	if sess.State != session.StateRunning {
		t.Errorf("session state = %q, want %q (a live fire must not be settled)", sess.State, session.StateRunning)
	}
	// The fire record is STILL in-flight (Stop empty — RecordFire did not run).
	fire, err := schedStore.LoadFire(ctx, string(sessID))
	if err != nil {
		t.Fatalf("LoadFire: %v", err)
	}
	if fire.Stop != "" {
		t.Errorf("fire Stop = %q, want empty (a live fire must stay in-flight; reconcile must not RecordFire it terminal)", fire.Stop)
	}
}
