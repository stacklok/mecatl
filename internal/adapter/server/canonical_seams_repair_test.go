package server

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

func TestInvariant_canonical_placement_seams_remove_legacy_authority(t *testing.T) {
	files := []string{"service.go", "team.go"}
	forbiddenMethods := map[string]bool{"ForkSession": true, "ListCommands": true, "ListWorktrees": true, "CreateTeam": true}
	forbiddenFields := map[string]bool{"Workspaces": true, "CommandRunner": true, "CommandRunnerFactory": true, "Worktrees": true, "DefaultWorkspace": true}
	for _, name := range files {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncDecl:
				if n.Recv != nil && forbiddenMethods[n.Name.Name] {
					t.Errorf("legacy Service.%s remains exported", n.Name.Name)
				}
			case *ast.TypeSpec:
				if n.Name.Name != "Config" {
					return true
				}
				st, ok := n.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range st.Fields.List {
					for _, fieldName := range field.Names {
						if forbiddenFields[fieldName.Name] {
							t.Errorf("legacy Config.%s remains exported", fieldName.Name)
						}
					}
				}
			}
			return true
		})
	}
}

type revisionPlacementProvider struct {
	mu             sync.Mutex
	binds          int
	reattachCalls  []PlacementReattachRequest
	reattachResult *PlacementBinding
	reattachErr    error
}

func (*revisionPlacementProvider) binding(revision string) PlacementBinding {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: revision}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil), GovernanceRoot: "/same-root"}
}

func (p *revisionPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.binds++
	return p.binding(string(rune('0' + p.binds))), nil
}

func (p *revisionPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reattachCalls = append(p.reattachCalls, req)
	if p.reattachErr != nil {
		return PlacementBinding{}, p.reattachErr
	}
	if p.reattachResult != nil {
		return *p.reattachResult, nil
	}
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil), GovernanceRoot: "/same-root"}, nil
}

func (*revisionPlacementProvider) ListWorktrees(context.Context, PlacementDiscoveryRequest) ([]ScopedWorktree, error) {
	return nil, nil
}

func TestInvariant_create_retry_reattaches_complete_environment_ref(t *testing.T) {
	provider := &revisionPlacementProvider{}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
		OwnershipEnforced: true, NewID: func() session.SessionID { return "fixed" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "subject"})
	first, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed"))
	if err != nil {
		t.Fatal(err)
	}
	if first.EnvironmentRef.Revision != "1" {
		t.Fatalf("first ref = %+v, want provider's complete create binding", first.EnvironmentRef)
	}
	retry, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed"))
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	binds := provider.binds
	reattachCalls := append([]PlacementReattachRequest(nil), provider.reattachCalls...)
	provider.mu.Unlock()
	if retry.EnvironmentRef != first.EnvironmentRef || binds != 2 {
		t.Fatalf("retry ref=%+v binds=%d, want exact ref %+v without another bind", retry.EnvironmentRef, binds, first.EnvironmentRef)
	}
	if len(reattachCalls) != 1 {
		t.Fatalf("reattach calls=%d, want 1", len(reattachCalls))
	}
	gotReq := reattachCalls[0]
	if gotReq.Ref != first.EnvironmentRef || gotReq.BindingID != first.ID || gotReq.Scope != "test" || gotReq.Principal == nil || gotReq.Principal.Issuer != "issuer" || gotReq.Principal.Subject != "subject" {
		t.Fatalf("reattach request=%+v, want exact persisted ref/binding/principal/scope", gotReq)
	}
}

func TestInvariant_existing_create_validates_and_closes_supplied_placement_before_exact_reattach(t *testing.T) {
	provider := &revisionPlacementProvider{}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
		OwnershipEnforced: true, NewID: func() session.SessionID { return "fixed" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "subject"})
	created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed"))
	if err != nil {
		t.Fatal(err)
	}
	binding := func(ref session.EnvironmentRef, closeCount *int) PlacementBinding {
		return PlacementBinding{
			Ref:         ref,
			Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil),
			Close:       func() error { *closeCount++; return nil },
		}
	}

	t.Run("matching", func(t *testing.T) {
		closed := 0
		got, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault,
			WithSessionID("fixed"), WithPlacementBinding(binding(created.EnvironmentRef, &closed)))
		if err != nil {
			t.Fatal(err)
		}
		if got.EnvironmentRef != created.EnvironmentRef || closed != 1 {
			t.Fatalf("retry ref=%+v close calls=%d, want exact persisted ref and one close", got.EnvironmentRef, closed)
		}
	})

	t.Run("mismatched", func(t *testing.T) {
		closed := 0
		wrong := created.EnvironmentRef
		wrong.Revision = "wrong"
		_, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault,
			WithSessionID("fixed"), WithPlacementBinding(binding(wrong, &closed)))
		if !errors.Is(err, ErrInvalidPlacementBinding) || closed != 1 {
			t.Fatalf("error=%v close calls=%d, want invalid placement and one close", err, closed)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		closed := 0
		invalid := PlacementBinding{Close: func() error { closed++; return nil }}
		_, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault,
			WithSessionID("fixed"), WithPlacementBinding(invalid))
		if !errors.Is(err, ErrInvalidPlacementBinding) || closed != 1 {
			t.Fatalf("error=%v close calls=%d, want invalid placement and one close", err, closed)
		}
	})
}

type bindOnlyPlacementProvider struct{ binding PlacementBinding }

func (p *bindOnlyPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	return p.binding, nil
}

func TestInvariant_exact_reattach_fails_closed_when_missing_or_mismatched(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: "exact"}
	binding := PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil)}
	missing := &PlacementBinder{provider: &bindOnlyPlacementProvider{binding: binding}}
	if _, err := missing.Reattach(t.Context(), PlacementReattachRequest{Ref: ref, Scope: "test", BindingID: "fixed"}); !errors.Is(err, ErrPlacementUnavailable) {
		t.Fatalf("missing reattach error=%v, want placement unavailable", err)
	}
	wrongRef := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: "wrong"}
	provider := &revisionPlacementProvider{reattachResult: &PlacementBinding{Ref: wrongRef, Environment: tool.MustEnvironment(wrongRef, memfs.NewWorkspace("/same-root"), memledger.New(), nil)}}
	mismatched := &PlacementBinder{provider: provider}
	if _, err := mismatched.Reattach(t.Context(), PlacementReattachRequest{Ref: ref, Scope: "test", BindingID: "fixed"}); !errors.Is(err, ErrInvalidPlacementBinding) {
		t.Fatalf("mismatched reattach error=%v, want invalid binding", err)
	}
}

type concurrentCreatePlacementProvider struct {
	mu      sync.Mutex
	waiting int
	gate    chan struct{}
}

func (*concurrentCreatePlacementProvider) binding(id session.SessionID) PlacementBinding {
	revision := "probe"
	if id != "" {
		revision = "rev-" + string(id)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: revision}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil)}
}

func (p *concurrentCreatePlacementProvider) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	p.mu.Lock()
	gate := p.gate
	if gate != nil && req.BindingID != "" {
		p.waiting++
		if p.waiting == 2 {
			close(gate)
		}
	}
	p.mu.Unlock()
	if gate != nil && req.BindingID != "" {
		select {
		case <-gate:
		case <-ctx.Done():
			return PlacementBinding{}, ctx.Err()
		}
	}
	return p.binding(req.BindingID), nil
}

func (p *concurrentCreatePlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	return p.binding(req.BindingID), nil
}

func TestInvariant_concurrent_initial_create_converges_on_deterministic_binding(t *testing.T) {
	provider := &concurrentCreatePlacementProvider{}
	store := memstore.New()
	newService := func() *Service {
		svc, err := NewService(Config{
			Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
			OwnershipEnforced: true, NewID: func() session.SessionID { return "fixed" }, Now: func() time.Time { return time.Unix(1, 0) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	a, b := newService(), newService()
	provider.mu.Lock()
	provider.gate = make(chan struct{})
	provider.mu.Unlock()
	base := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "subject"})
	ctx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	type createResult struct {
		created *session.Session
		err     error
	}
	results := make(chan createResult, 2)
	for _, svc := range []*Service{a, b} {
		go func() {
			created, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed"))
			results <- createResult{created: created, err: err}
		}()
	}
	var first *session.Session
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("concurrent create: %v", result.err)
			}
			if first == nil {
				first = result.created
			} else if result.created.EnvironmentRef != first.EnvironmentRef || result.created.ID != first.ID {
				t.Fatalf("concurrent creates diverged: first=%+v second=%+v", first.EnvironmentRef, result.created.EnvironmentRef)
			}
		case <-ctx.Done():
			t.Fatalf("concurrent create did not finish: %v", ctx.Err())
		}
	}
	if first.EnvironmentRef.Revision != "rev-fixed" {
		t.Fatalf("create ref=%+v, want deterministic binding", first.EnvironmentRef)
	}
	if _, err := a.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{MaxTurns: 1}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("changed limits retry error=%v, want invalid argument", err)
	}
	if _, err := b.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileNoFS, WithSessionID("fixed")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("changed profile retry error=%v, want invalid argument", err)
	}
}

func TestInvariant_create_session_rejects_only_legacy_unknown_placement_fields(t *testing.T) {
	provider := &revisionPlacementProvider{}
	next := 0
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
		NewID: func() session.SessionID { next++; return session.SessionID(string(rune('a' + next))) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHarnessServer(svc)
	for _, field := range []protowire.Number{1, 8} {
		req := &mecatlv1.CreateSessionRequest{}
		req.ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, field, protowire.BytesType), "legacy-path"))
		if _, err := h.CreateSession(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("legacy unknown field %d = %v, want InvalidArgument", field, err)
		}
	}
	req := &mecatlv1.CreateSessionRequest{}
	req.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
	if _, err := h.CreateSession(context.Background(), req); err != nil {
		t.Fatalf("forward-compatible unknown field rejected: %v", err)
	}
}

func TestInvariant_shared_create_collision_publishes_matching_pending_reference(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: "pending"}
	owner := &session.Principal{Issuer: "issuer", Subject: "subject"}
	store := memstore.New()
	service, err := NewService(Config{
		Engine: repairEngine(), Store: store, OwnershipEnforced: true, PlacementScope: "test", SharedEngineRoot: "/same-root",
		PlacementProvider: &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	winner := session.New("fixed", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	winner.Owner = owner
	winner.Title = "durable winner"
	if err := store.Create(t.Context(), winner); err != nil {
		t.Fatal(err)
	}
	request := newCreateRequest(ref, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, "", createSessionOpts{})

	t.Run("matching winner commits", func(t *testing.T) {
		candidate := session.New("fixed", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
		candidate.Owner = owner
		commits, closes := 0, 0
		persisted, err := service.persistPlacedCreatedSession(t.Context(), candidate, owner, &request, &PlacementBinding{
			Ref: ref, Commit: func(context.Context) error { commits++; return nil }, Close: func() error { closes++; return nil },
		})
		if err != nil || persisted == nil || persisted.Title != winner.Title || commits != 1 || closes != 0 {
			t.Fatalf("persisted=%+v err=%v commits=%d closes=%d, want durable winner, successful publication, and no abort", persisted, err, commits, closes)
		}
	})

	t.Run("mismatched owner or request does not publish or adopt", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			owner   *session.Principal
			request createRequest
			want    error
		}{
			{name: "owner", owner: &session.Principal{Issuer: "other", Subject: "subject"}, request: request, want: ErrNotFound},
			{name: "request", owner: owner, request: newCreateRequest(ref, session.ModePlan, session.Limits{}, ProviderSelector{}, ProfileDefault, "", createSessionOpts{}), want: ErrInvalidArgument},
		} {
			t.Run(tc.name, func(t *testing.T) {
				candidate := session.New("fixed", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
				commits := 0
				persisted, err := service.persistPlacedCreatedSession(t.Context(), candidate, tc.owner, &tc.request, &PlacementBinding{Ref: ref, Commit: func(context.Context) error { commits++; return nil }})
				if !errors.Is(err, tc.want) || persisted != nil || commits != 0 {
					t.Fatalf("persisted=%p err=%v commits=%d, want %v with no adoption or publication", persisted, err, commits, tc.want)
				}
			})
		}
	})

	t.Run("commit failure retains pending reference and returns winner", func(t *testing.T) {
		candidate := session.New("fixed", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
		candidate.Owner = owner
		commits, closes := 0, 0
		persisted, err := service.persistPlacedCreatedSession(t.Context(), candidate, owner, &request, &PlacementBinding{
			Ref:    ref,
			Commit: func(context.Context) error { commits++; return errors.New("unavailable") },
			Close:  func() error { closes++; return nil },
		})
		if !errors.Is(err, ErrInternal) || persisted == nil || persisted.Title != winner.Title || commits != 1 || closes != 0 {
			t.Fatalf("persisted=%+v err=%v commits=%d closes=%d, want durable winner, internal error, and retained pending reference", persisted, err, commits, closes)
		}
	})
}

func TestInvariant_filesystem_placement_root_selects_policy_engine_for_every_kind(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "custom-fs", ID: "opaque", Revision: "r1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/root-b"), memledger.New(), nil), GovernanceRoot: "/root-b"}}
	factoryCalls := 0
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/root-a",
		SessionEngine: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, workspace string, _ session.PermissionMode) (SessionEngineResult, error) {
			factoryCalls++
			if workspace != "/root-b" {
				t.Fatalf("factory workspace = %q, want verified placement root", workspace)
			}
			return SessionEngineResult{Engine: repairEngine()}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
}

func TestInvariant_environment_override_reauthorizes_nonowning_provider(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "custom-fs", ID: "opaque", Revision: "r1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/fresh"), memledger.New(), nil), GovernanceRoot: "/fresh"}}
	store := memstore.New()
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/fresh"})
	if err != nil {
		t.Fatal(err)
	}
	baseline := provider.reattaches
	for range 2 {
		created, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		svc.SetSessionEnvironment(created.ID, tool.MustEnvironment(ref, memfs.NewWorkspace("/fresh"), memledger.New(), nil))
		run, err := svc.StartRun(context.Background(), created.ID, "go")
		if err != nil {
			t.Fatal(err)
		}
		for range run.Events() {
		}
	}
	if got := provider.reattaches - baseline; got != 2 {
		t.Fatalf("run entry reattached non-owning placement %d times, want 2", got)
	}
}

func TestInvariant_worktree_capability_comes_only_from_placement_provider(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/root", Revision: "r1"}
	without := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/root"), memledger.New(), nil)}}
	svc, err := NewService(Config{Engine: repairEngine(), Store: memstore.New(), PlacementProvider: without, PlacementScope: "test", SharedEngineRoot: "/root"})
	if err != nil {
		t.Fatal(err)
	}
	if svc.capabilities().Worktrees {
		t.Fatal("provider without PlacementDiscoverer advertised worktrees")
	}
	with := &revisionPlacementProvider{}
	svc, err = NewService(Config{Engine: repairEngine(), Store: memstore.New(), PlacementProvider: with, PlacementScope: "test", SharedEngineRoot: "/same-root"})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.capabilities().Worktrees {
		t.Fatal("provider PlacementDiscoverer did not advertise worktrees")
	}
}

func TestInvariant_placement_display_rejects_path_like_metadata(t *testing.T) {
	for _, value := range []string{"../secret", `..\\secret`, "safe/segment", `safe\\segment`, `C:relative`, "/absolute", `C:\\absolute`} {
		if got := sanitizePlacementDisplay(value, maxPlacementNameRunes); got != "" {
			t.Errorf("sanitizePlacementDisplay(%q) = %q, want empty", value, got)
		}
	}
	if got := sanitizePlacementDisplay("feature-name", maxPlacementNameRunes); got != "feature-name" {
		t.Fatalf("safe display = %q", got)
	}
}
