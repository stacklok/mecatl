package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// scheduler_default_test.go pins the ADR-0073 decision-2 posture: the
// scheduler is ON by default on any schedule-capable store, and a store with
// no ScheduleStore (the in-memory default: mecademo, mecatequi, offline
// tests) stays on the BYTE-IDENTICAL no-scheduling path — buildScheduler
// returns (nil, noop, nil), no tick goroutine starts, and startup never fails
// on an in-memory store. The verify:-named AC tests
// (TestScheduleTool_SchedulerOnByDefault / _NoSchedulerDisablesTickOnly /
// _InMemoryStoreByteIdentical) live in internal/app/scheduletool_test.go;
// this file pins the buildScheduler seam directly.

// TestBuildSchedulerStoreBackedByDefault pins the buildScheduler half of
// AC2.1: with SchedulerEnabled true (the cmd-layer default now that
// --no-scheduler is the only knob) and a store exposing a ScheduleStore, the
// scheduler is built — no flag beyond the store selection is required.
func TestBuildSchedulerStoreBackedByDefault(t *testing.T) {
	t.Parallel()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	sched, closeFn := buildScheduler(Config{
		SchedulerEnabled: true,
		Diagnostics:      port.NopDiagnostics{},
	}, store, nil, "test-owner")
	if sched == nil {
		t.Fatal("buildScheduler returned a nil scheduler over a ScheduleStore-backed store, want a ticking scheduler (on by default)")
	}
	closeFn()
}

// TestBuildSchedulerInMemoryInertByDefault pins the reconciliation the
// on-by-default flip requires: the OLD buildScheduler FAILED LOUD when
// enabled-but-no-store. With on-by-default that error path is unreachable by
// default — an operator running the in-memory default (mecademo, mecatequi,
// offline tests) must get the byte-identical no-scheduling path, not a
// startup failure. Only an EXPLICIT opt-out (SchedulerEnabled false) and the
// default-on-no-store path both return (nil, noop, nil).
func TestBuildSchedulerInMemoryInertByDefault(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{name: "default-on (no flag) over the in-memory store", enabled: true},
		{name: "explicit opt-out over a ScheduleStore-backed store", enabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var store port.SessionStore
			if tc.enabled {
				// The in-memory store has no ScheduleStore: inert under the
				// default-on posture.
				store = memstore.New()
			} else {
				// A schedule-capable store under the explicit opt-out: also
				// inert (nothing is built).
				jstore, err := jsonlstore.New(t.TempDir())
				if err != nil {
					t.Fatalf("jsonlstore.New: %v", err)
				}
				store = jstore
			}
			sched, closeFn := buildScheduler(Config{
				SchedulerEnabled: tc.enabled,
				Diagnostics:      port.NopDiagnostics{},
			}, store, nil, "test-owner")
			if sched != nil {
				t.Fatal("buildScheduler returned a scheduler, want nil (inert)")
			}
			closeFn() // must be the noop close, never nil
		})
	}
}

// TestBuildSchedulerEndToEndDefaultOn drives the FULL Build path (the real
// composition the cmd mains feed) with NO scheduler knob set: the store-dir
// Build ticks (AC2.1's composition half), the in-memory Build starts clean
// with no scheduler (AC2.3's startup half). The named AC tests assert the
// Service-facing surface; this pins that Build itself wires both postures
// from the same default.
func TestBuildSchedulerEndToEndDefaultOn(t *testing.T) {
	ctx := context.Background()
	base := Config{
		NoSoul:      true,
		NoUserModel: true,
		// The cmd layer feeds SchedulerEnabled = !--no-scheduler (true by
		// default); a zero-value app.Config leaves it false (the engine-library
		// consumer's OFF posture — a library Build ticks only when the caller
		// opts in). The cmd mains are what "no flag passed" means.
		SchedulerEnabled:    true,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("hi"))
		},
	}

	// Store-dir (schedule-capable) + the default (no knob): the scheduler ticks.
	storeCfg := base
	storeCfg.StoreDir = t.TempDir()
	storeCfg.Workspace = t.TempDir()
	built, err := Build(ctx, storeCfg)
	if err != nil {
		t.Fatalf("Build (store-dir, default scheduler posture): %v", err)
	}
	defer built.Close()
	if !built.Service.HasScheduler() {
		t.Fatal("Build over a store-dir with NO scheduler knob has no scheduler, want the on-by-default tick loop")
	}

	// In-memory (no ScheduleStore) + the default: startup neither ticks nor
	// fails — the byte-identical no-scheduling path.
	memCfg := base
	memCfg.Workspace = t.TempDir()
	memBuilt, err := Build(ctx, memCfg)
	if err != nil {
		t.Fatalf("Build (in-memory store, default scheduler posture) = %v, want a clean startup (the on-by-default path never fails on a store with no ScheduleStore)", err)
	}
	defer memBuilt.Close()
	if memBuilt.Service.HasScheduler() {
		t.Fatal("Build over the in-memory store has a scheduler, want none (byte-identical no-scheduling path)")
	}
}

// TestScheduleFireRetentionDefaultActivation pins the composition-consumable
// half of AC2.1b: the retention POLICY the GC sweep enforces (a sched-- fire
// session older than the retention is deleted; retention 0 disables the
// pass). The 7d-default fold lives at the cmd layer (pinned in cmd/mecated);
// this pins that the policy value, once defaulted on, sweeps exactly the
// aged fire sessions and nothing else.
func TestScheduleFireRetentionDefaultActivation(t *testing.T) {
	// A 7d-retention policy (the cmd-layer default on the scheduler-on
	// posture) sweeps a sched-- fire session older than 7d and keeps a fresh
	// one; a 0 retention (the explicit =0) sweeps nothing.
	f := newGCFixture(t, childGCPolicy{scheduleFireRetention: 7 * 24 * time.Hour})
	f.save(t, "sched--nightly-20260520-aaaa") // saved at the fixture's t0
	f.now = f.now.Add(8 * 24 * time.Hour)     // the snapshot is now 8 days old
	f.save(t, "sched--nightly-fresh-bbbb")    // saved "now" — fresh

	deleted, _ := f.gc.sweep(context.Background())
	if deleted != 1 {
		t.Fatalf("sweep deleted %d sessions, want exactly 1 (the 8-day-old sched-- fire session)", deleted)
	}
	ids := f.ids(t)
	if ids["sched--nightly-20260520-aaaa"] {
		t.Fatal("the >7d sched-- fire session survived the 7d-retention sweep, want deleted")
	}
	if !ids["sched--nightly-fresh-bbbb"] {
		t.Fatal("the fresh sched-- fire session was swept, want retained")
	}

	// The explicit-disable half: retention 0 sweeps nothing (the
	// --schedule-fire-retention=0 posture).
	off := newGCFixture(t, childGCPolicy{}) // all knobs 0 — the explicit =0 fold
	off.save(t, "sched--nightly-20260520-aaaa")
	off.now = off.now.Add(30 * 24 * time.Hour)
	if deleted, _ := off.gc.sweep(context.Background()); deleted != 0 {
		t.Fatalf("retention-0 sweep deleted %d sessions, want 0 (an explicit --schedule-fire-retention=0 disables the pass)", deleted)
	}
}
