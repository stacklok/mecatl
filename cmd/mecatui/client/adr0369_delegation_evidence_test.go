package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestDelegationSelectionProjection(t *testing.T) {
	subagent := subagentMsg(SubagentStart, &mecatlv1.Subagent{
		Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	})
	if subagent.Provider != "anthropic" || subagent.Model != "claude-opus-4-1" || subagent.ExplicitRouterCategory != "deep" {
		t.Fatalf("subagent selection = %#v", subagent)
	}

	parallel := parallelMsg(ParallelBranchStart, &mecatlv1.Parallel{
		Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	})
	if parallel.Provider != "anthropic" || parallel.Model != "claude-opus-4-1" || parallel.ExplicitRouterCategory != "deep" {
		t.Fatalf("parallel selection = %#v", parallel)
	}

	team := teamMsg(TeamStart, &mecatlv1.Team{Roster: []*mecatlv1.TeamMemberSpec{{
		Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	}}})
	if len(team.Roster) != 1 || team.Roster[0].Provider != "anthropic" || team.Roster[0].Model != "claude-opus-4-1" || team.Roster[0].ExplicitRouterCategory != "deep" {
		t.Fatalf("team selection = %#v", team.Roster)
	}
}
