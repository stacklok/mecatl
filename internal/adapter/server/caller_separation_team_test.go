package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestCallerSeparation_Scenario5_LiveTeamOperationsAreOwnerChecked pins the ADR 0212
// live-team boundaries: lookup, run, and cleanup are all absence-shaped for a foreign
// caller, while the owner retains each operation.
func TestCallerSeparation_Scenario5_LiveTeamOperationsAreOwnerChecked(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(), Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	svc, err := server.NewService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:             memstore.New(),
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(0, 0) },
		MemberEngine:      memberEngine,
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := callerCtx("alice")
	bob := callerCtx("bob")

	teamID, _, err := svc.CreateTeam(alice, "/ws", "alice-team", "goal", 0, nil)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	if _, _, _, err := svc.ListTeam(bob, teamID); !errors.Is(err, server.ErrTeamNotFound) {
		t.Fatalf("Bob ListTeam = %v, want ErrTeamNotFound", err)
	}
	if _, err := svc.RunTeam(bob, teamID, nil); !errors.Is(err, server.ErrTeamNotFound) {
		t.Fatalf("Bob RunTeam = %v, want ErrTeamNotFound", err)
	}
	if err := svc.CleanupTeam(bob, teamID); !errors.Is(err, server.ErrTeamNotFound) {
		t.Fatalf("Bob CleanupTeam = %v, want ErrTeamNotFound", err)
	}
	if _, _, _, err := svc.ListTeam(alice, teamID); err != nil {
		t.Fatalf("team survived Bob's denied lookup and cleanup, but Alice cannot list it: %v", err)
	}
	if _, err := svc.RunTeam(alice, teamID, nil); err != nil {
		t.Fatalf("Alice RunTeam: %v", err)
	}
	if err := svc.CleanupTeam(alice, teamID); err != nil {
		t.Fatalf("Alice CleanupTeam: %v", err)
	}
}

func callerCtx(sub string) context.Context {
	return session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: "https://issuer.example", Subject: sub, GrantType: session.GrantTypeUser,
	})
}
