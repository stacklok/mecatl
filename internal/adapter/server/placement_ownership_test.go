package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

type ownershipPlacementProvider struct {
	ref         session.EnvironmentRef
	binds       atomic.Int32
	reattaches  atomic.Int32
	detaches    atomic.Int32
	rollbacks   atomic.Int32
	rollbackErr error
	afterBind   func(context.Context, PlacementBindRequest)
}

func (p *ownershipPlacementProvider) binding(reattach bool) PlacementBinding {
	if reattach {
		p.reattaches.Add(1)
	} else {
		p.binds.Add(1)
	}
	return PlacementBinding{
		Ref:            p.ref,
		Environment:    tool.MustEnvironment(p.ref, memfs.NewWorkspace("/owned"), memledger.New(), nil),
		GovernanceRoot: "/owned",
		Close:          func() error { p.detaches.Add(1); return nil },
		Rollback:       func() error { p.rollbacks.Add(1); return p.rollbackErr },
	}
}

func (p *ownershipPlacementProvider) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	binding := p.binding(false)
	if p.afterBind != nil {
		p.afterBind(ctx, req)
	}
	return binding, nil
}

func (p *ownershipPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	if req.Ref != p.ref {
		return PlacementBinding{}, ErrPlacementNotFound
	}
	return p.binding(true), nil
}

func newOwnershipService(t *testing.T, store *memstore.Store, provider *ownershipPlacementProvider) *Service {
	t.Helper()
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned", NewID: func() session.SessionID { return "owned-session" }, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPlacementFirstRunReattachIsOwnedUntilCloseSession(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	store := memstore.New()
	persisted := session.New("owned-session", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(t.Context(), persisted); err != nil {
		t.Fatal(err)
	}
	svc := newOwnershipService(t, store, provider)

	run, err := svc.StartRun(t.Context(), persisted.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.FinishRun(persisted.ID, run)
	if got := provider.reattaches.Load(); got != 1 {
		t.Fatalf("reattaches = %d, want 1", got)
	}
	if got := provider.detaches.Load(); got != 0 {
		t.Fatalf("detaches before CloseSession = %d, want 0", got)
	}

	svc.CloseSession(persisted.ID)
	if got := provider.detaches.Load(); got != 1 {
		t.Fatalf("detaches after CloseSession = %d, want 1", got)
	}
}

func TestPlacementRepeatedRunsAndShutdownShareOneOwner(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	store := memstore.New()
	persisted := session.New("owned-session", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(t.Context(), persisted); err != nil {
		t.Fatal(err)
	}
	svc := newOwnershipService(t, store, provider)
	for _, prompt := range []string{"one", "two"} {
		run, err := svc.StartRun(t.Context(), persisted.ID, prompt)
		if err != nil {
			t.Fatal(err)
		}
		for range run.Events() {
		}
		svc.FinishRun(persisted.ID, run)
	}
	if got := provider.reattaches.Load(); got != 1 {
		t.Fatalf("reattaches = %d, want 1", got)
	}
	svc.Close()
	if got := provider.detaches.Load(); got != 1 {
		t.Fatalf("shutdown detaches = %d, want 1", got)
	}
}

func TestPlacementBorrowersDelayDetachUntilLastRelease(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	store := memstore.New()
	persisted := session.New("owned-session", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(t.Context(), persisted); err != nil {
		t.Fatal(err)
	}
	svc := newOwnershipService(t, store, provider)
	_, releaseA, err := svc.borrowSessionPlacement(t.Context(), persisted)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseB, err := svc.borrowSessionPlacement(t.Context(), persisted)
	if err != nil {
		t.Fatal(err)
	}
	svc.CloseSession(persisted.ID)
	releaseA()
	if got := provider.detaches.Load(); got != 0 {
		t.Fatalf("detach with borrower live = %d, want 0", got)
	}
	releaseB()
	if got := provider.detaches.Load(); got != 1 {
		t.Fatalf("detach after final borrower = %d, want 1", got)
	}
}

func TestFailedCreateRollsBackUnpublishedPlacement(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	store := failingPlacementStore{Store: memstore.New(), err: errors.New("store failed")}
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err == nil {
		t.Fatal("CreateSession succeeded")
	}
	if got := provider.rollbacks.Load(); got != 1 {
		t.Fatalf("rollbacks = %d, want 1", got)
	}
	if got := provider.detaches.Load(); got != 0 {
		t.Fatalf("detach used instead of rollback: %d", got)
	}
}

func TestFailedFactoryCreateRollsBackUnpublishedPlacement(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned",
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return SessionEngineResult{}, errors.New("factory failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateSessionWithProvider(t.Context(), session.ModeDefault, session.Limits{}, ProviderSelector{ProviderID: "other"})
	if err == nil {
		t.Fatal("CreateSessionWithProvider succeeded")
	}
	if got := provider.rollbacks.Load(); got != 1 {
		t.Fatalf("rollbacks = %d, want 1", got)
	}
}

func TestCapacityFailurePrecedesPlacementProvisioning(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned", MaxSessionEngines: 1,
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return SessionEngineResult{Engine: repairEngine()}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.sessionEngines["full"] = &sessionEngine{engine: repairEngine()}
	svc.mu.Unlock()
	_, err = svc.CreateSessionWithProvider(t.Context(), session.ModeDefault, session.Limits{}, ProviderSelector{ProviderID: "other"})
	if !errors.Is(err, ErrTooManySessionEngines) {
		t.Fatalf("CreateSessionWithProvider = %v, want ErrTooManySessionEngines", err)
	}
	if got := provider.binds.Load(); got != 0 {
		t.Fatalf("placement binds = %d, want 0", got)
	}
}

func TestCollisionAfterProvisioningRollsBackUnpublishedPlacement(t *testing.T) {
	for _, perSession := range []bool{false, true} {
		name := "shared engine"
		if perSession {
			name = "per-session engine"
		}
		t.Run(name, func(t *testing.T) {
			ref := session.EnvironmentRef{Kind: "microvm", ID: "candidate", Revision: "1"}
			provider := &ownershipPlacementProvider{ref: ref}
			winnerRef := ref
			winnerRef.ID = "winner"
			winnerProvider := &ownershipPlacementProvider{ref: winnerRef}
			store := memstore.New()
			cfg := Config{Engine: repairEngine(), Store: store, PlacementProvider: winnerProvider, PlacementScope: "test", SharedEngineRoot: "/owned", OwnershipEnforced: true}
			sel := ProviderSelector{}
			if perSession {
				sel.ProviderID = "test"
				cfg.SessionEngine = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
					return SessionEngineResult{Engine: repairEngine()}, nil
				}
			}
			winnerSvc, err := NewService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(winnerSvc.Close)
			cfg.PlacementProvider = provider
			svc, err := NewService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
			ctx := session.WithPrincipal(t.Context(), owner)
			var winner *session.Session
			provider.afterBind = func(ctx context.Context, req PlacementBindRequest) {
				if provider.binds.Load() != 1 || req.BindingID != "collision" || !req.Principal.SameIdentity(owner) {
					t.Fatal("candidate was not provisioned for the exact binding and owner")
				}
				if _, err := store.Load(ctx, req.BindingID); !errors.Is(err, port.ErrSessionNotFound) {
					t.Fatalf("winner published before candidate provisioning: %v", err)
				}
				// Publish the competing placement only after the candidate's absence
				// probe and provisioning, but before its atomic Store.Create.
				var err error
				winner, err = winnerSvc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, sel, ProfileDefault, WithSessionID(req.BindingID))
				if err != nil {
					t.Fatal(err)
				}
			}
			created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, sel, ProfileDefault, WithSessionID("collision"))
			if !errors.Is(err, ErrInvalidArgument) || created != nil {
				t.Fatalf("colliding create = %v, %v; want exact-ref mismatch", created, err)
			}
			svc.Close()
			if provider.binds.Load() != 1 || provider.rollbacks.Load() != 1 || provider.detaches.Load() != 0 {
				t.Fatalf("candidate binds=%d rollbacks=%d closes=%d, want 1/1/0", provider.binds.Load(), provider.rollbacks.Load(), provider.detaches.Load())
			}
			stored, err := store.Load(ctx, "collision")
			if err != nil || winner == nil || stored.Incarnation() != winner.Incarnation() || stored.EnvironmentRef != winnerRef || !stored.Owner.SameIdentity(owner) || stored.Mode != winner.Mode || stored.Limits != winner.Limits || stored.ProviderID != winner.ProviderID {
				t.Fatalf("published winner changed: %v, %v", stored, err)
			}
			if winnerProvider.rollbacks.Load() != 0 || winnerProvider.detaches.Load() != 0 {
				t.Fatal("loser cleanup touched the published winner's placement")
			}
			winnerSvc.CloseSession(winner.ID)
			if winnerProvider.detaches.Load() != 1 || winnerProvider.rollbacks.Load() != 0 {
				t.Fatal("winner did not retain its own close-only placement lifetime")
			}
		})
	}
}

func TestKnownExplicitIDRetryDoesNotProvisionOrRollbackPlacement(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "winner", Revision: "1"}
	winnerProvider := &ownershipPlacementProvider{ref: ref}
	store := memstore.New()
	winnerSvc := newOwnershipService(t, store, winnerProvider)
	t.Cleanup(winnerSvc.Close)
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(t.Context(), owner)
	winner, err := winnerSvc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	retryProvider := &ownershipPlacementProvider{ref: ref}
	retrySvc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: retryProvider, PlacementScope: "test", SharedEngineRoot: "/owned", OwnershipEnforced: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retrySvc.Close)
	retried, err := retrySvc.CreateSessionWithProfile(ctx, winner.Mode, winner.Limits, ProviderSelector{}, ProfileDefault, WithSessionID(winner.ID))
	if err != nil || retried == nil || retried.Incarnation() != winner.Incarnation() || retried.EnvironmentRef != ref {
		t.Fatalf("known retry = %v, %v; want persisted winner", retried, err)
	}
	retrySvc.Close()
	if retryProvider.binds.Load() != 0 || retryProvider.rollbacks.Load() != 0 || retryProvider.reattaches.Load() != 1 || retryProvider.detaches.Load() != 1 {
		t.Fatalf("retry binds=%d rollbacks=%d reattaches=%d closes=%d, want 0/0/1/1", retryProvider.binds.Load(), retryProvider.rollbacks.Load(), retryProvider.reattaches.Load(), retryProvider.detaches.Load())
	}
	if winnerProvider.detaches.Load() != 0 || winnerProvider.rollbacks.Load() != 0 {
		t.Fatal("retry released the original winner's placement ownership")
	}
}

func TestRollbackFailureRetainsExplicitRecoveryDiagnostic(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref, rollbackErr: errors.New("dirty placement retained")}
	diag := &repairDiagnostics{}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: failingPlacementStore{Store: memstore.New(), err: errors.New("store failed")},
		PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned", Diagnostics: diag,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err == nil {
		t.Fatal("CreateSession succeeded")
	}
	if !strings.Contains(diag.text, "exact placement retained for recovery") {
		t.Fatalf("diagnostics = %q, want retryable recovery record", diag.text)
	}
}

func TestSuccessfulCreateNeverRollsBack(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "microvm", ID: "logical.environment", Revision: "1"}
	provider := &ownershipPlacementProvider{ref: ref}
	svc := newOwnershipService(t, memstore.New(), provider)
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.rollbacks.Load(); got != 0 {
		t.Fatalf("rollbacks = %d, want 0", got)
	}
	svc.CloseSession(created.ID)
}
