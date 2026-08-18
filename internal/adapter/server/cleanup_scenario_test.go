package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
)

var (
	cleanupAlice = &session.Principal{Issuer: "issuer", Subject: "alice"}
	cleanupBob   = &session.Principal{Issuer: "issuer", Subject: "bob"}
)

func cleanupContext(p *session.Principal) context.Context {
	return session.WithPrincipal(context.Background(), p)
}

func newCleanupService(t *testing.T, store port.SessionStore, now func() time.Time, policy RetentionPolicy, authorize func(context.Context) bool) *Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := NewService(Config{
		Engine: eng, Store: store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now: now, OwnershipEnforced: true, StorageManagementAuthorized: authorize, RetentionPolicy: policy,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func saveCleanupSession(t *testing.T, store port.SessionStore, id session.SessionID, owner *session.Principal, kind session.SessionKind) {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	if err := s.RestoreLabels(owner, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreSessionMetadata(kind, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStorageContinuity_Scenario5_CleanupDryRunIsReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-48 * time.Hour)
	store := memstore.New(memstore.WithNow(func() time.Time { return stamp }))
	saveCleanupSession(t, store, "old", cleanupAlice, session.SessionKindMain)
	before, _ := store.List(context.Background())
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: 24 * time.Hour}, func(context.Context) bool { return true })
	plan, err := svc.PlanSessionCleanup(cleanupContext(cleanupAlice), CleanupScope{})
	if err != nil {
		t.Fatalf("PlanSessionCleanup: %v", err)
	}
	after, _ := store.List(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("dry-run mutated store: before=%+v after=%+v", before, after)
	}
	if len(plan.Eligible) != 1 || plan.Eligible[0].Reason != "age" || plan.Eligible[0].ModifiedAt != stamp {
		t.Fatalf("plan eligible = %+v", plan.Eligible)
	}
	if plan.EligibleCounts.Total != 1 || plan.EligibleCounts.ByKind[string(session.SessionKindMain)] != 1 ||
		plan.EligibleCounts.ByState[string(session.StateIdle)] != 1 || plan.EligibleCounts.ByReason["age"] != 1 {
		t.Fatalf("eligible counts = %+v", plan.EligibleCounts)
	}
	if plan.Token == "" {
		t.Fatal("dry-run omitted confirmation token")
	}
}

func TestSessionStorageContinuity_Scenario5_ApplyRevalidatesPlan(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-48 * time.Hour)
	store := memstore.New(memstore.WithNow(func() time.Time { return stamp }))
	saveCleanupSession(t, store, "candidate", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{Version: "v1", MainMaxAge: 24 * time.Hour}, func(context.Context) bool { return true })
	ctx := cleanupContext(cleanupAlice)
	plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
	if err != nil {
		t.Fatal(err)
	}
	// A save advances the exact catalog generation. Apply must skip it rather than
	// deleting a candidate selected from stale metadata.
	stamp = now
	saveCleanupSession(t, store, "candidate", cleanupAlice, session.SessionKindMain)
	job, err := svc.ApplySessionCleanup(ctx, plan.Token)
	if !errors.Is(err, ErrCleanupPlanStale) {
		t.Fatalf("apply error = %v, want ErrCleanupPlanStale", err)
	}
	if job.Deleted != 0 || job.Stale == 0 {
		t.Fatalf("stale job = %+v", job)
	}
	if _, err := store.Load(context.Background(), "candidate"); err != nil {
		t.Fatalf("stale apply deleted candidate: %v", err)
	}
}

func TestSessionStorageContinuity_Scenario5_CrossCallerPlanReplayDenied(t *testing.T) {
	now := time.Now().UTC()
	store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-time.Hour) }))
	saveCleanupSession(t, store, "alice-secret-id", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxCount: 0, MainMaxAge: time.Minute}, func(context.Context) bool { return true })
	plan, err := svc.PlanSessionCleanup(cleanupContext(cleanupAlice), CleanupScope{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ApplySessionCleanup(cleanupContext(cleanupBob), plan.Token)
	if !errors.Is(err, ErrManagementUnauthorized) || strings.Contains(err.Error(), "alice-secret-id") {
		t.Fatalf("cross-caller replay error = %q", err)
	}
}

func TestSessionStorageContinuity_Scenario5_PartialFailureAndUnsupported(t *testing.T) {
	now := time.Now().UTC()
	unsupported := newCleanupService(t, memstoreWithoutPrune{SessionStore: memstore.New()}, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(context.Context) bool { return true })
	plan, err := unsupported.PlanSessionCleanup(cleanupContext(cleanupAlice), CleanupScope{})
	if err != nil || plan.Available || plan.UnavailableReason != "backend_unsupported" {
		t.Fatalf("unsupported plan = %+v err=%v", plan, err)
	}

	store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-time.Hour) }), memstore.WithDeleteFailure("fail", errors.New("raw /private/path SECRET=oops")))
	saveCleanupSession(t, store, "fail", cleanupAlice, session.SessionKindMain)
	saveCleanupSession(t, store, "ok", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(context.Context) bool { return true })
	ctx := cleanupContext(cleanupAlice)
	p, _ := svc.PlanSessionCleanup(ctx, CleanupScope{})
	job, err := svc.ApplySessionCleanup(ctx, p.Token)
	if err != nil {
		t.Fatalf("ApplySessionCleanup: %v", err)
	}
	if job.Deleted != 1 || job.Failed != 1 || len(job.Errors) != 1 || job.Errors[0].ReasonCode != cleanupBackendFailure {
		t.Fatalf("partial job = %+v", job)
	}
	if _, err := store.Load(context.Background(), "fail"); err != nil {
		t.Fatalf("failed item was not retained for retry: %v", err)
	}
	retry, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
	if err != nil || len(retry.Eligible) != 1 || retry.Eligible[0].ID != "fail" {
		t.Fatalf("retry plan = %+v, err=%v", retry, err)
	}
}

type memstoreWithoutPrune struct{ port.SessionStore }

func TestSessionStorageContinuity_Scenario5_CleanupErrorsAreSanitized(t *testing.T) {
	now := time.Now().UTC()
	raw := "raw /private/path SECRET=oops"
	store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-time.Hour) }), memstore.WithDeleteFailure("fail", errors.New(raw)))
	saveCleanupSession(t, store, "fail", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(context.Context) bool { return true })
	ctx := cleanupContext(cleanupAlice)
	plan, _ := svc.PlanSessionCleanup(ctx, CleanupScope{})
	job, _ := svc.ApplySessionCleanup(ctx, plan.Token)
	if strings.Contains(job.Errors[0].Message, raw) || strings.Contains(job.Errors[0].Message, "/private") || strings.Contains(job.Errors[0].Message, "SECRET") {
		t.Fatalf("raw backend detail escaped: %+v", job.Errors[0])
	}
}

func TestSessionStorageContinuity_Scenario5_AutomaticManualPlannerParity(t *testing.T) {
	now := time.Now().UTC()
	rows := []port.SessionDiscoveryMeta{
		{ID: "b", ModifiedAt: now.Add(-time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: cleanupAlice},
		{ID: "a", ModifiedAt: now.Add(-2 * time.Hour), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: cleanupAlice},
	}
	policy := RetentionPolicy{MainMaxCount: 1}
	auto := PlanAutomaticRetention(rows, policy, cleanupAlice, nil, nil, now)
	manual := PlanManualRetention(rows, policy, cleanupAlice, nil, nil, now)
	if !reflect.DeepEqual(auto.Eligible, manual.Eligible) || auto.Generation != manual.Generation {
		t.Fatalf("automatic=%+v manual=%+v", auto, manual)
	}
}

func TestSessionStorageContinuity_Scenario5_DeterministicCleanupOrdering(t *testing.T) {
	now := time.Now().UTC()
	rows := []port.SessionDiscoveryMeta{
		{ID: "z", ModifiedAt: now, State: session.StateCompleted, Kind: session.SessionKindMain, Owner: cleanupAlice},
		{ID: "a", ModifiedAt: now, State: session.StateCompleted, Kind: session.SessionKindMain, Owner: cleanupAlice},
		{ID: "m", ModifiedAt: now.Add(-time.Second), State: session.StateCompleted, Kind: session.SessionKindMain, Owner: cleanupAlice},
	}
	plan := PlanManualRetention(rows, RetentionPolicy{MainMaxCount: 1}, cleanupAlice, nil, nil, now)
	got := []session.SessionID{plan.Eligible[0].ID, plan.Eligible[1].ID}
	want := []session.SessionID{"m", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSessionStorageContinuity_Scenario5_ManagementAuthorizationAndNoOracle(t *testing.T) {
	now := time.Now().UTC()
	store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-time.Hour) }))
	saveCleanupSession(t, store, "hidden-id", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(ctx context.Context) bool {
		p := session.PrincipalFromContext(ctx)
		return p != nil && p.Subject == cleanupAlice.Subject
	})
	aliceCtx := cleanupContext(cleanupAlice)
	plan, err := svc.PlanSessionCleanup(aliceCtx, CleanupScope{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := svc.CancelSessionCleanup(aliceCtx, plan.JobID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancelled job = %+v, err=%v", cancelled, err)
	}
	applied, err := svc.ApplySessionCleanup(aliceCtx, plan.Token)
	if err != nil || applied.State != "cancelled" || applied.Deleted != 0 {
		t.Fatalf("apply after cancel = %+v, err=%v", applied, err)
	}
	if _, err := store.Load(context.Background(), "hidden-id"); err != nil {
		t.Fatalf("cancelled cleanup deleted hidden-id: %v", err)
	}
	for name, call := range map[string]func(context.Context) error{
		"plan":   func(ctx context.Context) error { _, err := svc.PlanSessionCleanup(ctx, CleanupScope{}); return err },
		"apply":  func(ctx context.Context) error { _, err := svc.ApplySessionCleanup(ctx, "opaque"); return err },
		"cancel": func(ctx context.Context) error { _, err := svc.CancelSessionCleanup(ctx, "opaque"); return err },
		"job":    func(ctx context.Context) error { _, err := svc.SessionCleanupJob(ctx, "opaque"); return err },
		"health": func(ctx context.Context) error { _, err := svc.StorageHealth(ctx); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := call(cleanupContext(cleanupBob))
			if !errors.Is(err, ErrManagementUnauthorized) || strings.Contains(err.Error(), "hidden-id") {
				t.Fatalf("error = %q", err)
			}
		})
	}
}
