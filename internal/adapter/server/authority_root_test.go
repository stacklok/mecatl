package server_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestCreateTeamStampsComposedRootAuthority(t *testing.T) {
	store := memstore.New()
	root := session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true},
		Provenance:    "composed_root",
	}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		catalog := tool.NewCatalog()
		for _, candidate := range agent.MemberTools(tm, spec.Name, nil) {
			catalog.MustRegister(candidate)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("round complete"), mockllm.TextTurn("report")), Catalog: catalog, Policy: allow, Model: "mock",
		})}
	}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}),
		Store:  store,

		MemberEngine:  memberEngine,
		RootAuthority: func(session.SessionKind) session.Authority { return root },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(context.Background(), "test", "complete work", 0, []agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "work"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := svc.RunTeam(context.Background(), teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	member, err := store.Load(context.Background(), agent.MemberSessionID(teamID, "lead"))
	if err != nil {
		t.Fatalf("Load member: %v", err)
	}
	got, bound := member.BoundAuthority()
	if !bound || !got.CapabilitySet.Contains(root.CapabilitySet) || !root.CapabilitySet.Contains(got.CapabilitySet) {
		t.Fatalf("member authority = %+v bound=%t, want composed root %+v", got, bound, root)
	}
}
