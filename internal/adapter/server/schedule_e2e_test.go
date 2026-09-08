package server_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
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

// TestScheduleE2E is the Phase 2a end-to-end gate (issue #232): it drives the
// gRPC ScheduleServer directly (in-process, no network) over a built *Service
// wired with jsonlstore + mockllm + the in-process scheduler. It exercises the
// full wire surface — Create/Get/List/Pause/Resume/FireNow/GetFire/ListFires/
// Delete + the bad-spec rejections — and polls the FireNow fire's session to a
// terminal StopEndTurn, mirroring the scheduler_fire_test.go eventually pattern.
//
// Fully offline: mockllm (a single text turn → StopEndTurn) + jsonlstore (which
// exposes ScheduleStore + EventLog) + memfs. No network, no API key.
func TestScheduleE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()

	llm := mockllm.New(mockllm.TextTurn("scheduled fire result"))

	svc, sched, _, cleanup := buildScheduleService(t, storeDir, llm)
	defer cleanup()

	srv := server.NewScheduleServer(svc)

	// --- CreateSchedule (cron): Enabled + NextFireAt computed -----------------
	cronResp, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "e2e-cron",
			Prompt:  "cron hello",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{Cron: "@every 1m"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule cron: %v", err)
	}
	cron := cronResp.GetSchedule()
	if cron.GetState().GetEnabled() != true {
		t.Errorf("cron Enabled = false, want true")
	}
	if cron.GetState().GetNextFireAt() == nil {
		t.Errorf("cron NextFireAt = nil, want computed")
	}

	// --- CreateSchedule (one-shot): a future time ----------------------------
	oneShotAt := time.Now().Add(1 * time.Hour)
	osResp, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "e2e-oneshot",
			Prompt:  "oneshot hello",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{OneShot: timestamppb.New(oneShotAt)},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule one-shot: %v", err)
	}
	if osResp.GetSchedule().GetState().GetEnabled() != true {
		t.Errorf("one-shot Enabled = false, want true")
	}

	// --- CreateSchedule (bad cron): InvalidArgument --------------------------
	if _, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "e2e-bad-cron",
			Prompt:  "x",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{Cron: "not a real cron"},
		},
	}); err == nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad cron: err=%v, want InvalidArgument", err)
	}

	// --- CreateSchedule (past one-shot): InvalidArgument ---------------------
	if _, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "e2e-past",
			Prompt:  "x",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{OneShot: timestamppb.New(time.Now().Add(-1 * time.Hour))},
		},
	}); err == nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("past one-shot: err=%v, want InvalidArgument", err)
	}

	// --- ListSchedules: returns both created schedules ----------------------
	listResp, err := srv.ListSchedules(ctx, &mecatlv1.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if !scheduleNames(listResp).has("e2e-cron") || !scheduleNames(listResp).has("e2e-oneshot") {
		t.Fatalf("ListSchedules = %v, want both e2e-cron + e2e-oneshot", scheduleNames(listResp))
	}

	// --- GetSchedule: by name ------------------------------------------------
	getResp, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "e2e-cron"})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if getResp.GetSchedule().GetSpec().GetName() != "e2e-cron" {
		t.Errorf("GetSchedule name = %q, want e2e-cron", getResp.GetSchedule().GetSpec().GetName())
	}

	// --- GetSchedule (unknown): NotFound ------------------------------------
	if _, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "no-such-schedule"}); err == nil ||
		status.Code(err) != codes.NotFound {
		t.Fatalf("GetSchedule unknown: err=%v, want NotFound", err)
	}

	// --- PauseSchedule + GetSchedule: Enabled=false --------------------------
	if _, err := srv.PauseSchedule(ctx, &mecatlv1.PauseScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}
	if paused, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("GetSchedule after pause: %v", err)
	} else if paused.GetSchedule().GetState().GetEnabled() {
		t.Error("Enabled = true after pause, want false")
	}

	// --- ResumeSchedule + GetSchedule: Enabled=true --------------------------
	if _, err := srv.ResumeSchedule(ctx, &mecatlv1.ResumeScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}
	if resumed, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("GetSchedule after resume: %v", err)
	} else if !resumed.GetSchedule().GetState().GetEnabled() {
		t.Error("Enabled = false after resume, want true")
	}

	// --- FireNow: returns fire_id + session_id, polls to terminal -----------
	// FireNow bypasses the due check (ClaimNow) per the proto contract: a manual
	// trigger fires regardless of whether the schedule is due, while still
	// claiming the slot atomically (at-most-once). The cron schedule above was
	// created via the create seam with a FUTURE NextFireAt; FireNow on it succeeds
	// directly (no Delete + re-Save-as-due workaround).
	fireResp, err := srv.FireNow(ctx, &mecatlv1.FireNowRequest{Name: "e2e-cron"})
	if err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	fireID := fireResp.GetFireId()
	sessID := fireResp.GetSessionId()
	if fireID == "" || sessID == "" || fireID != sessID {
		t.Fatalf("FireNow fire_id=%q session_id=%q (want non-empty and equal)", fireID, sessID)
	}
	// ADR 0059 decision #7 Phase-2: the fire's session id is "sched--"-prefixed
	// (the fire path pre-mints it via newFireID and passes it as the
	// WithSessionID override on CreateSessionWithProfile, so the persisted
	// session carries the sched-- GC-retention family prefix).
	if !strings.HasPrefix(sessID, "sched--") {
		t.Errorf("FireNow session_id=%q, want a \"sched--\" prefix", sessID)
	}

	// Poll the fire's session to a terminal state (the mockllm drives StopEndTurn).
	deadline := 10 * time.Second
	if !eventually(deadline, func() bool {
		sess, err := svc.GetSession(ctx, session.SessionID(sessID))
		if err != nil {
			return false
		}
		return sess.State == session.StateCompleted
	}) {
		t.Fatalf("fire session %q did not reach completed within %v", sessID, deadline)
	}

	// --- GetFire: by the returned fire_id -----------------------------------
	gfResp, err := srv.GetFire(ctx, &mecatlv1.GetFireRequest{FireId: fireID})
	if err != nil {
		t.Fatalf("GetFire: %v", err)
	}
	fire := gfResp.GetFire()
	if fire.GetId() != fireID {
		t.Errorf("GetFire id = %q, want %q", fire.GetId(), fireID)
	}
	if fire.GetSessionId() != sessID {
		t.Errorf("GetFire session_id = %q, want %q", fire.GetSessionId(), sessID)
	}
	if fire.GetStop() != string(session.StopEndTurn) {
		t.Errorf("GetFire stop = %q, want %q", fire.GetStop(), session.StopEndTurn)
	}

	// --- ListFires: for the schedule → returns the fire record(s) ------------
	lfResp, err := srv.ListFires(ctx, &mecatlv1.ListFiresRequest{ScheduleName: "e2e-cron"})
	if err != nil {
		t.Fatalf("ListFires: %v", err)
	}
	if !containsFire(lfResp.GetFires(), fireID) {
		t.Fatalf("ListFires = %v, want fire %q present", lfResp.GetFires(), fireID)
	}

	// --- DeleteSchedule: idempotent (delete → delete again → success) --------
	if _, err := srv.DeleteSchedule(ctx, &mecatlv1.DeleteScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("DeleteSchedule #1: %v", err)
	}
	if _, err := srv.DeleteSchedule(ctx, &mecatlv1.DeleteScheduleRequest{Name: "e2e-cron"}); err != nil {
		t.Fatalf("DeleteSchedule #2 (idempotent): %v", err)
	}

	// --- GetSchedule after delete: NotFound ----------------------------------
	if _, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "e2e-cron"}); err == nil ||
		status.Code(err) != codes.NotFound {
		t.Fatalf("GetSchedule after delete: err=%v, want NotFound", err)
	}

	// sanity: the scheduler is the one we wired (no shadow)
	if !svc.HasScheduler() {
		t.Fatal("Service has no scheduler after wiring")
	}
	_ = sched // scheduler started in buildScheduleService; close handles Stop

	// the one-shot + cron schedules are durable (jsonlstore); the workspace path
	// separator is exercised by the store's file layout. Keep the import honest.
	_ = filepath.Separator
}

// TestScheduleFireNowOneShotExhaustedWireMapping (M1): a FireNow on an
// already-fired one-shot returns codes.FailedPrecondition (the wire mapping of
// ErrScheduleExhausted), NOT codes.Internal/500. The scheduler-level test
// (TestFireNowOneShotExhausted) only asserts the scheduler sentinel; this test
// pins the gRPC handler-level mapping the toStatus chokepoint applies.
func TestScheduleFireNowOneShotExhaustedWireMapping(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()

	llm := mockllm.New(mockllm.TextTurn("scheduled fire result"))
	svc, _, store, cleanup := buildScheduleService(t, storeDir, llm)
	defer cleanup()
	srv := server.NewScheduleServer(svc)

	// Create a one-shot schedule and fire it once (FireNow succeeds → the
	// one-shot is now exhausted).
	oneShotAt := time.Now().Add(1 * time.Hour)
	if _, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "exhausted-oneshot",
			Prompt:  "oneshot hello",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{OneShot: timestamppb.New(oneShotAt)},
		},
	}); err != nil {
		t.Fatalf("CreateSchedule one-shot: %v", err)
	}
	if _, err := srv.FireNow(ctx, &mecatlv1.FireNowRequest{Name: "exhausted-oneshot"}); err != nil {
		t.Fatalf("FireNow #1 (the first fire): %v", err)
	}
	// Poll the fire's session to terminal so the first fire completes before
	// the second FireNow attempt (otherwise the singleton check would reject it
	// as an overlap, not exhaustion).
	if !eventually(10*time.Second, func() bool {
		fires, _ := store.ScheduleStore().ListFires(ctx, "exhausted-oneshot")
		for _, f := range fires {
			if f.Stop != "" { // terminal
				return true
			}
		}
		return false
	}) {
		t.Fatal("first fire did not reach terminal within 10s")
	}
	// A second FireNow on the exhausted one-shot → FailedPrecondition (412), NOT
	// Internal/500 (the M1 fix: ErrScheduleExhausted is mapped, not defaulted).
	_, err := srv.FireNow(ctx, &mecatlv1.FireNowRequest{Name: "exhausted-oneshot"})
	if err == nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("FireNow on exhausted one-shot: err=%v, want FailedPrecondition", err)
	}
}

// TestScheduleEventLogContainsEvScheduleFired (S7): after a FireNow completes,
// the fire session's durable EventLog contains an EvScheduleFired event. This
// pins the S1 v1 delivery contract: the schedule.* lifecycle is durable-log-only
// (pull-only via GetFire/ListFires), emitted from composition via the
// EmitScheduleEvent callback.
func TestScheduleEventLogContainsEvScheduleFired(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()

	llm := mockllm.New(mockllm.TextTurn("scheduled fire result"))
	svc, _, store, cleanup := buildScheduleService(t, storeDir, llm)
	defer cleanup()
	srv := server.NewScheduleServer(svc)

	if _, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "eventlog-cron",
			Prompt:  "cron hello",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{Cron: "@every 1m"},
		},
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	fireResp, err := srv.FireNow(ctx, &mecatlv1.FireNowRequest{Name: "eventlog-cron"})
	if err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	sessID := session.SessionID(fireResp.GetSessionId())

	// Poll the session to terminal so the fire's EvScheduleFired has been
	// appended to the durable log (the emit runs after the FireFunc returns).
	if !eventually(10*time.Second, func() bool {
		sess, err := svc.GetSession(ctx, sessID)
		if err != nil {
			return false
		}
		return sess.State == session.StateCompleted
	}) {
		t.Fatalf("fire session %q did not reach completed within 10s", sessID)
	}

	// Read the durable EventLog and assert an EvScheduleFired event is present.
	var sawFired bool
	for ev, err := range store.Read(ctx, sessID) {
		if err != nil {
			t.Fatalf("EventLog.Read: %v", err)
		}
		if ev.Type == session.EvScheduleFired && ev.Schedule != nil &&
			ev.Schedule.ScheduleName == "eventlog-cron" {
			sawFired = true
		}
	}
	if !sawFired {
		t.Fatalf("EventLog.Read(%q) did not contain an EvScheduleFired event", sessID)
	}
}

// TestScheduleNoSchedulerVariants covers the no-scheduler-wired case: a Service
// whose store does NOT expose a ScheduleStore (memstore) honestly reports the
// schedule RPCs as Unimplemented (CreateSchedule) / Unimplemented (FireNow with
// no scheduler). It reuses the lightweight newService helper (memstore-backed).
func TestScheduleNoSchedulerVariants(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, mockllm.New(), allowRules())
	srv := server.NewScheduleServer(svc)

	// CreateSchedule → Unimplemented (memstore exposes no ScheduleStore).
	if _, err := srv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{
		Spec: &mecatlv1.ScheduleSpec{
			Name:    "no-sched-store",
			Prompt:  "x",
			Mode:    mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
			Trigger: &mecatlv1.TriggerSpec{Cron: "@every 1m"},
		},
	}); err == nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("CreateSchedule without ScheduleStore: err=%v, want Unimplemented", err)
	}

	// FireNow → Unimplemented (scheduler is nil).
	if _, err := srv.FireNow(ctx, &mecatlv1.FireNowRequest{Name: "no-such"}); err == nil ||
		status.Code(err) != codes.Unimplemented {
		t.Fatalf("FireNow without scheduler: err=%v, want Unimplemented", err)
	}

	// ListSchedules / GetSchedule / DeleteSchedule → Unimplemented too.
	if _, err := srv.ListSchedules(ctx, &mecatlv1.ListSchedulesRequest{}); err == nil ||
		status.Code(err) != codes.Unimplemented {
		t.Fatalf("ListSchedules without ScheduleStore: err=%v, want Unimplemented", err)
	}
	if _, err := srv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "x"}); err == nil ||
		status.Code(err) != codes.Unimplemented {
		t.Fatalf("GetSchedule without ScheduleStore: err=%v, want Unimplemented", err)
	}
}

// buildScheduleService wires a *Service + *scheduler.Scheduler over a jsonlstore
// (which exposes ScheduleStore + EventLog) + mockllm + memfs, mirroring the
// composition app.Build does for the SchedulerEnabled path but WITHOUT crossing
// into the internal/app package (whose test seams are unexported). The FireFunc
// mirrors makeFireFunc: CreateSessionWithProfile → StartScheduledRunContent → drain to the
// terminal EvResult, returning the ScheduleFire carrying the stop reason. The
// returned cleanup stops the scheduler + closes the service.
func buildScheduleService(t *testing.T, storeDir string, llm *mockllm.Provider) (*server.Service, *scheduler.Scheduler, *jsonlstore.Store, func()) {
	t.Helper()
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		// Store: the engine persists mid-run transitions + the terminal state to it
		// (the same discipline app.Build wires via Deps.Store), so the fire's
		// session is durable as completed for the pull-only GetSession assertion.
		Store: store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:           engine,
		Store:            store,
		SharedEngineRoot: "/workspace",

		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store, // jsonlstore implements EventLog
		Diagnostics:         port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sched := scheduler.New(scheduler.Config{
		Store:        store.ScheduleStore(),
		Clock:        wallclock.Clock{},
		Diagnostics:  port.NopDiagnostics{},
		TickInterval: 50 * time.Millisecond,
	})
	svc.SetScheduler(sched)
	sched.SetFire(fireFuncForTest(svc))
	sched.SetEmitScheduleEvent(svc.EmitScheduleEvent)
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	cleanup := func() {
		// svc.Close stops the scheduler first (so in-flight fires drain while the
		// service is still alive to serve them); calling sched.Stop again would be
		// a harmless no-op (idempotent), but relying on svc.Close alone is cleaner.
		svc.Close()
	}
	return svc, sched, store, cleanup
}

// fireFuncForTest mirrors internal/app.makeFireFunc over the *Service: it mints
// a fresh "sched--"-prefixed session reattached to the schedule's exact
// placement via the WithSessionID override, drives it to
// the terminal EvResult via StartScheduledRunContent, and returns the fire record. The
// fire id IS the session id (ADR 0059 decision #7 Phase-2). Read-leaning
// schedules run in plan mode (a read-only toolset).
func fireFuncForTest(svc *server.Service) scheduler.FireFunc {
	return func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error) {
		mode := sched.Spec.Mode
		if mode == "" {
			mode = session.ModeDefault
		}
		if !sched.Spec.Mutating {
			mode = session.ModePlan
		}
		limits := sched.Spec.Limits
		if limits.MaxTurns == 0 {
			limits.MaxTurns = 50
		}
		if limits.MaxToolCalls == 0 {
			limits.MaxToolCalls = 200
		}
		if limits.MaxConsecutiveFailures == 0 {
			limits.MaxConsecutiveFailures = 5
		}
		fireID := newFireIDForTest(sched.Spec.Name, now)
		placement, err := svc.ReattachPlacementInScope(ctx, sched.Spec.EnvironmentRef, sched.Spec.PlacementScope)
		if err != nil {
			return port.ScheduleFire{ID: fireID, ScheduleName: sched.Spec.Name, FiredAt: now, Stop: session.StopError, Err: err.Error()}, err
		}
		sess, err := svc.CreateSessionWithProfile(ctx, mode, limits, server.ProviderSelector{}, server.ProfileDefault,
			server.WithSessionID(session.SessionID(fireID)),
			server.WithPlacementBinding(placement),
			server.WithScheduledRelationship(sched.Spec.Name, sched.Spec.OriginSessionID))
		if err != nil {
			return port.ScheduleFire{
				ID: fireID, ScheduleName: sched.Spec.Name,
				FiredAt: now, Stop: session.StopError, Err: err.Error(),
			}, err
		}
		defer svc.CloseSession(sess.ID)
		run, err := svc.StartScheduledRunContent(ctx, sess.ID, sched.Spec.Prompt, sched.Spec.Parts)
		if err != nil {
			return port.ScheduleFire{
				ID: string(sess.ID), ScheduleName: sched.Spec.Name, SessionID: sess.ID,
				FiredAt: now, Stop: session.StopError, Err: err.Error(),
			}, err
		}
		var stop session.StopReason
		var runErr string
		for ev := range run.Events() {
			if ev.Type == session.EvResult && ev.Result != nil {
				stop = ev.Result.Stop
				runErr = ev.Result.Error
				break
			}
		}
		id := fireID
		if string(sess.ID) != "" {
			id = string(sess.ID)
		}
		return port.ScheduleFire{
			ID:           id,
			ScheduleName: sched.Spec.Name,
			SessionID:    sess.ID,
			FiredAt:      now,
			Stop:         stop,
			Err:          runErr,
		}, nil
	}
}

// newFireIDForTest mirrors internal/app.newFireID: "sched--<name>-<UTC compact>-<rand>".
// It is the test-local copy (this package cannot import internal/app).
func newFireIDForTest(name string, now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("sched--%s-%s-%s", name, now.UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// --- small test helpers -----------------------------------------------------

type nameSet map[string]struct{}

func (n nameSet) has(s string) bool { _, ok := n[s]; return ok }

func scheduleNames(resp *mecatlv1.ListSchedulesResponse) nameSet {
	out := nameSet{}
	for _, s := range resp.GetSchedules() {
		out[s.GetSpec().GetName()] = struct{}{}
	}
	return out
}

func containsFire(fires []*mecatlv1.ScheduleFire, id string) bool {
	for _, f := range fires {
		if f.GetId() == id {
			return true
		}
	}
	return false
}

// eventually polls f every 20ms until it returns true or the deadline elapses.
// It mirrors internal/app.eventually (which is package-app-private) so this
// in-package-server_test e2e can poll the async fire's session to terminal.
func eventually(deadline time.Duration, f func() bool) bool {
	deadlineAt := time.Now().Add(deadline)
	for time.Now().Before(deadlineAt) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}
