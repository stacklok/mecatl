package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestScheduleManagerConfig_ScheduleStoreOverride pins issue #257 Wave 3: the
// OPTIONAL ScheduleManagerConfig.ScheduleStore explicit override WINS over the
// Store type-assertion discovery, and a nil override keeps the byte-identical
// accessor-discovery posture. The three cases:
//  1. override set + accessor-less session store (memstore) → a NON-NIL
//     manager backed by the OVERRIDE (the absent-tool gap with an accessor-less
//     store + --schedule-store-url is closed — the tool + tick loop share the
//     one registry).
//  2. override nil + accessor-less session store (memstore) → a NIL manager
//     (byte-identical no-scheduling path — the override did not widen the gate).
//  3. override nil + accessor-ful session store (jsonlstore) → a NON-NIL
//     manager backed by the accessor (byte-identical to the pre-override
//     discovery).
func TestScheduleManagerConfig_ScheduleStoreOverride(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	nowFunc := func() time.Time { return now }

	t.Run("override wins over accessor-less store (absent-tool gap closed)", func(t *testing.T) {
		override := memschedulestore.New()
		mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
			Store:         memstore.New(), // accessor-less → accessor discovery yields nil
			ScheduleStore: override,       // the override must still construct a manager
			Now:           nowFunc,
		})
		if mgr == nil {
			t.Fatal("NewScheduleManager with a ScheduleStore override over an accessor-less store = nil, want non-nil (the override backs the tool)")
		}
		var pm port.ScheduleManager = mgr
		_ = pm
	})

	t.Run("nil override + accessor-less store -> nil (byte-identical)", func(t *testing.T) {
		if got := server.NewScheduleManager(server.ScheduleManagerConfig{
			Store: memstore.New(),
			Now:   nowFunc,
		}); got != nil {
			t.Errorf("NewScheduleManager with no override over an accessor-less store = %v, want nil (the byte-identical no-scheduling path)", got)
		}
	})

	t.Run("nil override + accessor-ful store -> accessor-backed manager (byte-identical)", func(t *testing.T) {
		store, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
			Store: store,
			Now:   nowFunc,
		})
		if mgr == nil {
			t.Fatal("NewScheduleManager with no override over an accessor-ful store = nil, want non-nil (the accessor discovery still works)")
		}
	})
}

// TestScheduleManagerConfig_ScheduleStoreOverridePreventsSplitBrain is the
// split-brain guard (issue #257 Wave 3): when BOTH an accessor-ful session
// store AND a ScheduleStore override are configured, the manager MUST read
// the OVERRIDE, not the accessor's store — the --schedule-store-url override
// backs the tick loop, so the tool must share that ONE registry (an
// accessor-ful store + the override is the split-brain case: the tool would
// manage the LOCAL store while the tick loop fires from the REMOTE one). It
// distinguishes the two via a sentinel schedule seeded ONLY into the override;
// the manager's GetSchedule hits the override fake and NOT the accessor fake.
func TestScheduleManagerConfig_ScheduleStoreOverridePreventsSplitBrain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	nowFunc := func() time.Time { return now }

	// The accessor-ful session store (jsonlstore) exposes its OWN
	// ScheduleStore — the LOCAL registry the tick loop would NOT read under
	// --schedule-store-url. Seed it with a sentinel the manager must NOT see.
	accessorStore, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	if as := accessorStore.ScheduleStore(); as == nil {
		t.Fatal("jsonlstore.ScheduleStore() = nil, want non-nil (the accessor the override must shadow)")
	} else if err := as.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name: "accessor-only", Prompt: "x", Trigger: port.TriggerSpec{Cron: "@every 1h"},
			Workspace: "/ws", Mode: "plan",
		},
	}); err != nil {
		t.Fatalf("seed accessor store: %v", err)
	}

	// The override (the REMOTE driver client the tick loop reads). Seed it
	// with a DIFFERENT sentinel the manager SHOULD see.
	override := memschedulestore.New()
	if err := override.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name: "override-only", Prompt: "x", Trigger: port.TriggerSpec{Cron: "@every 1h"},
			Workspace: "/ws", Mode: "plan",
		},
	}); err != nil {
		t.Fatalf("seed override store: %v", err)
	}

	mgr := server.NewScheduleManager(server.ScheduleManagerConfig{
		Store:         accessorStore, // accessor-ful (the split-brain half)
		ScheduleStore: override,      // the override wins for the tool
		Now:           nowFunc,
	})
	if mgr == nil {
		t.Fatal("NewScheduleManager with an override over an accessor-ful store = nil, want non-nil (the override wins)")
	}

	// The override-only sentinel is visible through the manager.
	if got, err := mgr.GetSchedule(ctx, "override-only"); err != nil {
		t.Errorf("GetSchedule(override-only) over the OVERRIDE = %v, want nil (the manager reads the override)", err)
	} else if got.Spec.Name != "override-only" {
		t.Errorf("GetSchedule(override-only) name = %q, want %q", got.Spec.Name, "override-only")
	}
	// The accessor-only sentinel is NOT visible through the manager — the
	// override shadowed the accessor's store, so the manager does NOT read it.
	if _, err := mgr.GetSchedule(ctx, "accessor-only"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("GetSchedule(accessor-only) over the OVERRIDE = %v, want port.ErrScheduleNotFound (the manager does NOT read the accessor's store — no split-brain)", err)
	}
}
