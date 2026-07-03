package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestSchedulerFire is the Phase 1f user-reachable gate (issue #189): a full
// app.Build with --scheduler over a real on-disk jsonlstore (which exposes a
// ScheduleStore), a one-shot schedule saved to the store, and the scheduler's
// tick loop firing it. It proves the whole Phase 1 arc end-to-end:
//
//  1. the scheduler started (non-nil on the Service);
//  2. within a bounded timeout a "sched--" session was created + persisted
//     (the store holds it);
//  3. StartRunContent drove it to a terminal EvResult (the fire's stop reason
//     is recorded);
//  4. the ScheduleFire record was recorded with Stop = StopEndTurn (success)
//     via LoadFire;
//  5. the one-shot schedule's NextFireAt is zero + Enabled=false (fired once).
//
// Fully offline: mockllm (a single text turn → StopEndTurn) + jsonlstore, no
// network, no API key. The fire is async (the tick loop), so the assertions
// poll with a bounded eventually.
func TestSchedulerFire(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	// Save a one-shot schedule due in the near future via a SEPARATE jsonlstore
	// handle over the same dir (the schedule file is flushed to disk before Build
	// starts the scheduler; the Build's store reads it on the first tick). A
	// one-shot fires once, so after the fire NextFireAt is zero + Enabled=false.
	seedStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("seed jsonlstore: %v", err)
	}
	schedStore := seedStore.ScheduleStore()
	const schedName = "phase1f-oneshot"
	due := time.Now().Add(100 * time.Millisecond)
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      schedName,
			Prompt:    "say hello from the scheduler",
			Workspace: workspace,
			Trigger:   port.TriggerSpec{OneShot: due},
		},
		State: port.ScheduleState{
			NextFireAt: due, // the store is parser-free; the seed must set the first fire instant
			Enabled:    true,
		},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	cfg := Config{
		Workspace:             workspace,
		NoSoul:                true,
		NoUserModel:           true,
		StoreDir:              storeDir,
		SchedulerEnabled:      true,
		SchedulerTickInterval: 50 * time.Millisecond,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("hello from the fire"))
		},
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// (1) The scheduler started.
	if !built.Service.HasScheduler() {
		t.Fatal("Service has no scheduler after Build with SchedulerEnabled")
	}

	// (2)-(5) Poll for the fire outcome. The one-shot is due at +100ms; the tick
	// loop polls every 50ms, so a fire lands within a few hundred ms. The fire
	// mints a "sched--" session, drives it to StopEndTurn, and records the fire.
	deadline := 10 * time.Second
	if !eventually(deadline, func() bool {
		// The fire record is the pull-only outcome channel (LoadFire). A successful
		// fire records Stop=StopEndTurn; a create/run failure records StopError.
		fire, err := schedStore.LoadFire(ctx, firstFireID(ctx, t, schedStore, schedName))
		if err != nil {
			return false
		}
		return fire.Stop == session.StopEndTurn
	}) {
		t.Fatalf("scheduler did not record a successful fire within %v", deadline)
	}

	// (5) The one-shot schedule is now done: NextFireAt zero + Enabled=false.
	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatalf("Load schedule after fire: %v", err)
	}
	if !loaded.State.NextFireAt.IsZero() {
		t.Errorf("one-shot NextFireAt = %v, want zero (fired once)", loaded.State.NextFireAt)
	}
	if loaded.State.Enabled {
		t.Error("one-shot Enabled = true after fire, want false (fired once)")
	}
	if loaded.State.FireCount != 1 {
		t.Errorf("one-shot FireCount = %d, want 1", loaded.State.FireCount)
	}

	// (2) A session was persisted for the fire. The schedule's LastFireSessionID
	// points at it (the FireFunc set it via RecordFire); load it from the session
	// store. The session id is the Service's random id (CreateSessionWithProfile
	// mints it); the "sched--" prefix is the FIRE-id / GC-family key (Phase 1g),
	// not the session id — the two are linked via LastFireSessionID.
	sess, err := seedStore.Load(ctx, loaded.State.LastFireSessionID)
	if err != nil {
		t.Fatalf("Load fire session %q: %v", loaded.State.LastFireSessionID, err)
	}
	if sess.ID != loaded.State.LastFireSessionID {
		t.Errorf("fire session id %q != schedule's LastFireSessionID %q", sess.ID, loaded.State.LastFireSessionID)
	}
	if sess.State != session.StateCompleted {
		t.Errorf("fire session state = %q, want completed", sess.State)
	}
	_ = filepath.Separator // keep filepath import (store dir layoutagnostic)
}

// TestMakeFireFuncReleasesSessionLease is the F-1 regression test (PR #211
// review): makeFireFunc's `defer svc.CloseSession(sess.ID)` must release the
// fire session's cross-process lease when the fire's run completes, keeping
// the durable snapshot but freeing the lease + renewer (internal/app/scheduler_fire.go).
// Before that fix, a lease-backed deployment leaked a held lease + renewer
// goroutine on EVERY fire (they are released only by CloseSession/shutdown,
// never per-run otherwise); the leak is doubly bad for a Singleton schedule
// (review #189), whose next fire's trial-acquire on the still-held lease
// would return port.ErrLeaseHeld and skip forever.
//
// This constructs a *server.Service directly (this test file is package app,
// so it can call the unexported makeFireFunc — Build's real composition path
// — without going through app.Build's Config, which has no knob to wire an
// engine/adapter/memlease backend directly; Config only exposes flock/driver/
// k8s SessionLease backends via SessionLeaseDir/URL/K8sNamespace). It proves
// the release DIRECTLY against Service.ActiveRuns() (the held-lease count)
// rather than depending on the scheduler's real-wallclock tick loop, which
// would be flaky — makeFireFunc is called synchronously, twice, so a leak
// shows up immediately as a non-zero count instead of requiring a timing-
// sensitive wait for a second real tick.
func TestMakeFireFuncReleasesSessionLease(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	llm := mockllm.New(mockllm.TextTurn("hello from fire one"), mockllm.TextTurn("hello from fire two"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	lease := memlease.New(wallclock.Clock{}, 30*time.Second)
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               store,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 time.Now,
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            store,
		Diagnostics:         port.NopDiagnostics{},
		SessionLease:        lease,
		LeaseOwner:          "test-owner",
		LeaseTTL:            30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	fire := makeFireFunc(svc)
	sched := port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      "lease-release",
			Prompt:    "say hi",
			Workspace: workspace,
			Mode:      session.ModePlan,
		},
	}

	if _, err := fire(ctx, sched, time.Now()); err != nil {
		t.Fatalf("fire #1: %v", err)
	}
	if n := svc.ActiveRuns(); n != 0 {
		t.Fatalf("ActiveRuns after fire #1 = %d, want 0 (the fire session's lease must be released — review #189)", n)
	}

	// A second, independent fire proves the release is not a one-shot fluke: a
	// leak that only shows up on the SECOND fire (e.g. an off-by-one in the
	// registry) would be missed by asserting only once.
	if _, err := fire(ctx, sched, time.Now()); err != nil {
		t.Fatalf("fire #2: %v", err)
	}
	if n := svc.ActiveRuns(); n != 0 {
		t.Fatalf("ActiveRuns after fire #2 = %d, want 0", n)
	}
}

// firstFireID returns the fire id for the schedule's most recent fire. The fire
// id is the session id (decision #7: the session id and fire id are the same
// "sched--" value). During the fire, LastFireSessionID is the "pending" sentinel
// Claim sets; RecordFire overwrites it with the real session id (= fire id).
// So this poll-skip returns "" while the fire is in-flight and the caller's
// eventually loop retries.
func firstFireID(ctx context.Context, t *testing.T, store port.ScheduleStore, name string) string {
	t.Helper()
	loaded, err := store.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load schedule %q: %v", name, err)
	}
	id := string(loaded.State.LastFireSessionID)
	if id == "" || id == "pending" {
		return "" // fire in-flight; the caller retries.
	}
	return id
}

// eventually polls f every 20ms until it returns true or the deadline elapses.
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
