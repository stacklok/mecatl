package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Exercise the production scoped factory and resolver, including the source-only
// execution lease that outlives an engine borrow until its binding is retired.
func TestAgentRootPublicationBindingLifecycle(t *testing.T) {
	for _, operation := range []string{"create", "fork", "clear"} {
		for _, failure := range []string{"construction", "configuration", "publication", "commit", "success"} {
			if operation == "create" && (failure == "configuration" || failure == "commit") {
				continue // Successor-only failure boundaries.
			}
			t.Run(operation+"/"+failure, func(t *testing.T) {
				ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "owner"})
				placement := harnessExecutionBinding(t, "files", "instructions", "command")
				fail := false
				placement.Commit = func(context.Context) error {
					if fail && failure == "commit" {
						return errors.New("reference publication failed")
					}
					return nil
				}
				provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{placement.Ref: placement}}
				store := &failingHarnessPublicationStore{Store: memstore.New()}
				cfg := isolateConfig(t, Config{Workspace: t.TempDir(), Model: "test-model", NoSoul: true, NoUserModel: true})
				defs := &resolvedAgentSource{defs: []tool.AgentDef{{Name: "root", Body: "ROOT-LIFECYCLE", Tools: []string{"Read"}}}}
				cfg.harnessAgentDefs = defs
				cfg.harnessSkills = &resolvedSkillSource{}
				cfg.harnessInstructions = hcAssembler("SCOPED-INSTRUCTIONS")
				binds, closes := map[session.SessionID]int{}, map[session.SessionID]int{}
				workspaces := map[session.SessionID]tool.Workspace{}
				resolver, err := newHarnessCommandResolver(ctx, harnessKindPolicy{sources: []HarnessSourceID{"repository"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{{
					ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, UsesExecutionWorkspace: true,
					Bind: func(ctx context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
						ws, release, err := scope.AcquireExecutionWorkspace(ctx)
						if err != nil {
							return nil, nil, err
						}
						binds[scope.SessionID]++
						workspaces[scope.SessionID] = ws
						return executionCommands{source: ws}, func() error { closes[scope.SessionID]++; return release() }, nil
					},
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer resolver.Close()
				cfg.harnessResolver = resolver
				resolver.sourceConfig = &cfg
				llm := mockllm.New(mockllm.TextTurn("done"))
				reg := regForTest(llm, providerMock, cfg.Model)
				engine, _, _, _, ordinary, _, _, assets, _, cleanup, err := buildEngine(ctx, cfg, reg, llm, store, store, resolveAgentRegistry(ctx, cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				factory := func(ctx context.Context, id session.SessionID, owner *session.Principal, acquire server.ExecutionWorkspaceAcquirer, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode, tools []tool.Tool, toolKeys []string, forkSource session.SessionID) (server.SessionEngineResult, error) {
					res, err := assets.sessionContextFactory(ctx, id, owner, acquire, sel, specs, profile, workspace, mode, tools, toolKeys, forkSource)
					if err == nil && fail && failure == "configuration" {
						res.BuiltForMode = session.ModeAccept
					}
					return res, err
				}
				svc, err := server.NewService(server.Config{Engine: engine, Store: store, Commands: resolver, PlacementProvider: provider, PlacementScope: "test", OwnershipEnforced: true,
					SessionEngine: ordinary, AgentDefSessionEngine: assets.agentDefSessionEngine, SessionContextEngine: factory, OnCloseSession: resolver.Retire,
					NewID: func() session.SessionID { return "attempt" },
				})
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				create := func(id session.SessionID) (*session.Session, error) {
					return svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithAgentDefinitionName("root"))
				}
				source, err := create("source")
				if err != nil {
					t.Fatal(err)
				}
				fail = true
				store.fail.Store(failure == "publication")
				if failure == "construction" {
					cfg.harnessAgentDefs = &resolvedAgentSource{}
				}
				var id session.SessionID
				switch operation {
				case "create":
					var created *session.Session
					created, err = create("attempt")
					if created != nil {
						id = created.ID
					}
				case "fork":
					id, err = svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID})
				case "clear":
					id, err = svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
				}
				switch failure {
				case "commit":
					if !errors.Is(err, server.ErrInternal) || operation == "fork" && id != "attempt" || closes["attempt"] != 0 {
						t.Fatalf("persisted commit failure: id=%s err=%v closes=%v", id, err, closes)
					}
					if _, err := store.Load(ctx, "attempt"); err != nil {
						t.Fatal(err)
					}
					_, release, err := resolver.Borrow(ctx, "attempt", source.Owner, source.Profile)
					if err != nil {
						t.Fatalf("persisted binding retired: %v", err)
					}
					release()
				case "success":
					if err != nil || id != "attempt" || closes["attempt"] != 0 {
						t.Fatalf("successful publication: id=%s err=%v closes=%v", id, err, closes)
					}
				default:
					if err == nil || id != "" || binds["attempt"] != 1 || closes["attempt"] != 1 || provider.sourceCloses.Load() != 1 {
						t.Fatalf("failed publication: id=%s err=%v binds=%v closes=%v lease closes=%d", id, err, binds, closes, provider.sourceCloses.Load())
					}
					if _, err := store.Load(ctx, "attempt"); !errors.Is(err, port.ErrSessionNotFound) {
						t.Fatalf("failed session persisted: %v", err)
					}
					if _, err := workspaces["attempt"].Read(ctx, "command.md"); err == nil {
						t.Fatal("failed destination lease remains live")
					}
					if _, _, err := resolver.Borrow(ctx, "attempt", source.Owner, source.Profile); !errors.Is(err, server.ErrCommandBindingRetired) {
						t.Fatalf("failed destination not retired: %v", err)
					}
				}
				if closes["source"] != 0 {
					t.Fatal("destination cleanup retired source")
				}
				if body, err := workspaces["source"].Read(ctx, "command.md"); err != nil || string(body) != "command" {
					t.Fatalf("source lease changed: %q %v", body, err)
				}
				unchanged, err := store.Load(ctx, source.ID)
				if err != nil || !reflect.DeepEqual(unchanged.Authority, source.Authority) || unchanged.Mode != source.Mode || unchanged.Limits != source.Limits {
					t.Fatalf("source configuration changed: %+v %v", unchanged, err)
				}
				if operation == "create" && failure != "success" {
					fail = false
					store.fail.Store(false)
					cfg.harnessAgentDefs = defs
					if _, err := create("attempt"); err != nil {
						t.Fatalf("identical explicit-ID retry: %v", err)
					}
					if binds["attempt"] != 2 || closes["attempt"] != 1 {
						t.Fatalf("retry bindings=%v closes=%v", binds, closes)
					}
				}
				svc.CloseSession("attempt")
				svc.CloseSession("source")
				// A failed reference commit deliberately retains its placement for
				// reconciliation; source-only leases still close with their bindings.
				wantCloses := len(provider.reattaches)
				if failure == "commit" {
					wantCloses--
				}
				if !reflect.DeepEqual(binds, closes) || int(provider.sourceCloses.Load()) != wantCloses {
					t.Fatalf("unbalanced cleanup: binds=%v closes=%v lease closes=%d", binds, closes, provider.sourceCloses.Load())
				}
			})
		}
	}
}
