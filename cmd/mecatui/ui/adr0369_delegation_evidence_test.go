package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestDelegationEvidenceRendering(t *testing.T) {
	const want = "selected: model-router/deep → anthropic/claude-opus-4-1"

	var c conversation
	c.addTool("sub", "Subagent", `{"prompt":"private task"}`)
	applySubagentTo(&c, client.SubagentMsg{
		Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "bounded goal",
		Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	})
	sub, ok := c.subagentCard("sub")
	if !ok {
		t.Fatal("subagent start was not retained in scrollback")
	}
	subRendered := stripANSIstr(newTestRenderer().renderSubagentPresentation(subagentCardPresentationFromSnapshot(sub), 120))
	if !strings.Contains(subRendered, want) {
		t.Fatalf("subagent rendering = %q, want %q", subRendered, want)
	}

	applyParallelTo(&c, client.ParallelMsg{
		Kind: client.ParallelBranchStart, ParentCallID: "parallel", BranchIndex: 0,
		Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	})
	branch := c.parallelGroups[0].branches[0]
	parallelRendered := parallelBranchDetails(&branch)
	if !strings.Contains(parallelRendered, want) {
		t.Fatalf("parallel rendering = %q, want %q", parallelRendered, want)
	}

	c.addTool("team", "Team", `{}`)
	applyTeamTo(&c, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", Roster: []client.TeamMemberSpec{{
		Name: "reviewer", Provider: "anthropic", Model: "claude-opus-4-1", ExplicitRouterCategory: "deep",
	}}})
	team, ok := c.teamCard("team")
	if !ok || len(team.Update.Lanes) != 1 {
		t.Fatalf("team roster was not retained in scrollback: ok=%v lanes=%d", ok, len(team.Update.Lanes))
	}
	teamPresentation := teamCardPresentationFromSnapshot(team)
	teamRendered := teamRosterRuntime(&teamPresentation.lanes[0])
	if !strings.Contains(teamRendered, want) {
		t.Fatalf("team rendering = %q, want %q", teamRendered, want)
	}

	for _, got := range []string{subRendered, parallelRendered, teamRendered} {
		for _, forbidden := range []string{"private task", "operator-secret-alias", "credential-shaped-config-secret", "routed:"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("explicit selection label leaked %q: %q", forbidden, got)
			}
		}
	}

	if got := delegationModelLabelWithSelection("deep", "claude-opus-4-1", "", "claude-opus-4-1", "anthropic", "", &client.RoutingDecision{Outcome: "routed"}); got != "routed: deep → anthropic/claude-opus-4-1" {
		t.Fatalf("classifier label regressed: %q", got)
	}
	if got := delegationModelLabelWithSelection("small", "gpt-6-luna", "", "gpt-6-luna", "sprout-openai-api", "", nil); got != "routed: small → sprout-openai-api/gpt-6-luna" {
		t.Fatalf("configured scalar route label = %q", got)
	}
	c.addTool("routed-sub", "Subagent", `{}`)
	applySubagentTo(&c, client.SubagentMsg{
		Kind: client.SubagentStart, ParentCallID: "routed-sub", ChildID: "routed-child", Goal: "smoke test",
		Provider: "sprout-openai-api", Model: "gpt-6-luna", RoutedCategory: "small", RoutedModel: "gpt-6-luna",
		RoutingDecision: &client.RoutingDecision{Outcome: "routed"},
	})
	routed, ok := c.subagentCard("routed-sub")
	if !ok {
		t.Fatal("routed subagent card missing")
	}
	routedPresentation := subagentCardPresentationFromSnapshot(routed)
	if got := stripANSIstr(newTestRenderer().renderSubagentPresentation(routedPresentation, 120)); !strings.Contains(got, "routed: small → sprout-openai-api/gpt-6-luna") {
		t.Fatalf("routed subagent card omitted its actual provider/model: %q", got)
	}
	if got := routingDecisionDetail(routedPresentation.routing, qualifiedModelLabel(routedPresentation.provider, routedPresentation.model), routedPresentation.routingReason); !strings.Contains(got, "actual model: sprout-openai-api/gpt-6-luna") {
		t.Fatalf("delegation focus omitted its actual provider/model: %q", got)
	}
	if got := delegationModelLabelWithSelection("", "", "resume", "gpt-6-luna", "sprout-openai-api", "", nil); got != "model: sprout-openai-api/gpt-6-luna · not routed: resume" {
		t.Fatalf("resumed child label = %q", got)
	}
	if got := delegationModelLabelWithSelection("", "", "", "gpt-6-luna", "", "", nil); got != "model: gpt-6-luna" {
		t.Fatalf("older server label = %q", got)
	}
}
