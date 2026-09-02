package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type placementStoreSpy struct {
	*memstore.Store
	mu      sync.Mutex
	creates int
}

func (s *placementStoreSpy) Create(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.creates++
	s.mu.Unlock()
	return s.Store.Create(ctx, sess)
}

func (s *placementStoreSpy) createCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates
}

type placementProviderSpy struct {
	mu            sync.Mutex
	bindCalls     int
	reattachCalls int
	reattachRef   session.EnvironmentRef
	worktrees     WorktreeLister
	selectors     *WorktreeSelectorIssuer
}

func (p *placementProviderSpy) Bind(_ context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	p.mu.Lock()
	p.bindCalls++
	p.mu.Unlock()
	if req.Selector.IsWorktree() {
		current, err := p.worktrees.List(context.Background(), req.Selector.SourceRef.ID)
		if err != nil {
			return PlacementBinding{}, ErrPlacementUnavailable
		}
		choice, err := p.selectors.Match(req.Selector.ID, req.Principal, req.Selector.Source, current)
		if err != nil {
			return PlacementBinding{}, err
		}
		if choice.Path == "/unavailable" {
			return PlacementBinding{}, ErrPlacementUnavailable
		}
		ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: choice.Path, Revision: choice.Head}
		return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(choice.Path), nil)}, nil
	}
	if req.Selector.Kind == PlacementSelectorNoFS {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}
		return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil)}, nil
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "base-head"}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(ref.ID), nil)}, nil
}

func (p *placementProviderSpy) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	p.mu.Lock()
	p.reattachCalls++
	p.reattachRef = req.Ref
	p.mu.Unlock()
	if req.Ref.Kind == session.EnvKindNoFS {
		return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), nil)}, nil
	}
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(req.Ref.ID), nil)}, nil
}

func (p *placementProviderSpy) ListWorktrees(ctx context.Context, req PlacementDiscoveryRequest) ([]ScopedWorktree, error) {
	current, err := p.worktrees.List(ctx, req.SourceRef.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ScopedWorktree, 0, len(current))
	for _, choice := range current {
		out = append(out, ScopedWorktree{Selector: p.selectors.Issue(req.Principal, req.Source, choice), Label: choice.Branch, Branch: choice.Branch, Revision: choice.Head})
	}
	return out, nil
}

func (p *placementProviderSpy) reset() {
	p.mu.Lock()
	p.bindCalls, p.reattachCalls = 0, 0
	p.mu.Unlock()
}

func (p *placementProviderSpy) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bindCalls, p.reattachCalls
}

type discoverySpy struct {
	mu        sync.Mutex
	calls     int
	worktrees []Worktree
}

func (s *discoverySpy) List(context.Context, string) ([]Worktree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]Worktree(nil), s.worktrees...), nil
}

func (s *discoverySpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type commandDiscoverySpy struct{ calls int }

func (s *commandDiscoverySpy) List(context.Context, string) ([]Command, error) {
	s.calls++
	return []Command{{Name: "forbidden"}}, nil
}

type placementLeaseSpy struct {
	mu       sync.Mutex
	acquires int
	err      error
}

func (s *placementLeaseSpy) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	return port.Lease{}, s.err
}
func (*placementLeaseSpy) Renew(context.Context, port.Lease) (port.Lease, error) {
	return port.Lease{}, nil
}
func (*placementLeaseSpy) Release(context.Context, port.Lease) error { return nil }

func newPlacementProofService(t *testing.T, store *placementStoreSpy, provider *placementProviderSpy, worktrees WorktreeLister, commands CommandLister, lease port.SessionLease, workspaceCalls *int, ids ...session.SessionID) *Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	var key [worktreeSelectorKeySize]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	issuer, err := NewWorktreeSelectorIssuer(key[:])
	if err != nil {
		t.Fatal(err)
	}
	provider.worktrees, provider.selectors = worktrees, issuer
	next := 0
	svc, err := NewService(Config{
		Engine: eng, Store: store, PlacementProvider: provider, PlacementScope: "tenant",
		Workspaces: func(root string) tool.Workspace {
			if workspaceCalls != nil {
				*workspaceCalls++
			}
			if root == "/unavailable" {
				return nil
			}
			return memfs.NewWorkspace(root)
		},
		Worktrees: worktrees, Commands: commands, SessionLease: lease, LeaseOwner: "proof", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
		Now: func() time.Time { return time.Unix(1, 0) }, NewID: func() session.SessionID {
			if next >= len(ids) {
				return session.SessionID("generated-extra")
			}
			id := ids[next]
			next++
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestADR_0280_ScopedWorktreeSelectorsFailClosed(t *testing.T) {
	store := &placementStoreSpy{Store: memstore.New()}
	provider := &placementProviderSpy{}
	inventory := &discoverySpy{worktrees: []Worktree{{Path: "/feature", Branch: "feature", Head: "h1"}}}
	workspaceCalls := 0
	svc := newPlacementProofService(t, store, provider, inventory, nil, nil, &workspaceCalls, "source-a", "source-b", "successor")
	alice := &session.Principal{Issuer: "issuer", Subject: "alice"}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob"}
	aliceCtx := session.WithPrincipal(context.Background(), alice)
	bobCtx := session.WithPrincipal(context.Background(), bob)
	svc.cfg.OwnershipEnforced = true

	for _, id := range []session.SessionID{"source-a", "source-b"} {
		if _, err := svc.CreateSession(aliceCtx, session.ModeDefault, session.Limits{}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	listed, err := svc.ListWorktreesForSession(aliceCtx, "source-a")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListWorktreesForSession = %+v, %v", listed, err)
	}
	token := listed[0].Selector
	baselineCreates := store.createCount()

	provider.reset()
	beforeInventory, beforeWorkspaces := inventory.count(), workspaceCalls
	if _, err := svc.ClearSessionSuccessor(bobCtx, "source-a", SuccessorPlacement{Selector: token}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner = %v, want hidden ErrNotFound", err)
	}
	if binds, reattaches := provider.calls(); binds != 0 || reattaches != 0 || inventory.count() != beforeInventory || workspaceCalls != beforeWorkspaces || store.createCount() != baselineCreates {
		t.Fatalf("wrong-owner selector invoked provider/filesystem/persistence: bind=%d reattach=%d inventory=%d workspace=%d creates=%d", binds, reattaches, inventory.count()-beforeInventory, workspaceCalls-beforeWorkspaces, store.createCount()-baselineCreates)
	}

	for name, sourceAndSelector := range map[string]struct {
		source   session.SessionID
		selector string
	}{
		"unknown":      {"source-a", "not-a-selector"},
		"wrong source": {"source-b", token},
	} {
		t.Run(name, func(t *testing.T) {
			beforeWorkspaces := workspaceCalls
			if _, err := svc.ClearSessionSuccessor(aliceCtx, sourceAndSelector.source, SuccessorPlacement{Selector: sourceAndSelector.selector}); !errors.Is(err, ErrPlacementNotFound) {
				t.Fatalf("ClearSessionSuccessor = %v, want ErrPlacementNotFound", err)
			}
			if workspaceCalls != beforeWorkspaces || store.createCount() != baselineCreates {
				t.Fatalf("fail-closed selector constructed target filesystem or persisted: workspace delta=%d create delta=%d", workspaceCalls-beforeWorkspaces, store.createCount()-baselineCreates)
			}
		})
	}

	inventory.mu.Lock()
	inventory.worktrees[0].Head = "h2"
	inventory.mu.Unlock()
	if _, err := svc.ClearSessionSuccessor(aliceCtx, "source-a", SuccessorPlacement{Selector: token}); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("stale selector = %v, want ErrPlacementNotFound", err)
	}
	if workspaceCalls != beforeWorkspaces || store.createCount() != baselineCreates {
		t.Fatalf("stale selector constructed target or persisted: workspace=%d creates=%d", workspaceCalls, store.createCount())
	}

	inventory.mu.Lock()
	inventory.worktrees = []Worktree{{Path: "/unavailable", Branch: "gone", Head: "h3"}}
	inventory.mu.Unlock()
	fresh, err := svc.ListWorktreesForSession(aliceCtx, "source-a")
	if err != nil || len(fresh) != 1 {
		t.Fatal(err)
	}
	if _, err := svc.ClearSessionSuccessor(aliceCtx, "source-a", SuccessorPlacement{Selector: fresh[0].Selector}); !errors.Is(err, ErrPlacementUnavailable) {
		t.Fatalf("unavailable selector = %v, want ErrPlacementUnavailable", err)
	}
	if store.createCount() != baselineCreates {
		t.Fatalf("unavailable selector persisted successor: creates=%d want %d", store.createCount(), baselineCreates)
	}
}

func TestADR_0280_NoFSDiscoveryDoesNotInvokeFilesystemProviders(t *testing.T) {
	store := &placementStoreSpy{Store: memstore.New()}
	provider := &placementProviderSpy{}
	worktrees := &discoverySpy{worktrees: []Worktree{{Path: "/must-not-run"}}}
	commands := &commandDiscoverySpy{}
	workspaceCalls := 0
	svc := newPlacementProofService(t, store, provider, worktrees, commands, nil, &workspaceCalls, "no-fs")
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}
	created := session.New("no-fs", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	created.Profile = string(ProfileNoFS)
	if err := store.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	provider.reset()
	workspaceCalls = 0
	if got, err := svc.ListCommandsForSession(context.Background(), created.ID); err != nil || len(got) != 0 {
		t.Fatalf("commands = %+v, %v", got, err)
	}
	if got, err := svc.ListWorktreesForSession(context.Background(), created.ID); err != nil || len(got) != 0 {
		t.Fatalf("worktrees = %+v, %v", got, err)
	}
	if binds, reattaches := provider.calls(); binds != 0 || reattaches != 0 || commands.calls != 0 || worktrees.count() != 0 || workspaceCalls != 0 {
		t.Fatalf("no-FS discovery touched provider/filesystem: bind=%d reattach=%d commands=%d worktrees=%d workspaces=%d", binds, reattaches, commands.calls, worktrees.count(), workspaceCalls)
	}
}

func TestADR_0280_ClearSessionIsLeaseSafeAndNonDestructive(t *testing.T) {
	store := &placementStoreSpy{Store: memstore.New()}
	provider := &placementProviderSpy{}
	lease := &placementLeaseSpy{}
	svc := newPlacementProofService(t, store, provider, nil, nil, lease, nil, "source", "must-not-exist")
	source, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{MaxTurns: 7})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	baselineCreates := store.createCount()
	lease.err = port.ErrLeaseHeld
	if _, err := svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{}); !errors.Is(err, ErrSessionLeasedElsewhere) {
		t.Fatalf("clear with peer lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	if store.createCount() != baselineCreates {
		t.Fatalf("lease failure published successor: creates=%d want %d", store.createCount(), baselineCreates)
	}
	if _, err := store.Load(context.Background(), "must-not-exist"); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("failed clear left target: %v", err)
	}
	after, err := store.Load(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.State != before.State || after.Mode != before.Mode || after.EnvironmentRef != before.EnvironmentRef || len(after.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("failed clear mutated source: before=%+v after=%+v", before, after)
	}
}

func TestInvariant_fork_placement_is_atomic_and_server_authorized(t *testing.T) {
	store := &placementStoreSpy{Store: memstore.New()}
	provider := &placementProviderSpy{}
	inventory := &discoverySpy{worktrees: []Worktree{{Path: "/feature", Branch: "feature", Head: "h1"}}}
	workspaceCalls := 0
	svc := newPlacementProofService(t, store, provider, inventory, nil, nil, &workspaceCalls, "source", "fork")
	source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	baselineCreates := store.createCount()
	provider.reset()
	beforeInventory := inventory.count()

	_, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID, Placement: SuccessorPlacement{Selector: "attacker"}, ModelID: "model-without-provider"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unauthorized model override = %v, want ErrInvalidArgument", err)
	}
	if binds, reattaches := provider.calls(); binds != 0 || reattaches != 0 || inventory.count() != beforeInventory || workspaceCalls != 0 || store.createCount() != baselineCreates {
		t.Fatalf("invalid fork invoked placement/filesystem/persistence: bind=%d reattach=%d inventory=%d workspace=%d creates=%d", binds, reattaches, inventory.count()-beforeInventory, workspaceCalls, store.createCount()-baselineCreates)
	}
	if _, err := store.Load(context.Background(), "fork"); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("failed fork left target: %v", err)
	}
	after, err := store.Load(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || after.EnvironmentRef != before.EnvironmentRef || len(after.Conversation.Messages) != len(before.Conversation.Messages) {
		t.Fatalf("failed fork mutated source: before=%+v after=%+v", before, after)
	}
}
