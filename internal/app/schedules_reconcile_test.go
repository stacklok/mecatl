package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// newReconcileService builds a *server.Service over a jsonlstore (which exposes a
// ScheduleStore) for the reconcile idempotency tests, mirroring the helper in
// internal/adapter/server/schedule_test.go.
func newReconcileService(t *testing.T, now time.Time) *server.Service {
	t.Helper()
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
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
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestReconcileSchedulesSingletonFalseIdempotent: a declaration with
// singleton: false must NOT churn on every restart. The create-seam forces
// Singleton=true (applyScheduleDefaults, because scheduleSingletonExplicit is a
// stub), so the stored spec carries Singleton=true. Without normalising the
// declared spec before the diff, reconcile would see false≠true and Update every
// restart. After the fix, the second reconcile is a no-op (no Update, so the
// store-side CreatedAt is unchanged).
func TestReconcileSchedulesSingletonFalseIdempotent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newReconcileService(t, now)
	ctx := context.Background()

	decl := port.ScheduleSpec{
		Name:      "nightly",
		Prompt:    "review",
		Trigger:   port.TriggerSpec{Cron: "0 9 * * *"},
		Mutating:  false,
		Mode:      session.ModePlan,
		Workspace: "/tmp",
		// Singleton left false — the create-seam normalises it to true.
	}
	cfg := Config{DeclaredSchedules: []port.ScheduleSpec{decl}}

	// First reconcile: creates the schedule.
	reconcileSchedules(ctx, cfg, svc)
	after1, err := svc.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules after first reconcile: %v", err)
	}
	if len(after1) != 1 {
		t.Fatalf("expected 1 schedule after first reconcile; got %d", len(after1))
	}
	if !after1[0].Spec.Singleton {
		t.Fatalf("stored Singleton = false, want true (create-seam default)")
	}
	createdBefore := after1[0].Spec.CreatedAt
	if createdBefore.IsZero() {
		t.Fatalf("CreatedAt is zero after create; the create-seam must stamp it")
	}

	// Second reconcile: must be a no-op. A spurious Update would (a) rewrite the
	// store and, before the CreatedAt-preservation fix, clobber CreatedAt to the
	// zero value. Assert the stored schedule is byte-identical.
	reconcileSchedules(ctx, cfg, svc)
	after2, err := svc.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules after second reconcile: %v", err)
	}
	if len(after2) != 1 {
		t.Fatalf("expected 1 schedule after second reconcile; got %d", len(after2))
	}
	if !after2[0].Spec.CreatedAt.Equal(createdBefore) {
		t.Fatalf("CreatedAt changed across a no-op reconcile: before=%v after=%v",
			createdBefore, after2[0].Spec.CreatedAt)
	}
	if after2[0].Spec.Singleton != true {
		t.Fatalf("Singleton drifted across a no-op reconcile: %v", after2[0].Spec.Singleton)
	}
}

// TestToScheduleSpecMutatingFalseWithoutModeDefaultsToPlan: a YAML declaration
// with mutating: false and no explicit mode: must default to plan mode. Without
// the defaulting, validateScheduleSpec would reject it on reconcile (a
// non-mutating schedule must use plan mode). This is the Fix 3 defaulting, plus
// an end-to-end reconcile to confirm the created schedule is stored in plan mode.
func TestToScheduleSpecMutatingFalseWithoutModeDefaultsToPlan(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc := newReconcileService(t, now)
	ctx := context.Background()

	spec, err := toScheduleSpec(permconfig.ScheduleDecl{
		Name:      "read-only",
		Cron:      "0 9 * * *",
		Prompt:    "scan",
		Mutating:  false,
		Workspace: "/tmp",
		// Mode intentionally empty — toScheduleSpec must default it to plan.
	})
	if err != nil {
		t.Fatalf("toScheduleSpec: %v", err)
	}
	if spec.Mode != session.ModePlan {
		t.Fatalf("defaulted Mode = %q, want %q", spec.Mode, session.ModePlan)
	}

	cfg := Config{DeclaredSchedules: []port.ScheduleSpec{spec}}
	reconcileSchedules(ctx, cfg, svc)

	got, err := svc.GetSchedule(ctx, "read-only")
	if err != nil {
		t.Fatalf("GetSchedule after reconcile: %v (the non-mutating declaration should have created, not failed)", err)
	}
	if got.Spec.Mode != session.ModePlan {
		t.Fatalf("stored Mode = %q, want plan", got.Spec.Mode)
	}
}
