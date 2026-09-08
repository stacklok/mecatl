package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type delegationPlacementProvider struct{ binding server.PlacementBinding }

func (p delegationPlacementProvider) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	return p.binding, nil
}
func (p delegationPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref != p.binding.Ref {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	return p.binding, nil
}

func TestInvariant_delegation_cannot_escalate_placement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := memstore.New()
	ref := session.EnvironmentRef{Kind: "remote", ID: "opaque-placement", Revision: "revision-9"}
	privateRoot := "/private/provider/root"
	env := tool.MustEnvironment(ref, memfs.NewWorkspace(privateRoot), memledger.New(), nil)
	provider := delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}}
	memberEngine := func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})}
	}
	workspaceCalls := 0
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}), Store: store,
		PlacementProvider: provider, PlacementScope: "test-scope", MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := session.New("source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: privateRoot, Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	source.EnvironmentRef = ref
	if err := store.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	teamID, _, err := svc.CreateTeamForSession(ctx, source.ID, "team", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.Load(ctx, agent.MemberSessionID(teamID, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if member.EnvironmentRef != ref {
		t.Fatalf("team member ref = %+v, want owning session exact ref %+v", member.EnvironmentRef, ref)
	}
	if workspaceCalls != 0 {
		t.Fatalf("source-owned team reconstructed placement from a root %d time(s)", workspaceCalls)
	}

	noFS := session.New("no-fs", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0))
	noFS.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	if err := store.Create(ctx, noFS); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateTeamForSession(ctx, noFS.ID, "forbidden", "", 0, nil); err == nil {
		t.Fatal("no-FS source upgraded into a filesystem team")
	}
	if workspaceCalls != 0 {
		t.Fatalf("no-FS team attempt constructed a workspace %d time(s)", workspaceCalls)
	}
}
