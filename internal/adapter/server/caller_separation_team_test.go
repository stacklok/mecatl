package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
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
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:  memstore.New(),

		Now:               func() time.Time { return time.Unix(0, 0) },
		MemberEngine:      memberEngine,
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	alice := callerCtx("alice")
	bob := callerCtx("bob")

	teamID, _, err := svc.CreateTeamOnDefaultPlacement(alice, "alice-team", "goal", 0, nil)
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

// ownedTeamService is teamServiceWithStore's ownership-enforcing sibling: the
// gRPC CreateTeam path with a verifier wired.
func ownedTeamService(t *testing.T, llm *mockllm.Provider) (*server.Service, port.SessionStore) {
	return ownedTeamServiceWithCap(t, llm, 0)
}

func ownedTeamServiceWithCap(t *testing.T, llm *mockllm.Provider, maxTeams int) (*server.Service, port.SessionStore) {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := memstore.New()
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
		}),
		Store: store,

		Now:               func() time.Time { return time.Unix(0, 0) },
		MemberEngine:      memberEngine,
		OwnershipEnforced: true,
		MaxTeams:          maxTeams,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

// TestCallerSeparation_GRPCTeamMembersAreOwnerStamped pins that the gRPC team
// path attributes its durable member sessions to the creating caller.
//
// Service.CreateTeam deliberately builds its supervisor with ZERO parent caps —
// no ask surfacing, no child-ask adjudicator — and owner attribution rode along
// in that same struct, so inheritOwner no-opped and AddMember published a
// snapshot with Owner == nil. Reads then fail closed for EVERYONE including the
// creator, and because this branch teaches every retention path to skip
// ownerless rows, such records can never be GC'd, cleaned or settled: any
// authenticated caller could mint unreapable storage in a loop, and
// StorageHealth.Ownerless — the operator's pre-cutover drain gate — would grow
// under normal use.
func TestCallerSeparation_GRPCTeamMembersAreOwnerStamped(t *testing.T) {
	svc, store := ownedTeamService(t, mockllm.New(mockllm.TextTurn("x")))
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), alice)

	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "t", "goal", 0,
		[]agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "work"}})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	memberID := agent.MemberSessionID(teamID, "lead")
	sess, err := store.Load(context.Background(), memberID)
	if err != nil {
		t.Fatalf("load member %q: %v", memberID, err)
	}
	if sess.Owner == nil {
		t.Fatal("durable member session published OWNERLESS: unreadable by its own team owner and skipped by every retention path")
	}
	if !sess.Owner.SameIdentity(alice) {
		t.Fatalf("member owner = %+v, want the creating caller alice", sess.Owner)
	}

	// The owner must be able to read it back through the ordinary authorized path.
	if _, err := svc.GetSession(ctx, memberID); err != nil {
		t.Fatalf("team owner cannot read its own member: %v", err)
	}
}

// TestCreateTeamRefusedAtCapacityLeavesNothingBehind pins that a full registry
// refuses BEFORE any lease or durable write.
//
// Enrolment acquires each member's cross-process lease and publishes a durable
// member snapshot. Checking MaxTeams afterwards meant a capacity refusal stranded
// both: leases no peer replica could take until expiry, and member records that —
// once owner-stamped — count against the ownerless/retention picture forever. A
// caller could drive that in a loop simply by creating teams against a full
// registry.
func TestCreateTeamRefusedAtCapacityLeavesNothingBehind(t *testing.T) {
	svc, store := ownedTeamServiceWithCap(t, mockllm.New(mockllm.TextTurn("x")), 1)
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(context.Background(), alice)
	roster := []agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "work"}}

	// Fill the single slot.
	if _, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "first", "goal", 0, roster); err != nil {
		t.Fatalf("first CreateTeam: %v", err)
	}

	// The second must be refused, and must not have written anything.
	before := storedSessionIDs(t, store)
	id, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "second", "goal", 0, roster)
	if !errors.Is(err, server.ErrTooManyTeams) {
		t.Fatalf("second CreateTeam = (%q, %v), want ErrTooManyTeams", id, err)
	}
	after := storedSessionIDs(t, store)
	if len(after) != len(before) {
		t.Fatalf("a refused CreateTeam published %d durable session(s): before=%v after=%v",
			len(after)-len(before), before, after)
	}
}

func storedSessionIDs(t *testing.T, store port.SessionStore) []session.SessionID {
	t.Helper()
	lister, ok := store.(port.PrunableStore)
	if !ok {
		t.Skip("store does not enumerate sessions")
	}
	rows, err := lister.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make([]session.SessionID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
