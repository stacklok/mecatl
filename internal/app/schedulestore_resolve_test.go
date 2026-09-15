package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestResolveScheduleStore pins the --schedule-store-url override resolution
// (cloud-native Phase 5, issue #257), mirroring the --event-log-url precedent.
// The three precedence arms:
//  1. an explicit ScheduleStoreURL wins — a *grpcdriver.ScheduleStore over its
//     OWN lazy-dialled conn (offline-safe; grpc.NewClient never touches the
//     network — the first RPC would, never reached here);
//  2. else the configured store is type-asserted for the ScheduleStore()
//     accessor — a jsonlstore exposes one, so its ScheduleStore is returned;
//  3. else nil (the in-memory memstore has no accessor → the byte-identical
//     no-scheduling path).
//
// The override conn close is non-nil (the driver owns a Build-scoped resource);
// the accessor and nil arms return a no-op close (the store's own ScheduleStore
// holds no separate Build-scoped resource).
func TestResolveScheduleStore(t *testing.T) {
	t.Run("override set -> grpcdriver client (offline-safe lazy dial)", func(t *testing.T) {
		cfg := Config{ScheduleStoreURL: "127.0.0.1:7445"}
		store, closeFn, err := resolveScheduleStore(cfg, memstore.New())
		if err != nil {
			t.Fatalf("resolveScheduleStore(override): %v", err)
		}
		defer closeFn()
		if _, ok := store.(*grpcdriver.ScheduleStore); !ok {
			t.Fatalf("resolveScheduleStore(override) store = %T, want *grpcdriver.ScheduleStore", store)
		}
	})

	t.Run("override empty + store with accessor -> store's ScheduleStore", func(t *testing.T) {
		js, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		// jsonlstore.ScheduleStore() returns a fresh *scheduleStore per call
		// (sharing the dir+mu), so pointer identity is NOT the contract; the
		// contract is that resolveScheduleStore returns the store's OWN
		// ScheduleStore (non-nil, same backend) rather than a driver client.
		want := js.ScheduleStore()
		if want == nil {
			t.Fatalf("jsonlstore.ScheduleStore() = nil, want non-nil (the accessor that backs scheduling)")
		}
		got, closeFn, err := resolveScheduleStore(Config{}, js)
		if err != nil {
			t.Fatalf("resolveScheduleStore(accessor): %v", err)
		}
		closeFn()
		if got == nil {
			t.Fatalf("resolveScheduleStore(accessor) store = nil, want the store's own ScheduleStore (non-nil)")
		}
		// A driver client would be a *grpcdriver.ScheduleStore; the accessor path
		// must NOT cross into the driver branch.
		if _, ok := got.(*grpcdriver.ScheduleStore); ok {
			t.Fatalf("resolveScheduleStore(accessor) store = *grpcdriver.ScheduleStore, want the store's own (no override set)")
		}
	})

	t.Run("override empty + memstore (no accessor) -> nil (no scheduling)", func(t *testing.T) {
		got, closeFn, err := resolveScheduleStore(Config{}, memstore.New())
		if err != nil {
			t.Fatalf("resolveScheduleStore(no-accessor): %v", err)
		}
		closeFn()
		if got != nil {
			t.Fatalf("resolveScheduleStore(no-accessor) store = %T, want nil (the byte-identical no-scheduling path)", got)
		}
	})
}

// TestBuild_ScheduleStoreOverrideWiresScheduleTool pins issue #257 Wave 3 at
// the composition level: with --schedule-store-url set + an accessor-LESS
// session store (memstore), the Schedule MANAGER built in buildEngine is
// NON-NIL (the override backs the in-chat Schedule tool too — the absent-tool
// gap is closed), and ServerCapabilities.Scheduling is true. The tick loop is
// DISABLED (--no-scheduler) so the Build never issues an RPC against the
// unreachable override driver (grpc.NewClient is lazy; the first RPC would
// surface a connect error, never reached here) — this isolates the TOOL wiring
// from the tick loop. The override + memstore combination is exactly the
// split-brain-free path: the tool + tick loop + fire path share the ONE
// resolveScheduleStore resolution (a memstore session store exposes no
// accessor, so there is no second registry to disagree with).
func TestBuild_ScheduleStoreOverrideWiresScheduleTool(t *testing.T) {
	ctx := context.Background()
	built, err := buildIsolated(t, ctx, Config{
		Workspace:           t.TempDir(),
		NoSoul:              true,
		NoUserModel:         true,
		SchedulerEnabled:    false,            // disable the tick loop; isolate the TOOL wiring
		ScheduleStoreURL:    "127.0.0.1:7445", // lazy dial — never RPC'd in this test
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("hi"))
		},
	})
	if err != nil {
		t.Fatalf("Build with --schedule-store-url + memstore (tick disabled) = %v, want a clean startup (the override backs the Schedule tool)", err)
	}
	defer built.Close()

	// The in-chat Schedule tool's manager is NON-NIL — the override closed the
	// absent-tool gap (a memstore session store exposes no accessor; without
	// the override the manager would be nil, byte-identical to the no-scheduling
	// path).
	if mgr := built.Service.ScheduleManager(); mgr == nil {
		t.Fatal("Service.ScheduleManager() = nil under --schedule-store-url + memstore, want non-nil (the override wires the in-chat Schedule tool)")
	}
	// No tick goroutine (--no-scheduler).
	if built.Service.HasScheduler() {
		t.Fatal("HasScheduler = true under --no-scheduler, want false (the tick loop is disabled, the tool wiring is what is asserted)")
	}
}

// TestBuild_ScheduleStoreOverrideStartsScheduler pins the scheduler-enabled arm
// of the override path: with SchedulerEnabled: true + ScheduleStoreURL set +
// memstore, Build succeeds (grpc.NewClient is lazy — no RPC is fired), the
// ScheduleManager is non-nil, and HasScheduler is true (the in-process tick
// loop is running). This is the scheduling-active counterpart of
// TestBuild_ScheduleStoreOverrideWiresScheduleTool, which isolates the TOOL
// wiring by disabling the tick loop.
func TestBuild_ScheduleStoreOverrideStartsScheduler(t *testing.T) {
	ctx := context.Background()
	built, err := buildIsolated(t, ctx, Config{
		Workspace:           t.TempDir(),
		NoSoul:              true,
		NoUserModel:         true,
		SchedulerEnabled:    true,             // enable the tick loop to assert startup + scheduling presence
		ScheduleStoreURL:    "127.0.0.1:7445", // lazy dial — never RPC'd in this test
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("hi"))
		},
	})
	if err != nil {
		t.Fatalf("Build with SchedulerEnabled + --schedule-store-url + memstore = %v, want a clean startup (the override backs the scheduler)", err)
	}
	defer built.Close()

	// The in-chat Schedule tool's manager is NON-NIL (the override backs it).
	if mgr := built.Service.ScheduleManager(); mgr == nil {
		t.Fatal("Service.ScheduleManager() = nil under SchedulerEnabled + override + memstore, want non-nil")
	}
	// The in-process tick loop is running.
	if !built.Service.HasScheduler() {
		t.Fatal("HasScheduler = false under SchedulerEnabled + override, want true (the tick loop is running)")
	}
}
