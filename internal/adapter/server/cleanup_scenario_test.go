package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
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
	return newCleanupServiceWithUpdate(t, store, now, policy, authorize, nil)
}

func newCleanupServiceWithUpdate(t *testing.T, store port.SessionStore, now func() time.Time, policy RetentionPolicy, authorize func(context.Context) bool, update func(StorageMaintenanceEvent)) *Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := NewService(Config{
		Engine: eng, Store: store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now: now, OwnershipEnforced: true, StorageManagementAuthorized: authorize, RetentionPolicy: policy,
		LocalStorageMaintenanceSingleWriter: true,
		StorageMaintenanceUpdate:            update,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

type maintenanceErrorLease struct{ err error }

func (l maintenanceErrorLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	return port.Lease{}, l.err
}
func (l maintenanceErrorLease) Renew(context.Context, port.Lease) (port.Lease, error) {
	return port.Lease{}, l.err
}
func (maintenanceErrorLease) Release(context.Context, port.Lease) error { return nil }

func maintenanceSafetyService(t *testing.T, store port.SessionStore, local bool, lease port.SessionLease, now time.Time) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now: func() time.Time { return now }, OwnershipEnforced: true,
		StorageManagementAuthorized:         func(context.Context) bool { return true },
		LocalStorageMaintenanceSingleWriter: local,
		SessionLease:                        lease, LeaseOwner: "maintenance-server", LeaseTTL: time.Minute, LeaseRenewInterval: 20 * time.Second,
		RetentionPolicy: RetentionPolicy{MainMaxAge: time.Hour},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestMaintenanceMutationCapabilityRequiresProvenExclusion(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	ctx := cleanupContext(cleanupAlice)

	t.Run("embedded single writer allowed", func(t *testing.T) {
		store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-48 * time.Hour) }))
		svc := maintenanceSafetyService(t, store, true, nil, now)
		if !svc.capabilities().GetStorageCleanup() {
			t.Fatal("embedded single-writer cleanup was not advertised")
		}
	})

	t.Run("remote without lease denied and unadvertised", func(t *testing.T) {
		store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-48 * time.Hour) }))
		saveCleanupSession(t, store, "remote-no-lease", cleanupAlice, session.SessionKindMain)
		svc := maintenanceSafetyService(t, store, false, nil, now)
		if svc.capabilities().GetStorageCleanup() {
			t.Fatal("remote cleanup without a lease was advertised")
		}
		plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
		if err != nil || plan.Available || plan.UnavailableReason != "maintenance_exclusion_unavailable" {
			t.Fatalf("unsafe cleanup plan = %+v, %v", plan, err)
		}
		if _, err := store.Load(ctx, "remote-no-lease"); err != nil {
			t.Fatalf("unsafe posture deleted the session: %v", err)
		}
		page, err := store.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
		if err != nil || len(page.Sessions) != 1 {
			t.Fatalf("metadata page = %+v, %v", page, err)
		}
		if err := svc.DeleteSessionForRetentionCandidate(ctx, page.Sessions[0]); !errors.Is(err, ErrMaintenanceExclusionUnavailable) {
			t.Fatalf("automatic retention delete = %v, want ErrMaintenanceExclusionUnavailable", err)
		}
		if _, err := store.Load(ctx, "remote-no-lease"); err != nil {
			t.Fatalf("automatic retention bypassed maintenance exclusion: %v", err)
		}
	})

	t.Run("remote with lease allowed and dry-run releases probe", func(t *testing.T) {
		store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-48 * time.Hour) }))
		const id = session.SessionID("remote-probe-release")
		saveCleanupSession(t, store, id, cleanupAlice, session.SessionKindMain)
		lease := memlease.New(wallclock.Clock{}, time.Minute)
		svc := maintenanceSafetyService(t, store, false, lease, now)
		if !svc.capabilities().GetStorageCleanup() {
			t.Fatal("leased remote cleanup was not advertised")
		}
		plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
		if err != nil || !plan.Available || len(plan.Eligible) != 1 {
			t.Fatalf("dry-run plan = %+v, %v", plan, err)
		}
		probe, err := lease.Acquire(ctx, id, "post-plan-proof")
		if err != nil {
			t.Fatalf("dry-run leaked its trial lease: %v", err)
		}
		if err := lease.Release(ctx, probe); err != nil {
			t.Fatalf("release proof lease: %v", err)
		}
	})

	t.Run("cross-process lease is protected at planning instant", func(t *testing.T) {
		store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-48 * time.Hour) }))
		const id = session.SessionID("remote-peer-live")
		saveCleanupSession(t, store, id, cleanupAlice, session.SessionKindMain)
		lease := memlease.New(wallclock.Clock{}, time.Minute)
		peer, err := lease.Acquire(ctx, id, "peer")
		if err != nil {
			t.Fatal(err)
		}
		svc := maintenanceSafetyService(t, store, false, lease, now)
		plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
		if err != nil || !plan.Available || len(plan.Eligible) != 0 || plan.Protected.ByReason["leased"] != 1 {
			t.Fatalf("peer-leased dry-run = %+v, %v", plan, err)
		}
		if err := lease.Release(ctx, peer); err != nil {
			t.Fatal(err)
		}
	})

	for name, leaseErr := range map[string]error{
		"unsupported lease": port.ErrLeaseUnsupported,
		"held lease":        port.ErrLeaseHeld,
	} {
		t.Run(name+" fails closed", func(t *testing.T) {
			store := memstore.New(memstore.WithNow(func() time.Time { return now.Add(-48 * time.Hour) }))
			id := session.SessionID("protected-" + name)
			saveCleanupSession(t, store, id, cleanupAlice, session.SessionKindMain)
			svc := maintenanceSafetyService(t, store, false, maintenanceErrorLease{err: leaseErr}, now)
			plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
			if err != nil {
				t.Fatalf("PlanSessionCleanup: %v", err)
			}
			if errors.Is(leaseErr, port.ErrLeaseUnsupported) {
				if plan.Available || plan.UnavailableReason != "maintenance_exclusion_unavailable" {
					t.Fatalf("unsupported lease plan = %+v", plan)
				}
				if svc.capabilities().GetStorageCleanup() {
					t.Fatal("unsupported lease remained advertised after detection")
				}
			} else if !plan.Available || len(plan.Eligible) != 0 || plan.Protected.ByReason["leased"] != 1 {
				t.Fatalf("held lease plan was not protected: %+v", plan)
			}
			if _, err := store.Load(ctx, id); err != nil {
				t.Fatalf("cleanup planning deleted lease-protected session: %v", err)
			}
		})
	}
}

func saveCleanupSession(t *testing.T, store port.SessionStore, id session.SessionID, owner *session.Principal, kind session.SessionKind) {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
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

func TestCleanupOwnershipCutoverExcludesOwnerlessSessions(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-48 * time.Hour)
	store := memstore.New(memstore.WithNow(func() time.Time { return stamp }))
	saveCleanupSession(t, store, "legacy-ownerless", nil, session.SessionKindMain)
	saveCleanupSession(t, store, "alice-owned", cleanupAlice, session.SessionKindMain)
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: 24 * time.Hour}, func(context.Context) bool { return true })

	plan, err := svc.PlanSessionCleanup(cleanupContext(cleanupAlice), CleanupScope{})
	if err != nil {
		t.Fatalf("PlanSessionCleanup: %v", err)
	}
	if len(plan.Eligible) != 1 || plan.Eligible[0].ID != "alice-owned" {
		t.Fatalf("cleanup eligible = %+v, want only owned session", plan.Eligible)
	}
	job, err := svc.ApplySessionCleanup(cleanupContext(cleanupAlice), plan.Token)
	if err != nil || job.Deleted != 1 {
		t.Fatalf("ApplySessionCleanup = (%+v, %v), want one owned deletion", job, err)
	}
	if _, err := store.Load(context.Background(), "legacy-ownerless"); err != nil {
		t.Fatalf("ownerless session changed during cleanup: %v", err)
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
	var lifecycle []StorageMaintenanceEvent
	svc := newCleanupServiceWithUpdate(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(context.Context) bool { return true }, func(event StorageMaintenanceEvent) {
		lifecycle = append(lifecycle, event)
	})
	ctx := cleanupContext(cleanupAlice)
	p, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
	if err != nil {
		t.Fatalf("PlanSessionCleanup: %v", err)
	}
	job, err := svc.ApplySessionCleanup(ctx, p.Token)
	if err != nil {
		t.Fatalf("ApplySessionCleanup: %v", err)
	}
	if job.Deleted != 1 || job.Failed != 1 || len(job.Errors) != 1 || job.Errors[0].ReasonCode != cleanupBackendFailure {
		t.Fatalf("partial job = %+v", job)
	}
	if len(lifecycle) < 3 || lifecycle[0].State != StorageMaintenanceStarted || lifecycle[len(lifecycle)-1].State != StorageMaintenanceCompleted ||
		lifecycle[len(lifecycle)-1].Failure != "cleanup: one or more items failed" || strings.Contains(lifecycle[len(lifecycle)-1].Failure, "private/path") {
		t.Fatalf("cleanup lifecycle = %+v", lifecycle)
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

type cleanupWorkStore struct {
	*memstore.Store
	pageCalls   atomic.Int64
	loadCalls   atomic.Int64
	deleteCalls atomic.Int64
}

func (s *cleanupWorkStore) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	s.pageCalls.Add(1)
	return s.Store.PageSessionMetadata(ctx, request)
}

func (s *cleanupWorkStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loadCalls.Add(1)
	return s.Store.Load(ctx, id)
}

func (s *cleanupWorkStore) DeleteSessionIfUnchanged(ctx context.Context, candidate port.SessionDiscoveryMeta) (bool, error) {
	s.deleteCalls.Add(1)
	return s.Store.DeleteSessionIfUnchanged(ctx, candidate)
}

func TestCleanupApplyUsesOneExactDeleteOperationPerCandidate(t *testing.T) {
	now := time.Now().UTC()
	store := &cleanupWorkStore{Store: memstore.New(memstore.WithNow(func() time.Time { return now.Add(-time.Hour) }))}
	for _, id := range []session.SessionID{"first", "second", "third"} {
		saveCleanupSession(t, store, id, cleanupAlice, session.SessionKindMain)
	}
	svc := newCleanupService(t, store, func() time.Time { return now }, RetentionPolicy{MainMaxAge: time.Minute}, func(context.Context) bool { return true })
	ctx := cleanupContext(cleanupAlice)
	plan, err := svc.PlanSessionCleanup(ctx, CleanupScope{})
	if err != nil || len(plan.Eligible) != 3 {
		t.Fatalf("PlanSessionCleanup = %+v, %v", plan, err)
	}
	store.pageCalls.Store(0)
	store.loadCalls.Store(0)
	store.deleteCalls.Store(0)

	job, err := svc.ApplySessionCleanup(ctx, plan.Token)
	if err != nil || job.Deleted != 3 {
		t.Fatalf("ApplySessionCleanup = %+v, %v", job, err)
	}
	if pages, loads, deletes := store.pageCalls.Load(), store.loadCalls.Load(), store.deleteCalls.Load(); pages != 1 || loads != 3 || deletes != 3 {
		t.Fatalf("apply work = pages:%d loads:%d exact-deletes:%d, want 1:%d:%d", pages, loads, deletes, len(plan.Eligible), len(plan.Eligible))
	}
}

func TestCleanupDeletionReasonsMatchRetentionCandidateOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"deleted": {want: ""},
		"changed": {err: errRetentionCandidateChanged, want: maintenanceReasonChanged},
		"active":  {err: errRetentionCandidateActive, want: maintenanceReasonActive},
		"leased":  {err: ErrSessionLeasedElsewhere, want: maintenanceReasonLeased},
		"backend": {err: ErrInternal, want: cleanupBackendFailure},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cleanupDeletionReason(tc.err); got != tc.want {
				t.Fatalf("cleanupDeletionReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestSessionStorageContinuity_Scenario5_AutomaticManualPlannerParity (AC5.5)
// now lives in internal/app/childgc_test.go, where it can drive the REAL
// automatic sweep (childGC.sweep) rather than a same-package shim: see the
// note on PlanManualRetention above for why the old in-package version was
// tautological.

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
