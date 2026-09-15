package server

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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

type revisionPlacementProvider struct{ binds int }

func (*revisionPlacementProvider) binding(revision string) PlacementBinding {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/same-root", Revision: revision}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil), CompositionRoot: "/same-root"}
}

func (p *revisionPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	p.binds++
	return p.binding(string(rune('0' + p.binds))), nil
}

func (*revisionPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/same-root"), memledger.New(), nil), CompositionRoot: "/same-root"}, nil
}

func (*revisionPlacementProvider) ListWorktrees(context.Context, PlacementDiscoveryRequest) ([]ScopedWorktree, error) {
	return nil, nil
}

func TestInvariant_create_retry_matches_complete_environment_ref(t *testing.T) {
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
	if _, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithSessionID("fixed")); err == nil {
		t.Fatal("retry with a different placement revision matched the existing session")
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

func TestInvariant_filesystem_placement_root_selects_policy_engine_for_every_kind(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "custom-fs", ID: "opaque", Revision: "r1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/root-b"), memledger.New(), nil), CompositionRoot: "/root-b"}}
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
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/fresh"), memledger.New(), nil), CompositionRoot: "/fresh"}}
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
