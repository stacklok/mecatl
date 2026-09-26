package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// TestADR_0352_Scenario7_UserJourney pins AC7.1 from the Jev router plan: the
// compact fallback cue, expanded card, and all three F6 family details preserve
// actual-model authority and optional-number presence from real client events.
func TestADR_0352_Scenario7_UserJourney(t *testing.T) {
	confidence, threshold := 0.42, 0.50
	decision := &client.RoutingDecision{
		Backend:           "jev",
		ClassifierModel:   "jev-1.13.0",
		CandidateCategory: "medium",
		CandidateModel:    "gpt-5.6-terra",
		Confidence:        &confidence,
		MinimumConfidence: &threshold,
		Outcome:           "fallback",
		ConsecutiveMisses: 2,
		MissLimit:         3,
	}

	zero := 0.0
	accepted := &client.RoutingDecision{
		Backend:           "jev",
		ClassifierModel:   "jev-1.13.0",
		CandidateCategory: "small",
		CandidateModel:    "gpt-5.6-mini",
		Confidence:        &zero,
		MinimumConfidence: &zero,
		Outcome:           "routed",
		MissLimit:         3,
	}

	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		client.ToolCallMsg{ID: "sub-call", Name: "Subagent", Args: `{"prompt":"inspect routing"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub-call", ChildID: "sub-child", Goal: "inspect routing", Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision},
		client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub-call", ChildID: "sub-child", InnerKind: "tool.call", ToolName: "Read", ToolCount: 1},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "sub-call", ChildID: "sub-child", Stop: "end_turn"},
		client.ToolCallMsg{ID: "accepted-call", Name: "Subagent", Args: `{"prompt":"implement routing"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "accepted-call", ChildID: "accepted-child", Goal: "implement routing", Model: "gpt-6-astra", RoutingDecision: accepted},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "accepted-call", ChildID: "accepted-child", Stop: "end_turn"},
		client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: "parallel-call", Join: "all", BranchCount: 1},
		client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: "parallel-call", BranchIndex: 0, ChildID: "parallel-child", BranchLabel: "branch-1", Goal: "inspect routing", Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision},
		client.ParallelMsg{Kind: client.ParallelBranchTool, ParentCallID: "parallel-call", BranchIndex: 0, InnerKind: "tool.call", ToolName: "Grep", ToolCount: 1},
		client.ParallelMsg{Kind: client.ParallelBranchEnd, ParentCallID: "parallel-call", BranchIndex: 0, ChildID: "parallel-child", ToolCount: 1, Stop: "end_turn"},
		client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "parallel-call", Join: "all", BranchCount: 1, Winner: -1},
		client.ToolCallMsg{ID: "team-call", Name: "Team", Args: `{}`},
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team-call", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "lead", Lead: true, Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision}}},
		client.TeamMsg{Kind: client.TeamMember, ParentCallID: "team-call", TeamID: "team-1", Member: "lead", InnerKind: "tool.call", ToolName: "Read"},
		client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "team-call", TeamID: "team-1", Rounds: 1, Stop: "end_turn"},
	)

	// The event owns an immutable decision snapshot. Later caller mutation must not
	// alter any card, roster, or focus view.
	decision.CandidateModel = "mutated-candidate"

	subBlock, ok := m.conv.scrollback.SnapshotForCall("sub-call")
	if !ok {
		t.Fatal("Subagent card was not created")
	}
	r := newTestRenderer()
	compact := stripANSIstr(r.renderSnapshot(0, subBlock, false))
	for _, want := range []string{
		"model: gpt-6-astra · fallback: low-confidence",
		"candidate: medium → gpt-5.6-terra · confidence 0.42 < threshold 0.50",
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact fallback card missing %q:\n%s", want, compact)
		}
	}
	if strings.Contains(compact, "model: gpt-5.6-terra") || strings.Contains(compact, "mutated-candidate") {
		t.Errorf("compact card replaced the actual model or retained a mutable decision pointer:\n%s", compact)
	}

	expanded := stripANSIstr(r.renderSnapshot(0, subBlock, true))
	assertRoutingDetail(t, "expanded completed Subagent card", expanded)
	if got := strings.Count(expanded, "candidate: medium"); got != 1 {
		t.Errorf("expanded fallback duplicated compact candidate cue %d times:\n%s", got, expanded)
	}
	acceptedBlock, ok := m.conv.scrollback.SnapshotForCall("accepted-call")
	if !ok {
		t.Fatal("accepted Subagent card was not created")
	}
	acceptedExpanded := stripANSIstr(r.renderSnapshot(0, acceptedBlock, true))
	assertAcceptedRoutingDetail(t, "expanded accepted Subagent card", acceptedExpanded)
	teamBlock, ok := m.conv.scrollback.SnapshotForCall("team-call")
	if !ok {
		t.Fatal("Team card was not created")
	}
	teamPayload := teamBlock.Payload.(scrollback.TeamCardSnapshot)
	teamPresentation := teamCardPresentationFromSnapshot(teamPayload)
	completedTeamExpanded := stripANSIstr(r.renderSnapshot(0, teamBlock, true))
	assertRoutingDetail(t, "expanded completed Team card", completedTeamExpanded)
	for _, tc := range []struct {
		name, want string
		stopped    bool
		retries    int
	}{
		{name: "done", want: "done"},
		{name: "stopped", want: "stopped — cancelled", stopped: true},
		{name: "retried", want: "done (retried)", retries: 1},
	} {
		t.Run("completed Team lane "+tc.name, func(t *testing.T) {
			terminal := teamPresentation
			terminal.lanes = append([]teamLane(nil), teamPresentation.lanes...)
			terminal.lanes[0].stopped = tc.stopped
			terminal.lanes[0].stopReason = teamStopReasonCancelled
			terminal.lanes[0].errorRounds = tc.retries
			view := stripANSIstr(r.renderTeamPresentation(terminal, true, 100))
			if !strings.Contains(view, "lead [lead] · "+tc.want+" ·") || strings.Contains(view, "Read…") {
				t.Errorf("completed Team lane did not show %q:\n%s", tc.want, view)
			}
			assertRoutingDetail(t, "completed Team lane "+tc.name, view)
		})
	}

	// Historic team.end events carried no per-member routing decision. Their compact
	// terminal summary remains exactly the pre-router one-line fallback.
	historicalTeam := teamCardPresentation{done: true, rounds: 1, stop: "end_turn"}
	if got, want := stripANSIstr(r.renderTeamPresentation(historicalTeam, false, 0)), "team · 1 round · ↑0 ↓0 · stop:done"; got != want {
		t.Errorf("historical compact completed Team = %q, want %q", got, want)
	}

	views := map[string]string{}
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 70})
	m = mm.(Model)
	conversationView := stripANSIstr(m.View().Content)
	if got, want := len(m.conv.testBlocks()), 3; got != want {
		t.Fatalf("Parallel lifecycle created an inline conversation block: got %d blocks, want %d", got, want)
	}
	if _, ok := m.conv.scrollback.SnapshotForCall("parallel-call"); ok {
		t.Fatal("Parallel lifecycle created a new inline routing card")
	}
	if !strings.Contains(conversationView, "model: gpt-6-astra") {
		t.Errorf("conversation View omitted the accepted Subagent actual model:\n%s", conversationView)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	m.agentsTab = tabSubagents
	m.subagents = subagentState{view: subagentFocus, child: "sub-child"}
	views["Subagent focus"] = stripANSIstr(m.View().Content)
	m.subagents = subagentState{view: subagentFocus, child: "accepted-child"}
	views["accepted Subagent focus"] = stripANSIstr(m.View().Content)

	m.agentsTab = tabParallel
	m.parallel = parallelState{view: parallelGroupView, group: "parallel-call"}
	views["Parallel focus"] = stripANSIstr(m.View().Content)

	m.agentsTab = tabTeams
	m.team = teamState{view: teamFocus, member: "lead"}
	views["Team focus"] = stripANSIstr(m.View().Content)

	assertAcceptedRoutingDetail(t, "accepted Subagent focus", views["accepted Subagent focus"])
	for _, name := range []string{"Subagent focus", "Parallel focus", "Team focus"} {
		t.Run(name, func(t *testing.T) { assertRoutingDetail(t, name, views[name]) })
	}
	var parallelGroup *parallelGroup
	for i := range m.conv.parallelGroups {
		if m.conv.parallelGroups[i].parentCallID == "parallel-call" {
			parallelGroup = &m.conv.parallelGroups[i]
			break
		}
	}
	if parallelGroup == nil || len(parallelGroup.branches) != 1 || !parallelGroup.done || !parallelGroup.branches[0].done {
		t.Fatalf("completed Parallel lifecycle was not retained: %#v", parallelGroup)
	}
	compactParallel := parallelBranchDetails(&parallelGroup.branches[0])
	for _, want := range []string{
		"model: gpt-6-astra · fallback: low-confidence",
		"candidate: medium → gpt-5.6-terra · confidence 0.42 < threshold 0.50",
		"done · 1 tool",
	} {
		if !strings.Contains(compactParallel, want) {
			t.Errorf("compact Parallel branch summary missing %q:\n%s", want, compactParallel)
		}
	}
	parallelFocus := views["Parallel focus"]
	if strings.Contains(parallelFocus, "Grep…") || !strings.Contains(parallelFocus, "done · 1 tool") {
		t.Errorf("terminal Parallel branch did not replace its active Grep state:\n%s", parallelFocus)
	}
	if !strings.Contains(parallelFocus, "Grep") {
		t.Errorf("terminal Parallel branch lost its historical tool trace:\n%s", parallelFocus)
	}

	// Width zero must remain renderable, and a narrow terminal must retain the
	// existing physical-width guarantee while showing the new bounded metadata.
	for _, width := range []int{0, 36} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			mm, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			out := stripANSIstr(mm.(Model).View().Content)
			if strings.TrimSpace(out) == "" {
				t.Fatal("routing detail view rendered blank")
			}
			if !strings.Contains(out, "backend: jev") {
				t.Errorf("routing detail disappeared at width %d:\n%s", width, out)
			}
			cardRenderer := newTestRenderer()
			cardRenderer.width = width
			completedTeam := stripANSIstr(cardRenderer.renderSnapshot(0, teamBlock, true))
			if strings.TrimSpace(completedTeam) == "" || !strings.Contains(completedTeam, "backend: jev") {
				t.Errorf("completed Team expanded routing detail disappeared at width %d:\n%s", width, completedTeam)
			}
			if width > 0 {
				for row, line := range strings.Split(out, "\n") {
					if got := maxLineWidth(line); got > width {
						t.Errorf("row %d width = %d, want <= %d: %q", row, got, width, line)
					}
				}
			}
		})
	}

	// Optional presence is visible: known zero is a real value, while nil LLM
	// confidence is unavailable. Historical nil retains the exact old label.
	zeroDecision := &client.RoutingDecision{Backend: "jev", ClassifierModel: "jev-1.13.0", Confidence: &zero, MinimumConfidence: &zero, Outcome: "routed", MissLimit: 3}
	zeroDetail := routingDecisionDetail(zeroDecision, "gpt-6-astra", "")
	if !strings.Contains(zeroDetail, "confidence: 0.00") || !strings.Contains(zeroDetail, "threshold: disabled (0.00)") || !strings.Contains(zeroDetail, "reason: accepted") || strings.Contains(zeroDetail, "no final reason") {
		t.Errorf("known zero and healthy accepted outcome must remain truthful: %q", zeroDetail)
	}
	llmDetail := routingDecisionDetail(&client.RoutingDecision{Backend: "llm", ClassifierModel: "quick", Outcome: "fallback", MissLimit: 3}, "gpt-6-astra", "bad-verdict")
	if !strings.Contains(llmDetail, "confidence: unavailable") || strings.Contains(llmDetail, "confidence: 0.00") {
		t.Errorf("nil LLM confidence must render unavailable, never zero: %q", llmDetail)
	}
	openDetail := routingDecisionDetail(&client.RoutingDecision{Backend: "jev", ClassifierModel: "jev-1.13.0", Outcome: "skipped", ConsecutiveMisses: 3, MissLimit: 3, BreakerOpen: true}, "gpt-6-astra", "breaker-open")
	for _, want := range []string{"actual model: gpt-6-astra · reason: breaker-open", "breaker: 3/3 misses · open"} {
		if !strings.Contains(openDetail, want) {
			t.Errorf("configured breaker skip missing %q: %q", want, openDetail)
		}
	}
	const historical = "model: gpt-6-astra · not routed: pinned-model"
	if got := delegationModelLabel("", "", "pinned-model", "gpt-6-astra", nil); got != historical {
		t.Errorf("historical nil label = %q, want byte-identical %q", got, historical)
	}
	hostileDetail := routingDecisionDetail(&client.RoutingDecision{
		Backend: "jev\u202e", ClassifierModel: "jev\u200b", CandidateCategory: "deep\u2066", CandidateModel: "model\u202d", Outcome: "fallback",
	}, "actual\u202e", "low-confidence\u200b")
	for _, marker := range []rune{'\u202e', '\u200b', '\u2066', '\u202d'} {
		if strings.ContainsRune(hostileDetail, marker) {
			t.Errorf("routing detail retained Unicode format control %U: %q", marker, hostileDetail)
		}
	}

	compareGolden(t, "jev_routing_user_journey.golden", []byte(expanded+"\n\n"+views["Subagent focus"]+"\n"))
}

func assertAcceptedRoutingDetail(t *testing.T, surface, out string) {
	t.Helper()
	for _, want := range []string{
		"backend: jev · classifier: jev-1.13.0 · outcome: routed",
		"candidate: small → gpt-5.6-mini · confidence: 0.00 · threshold: disabled (0.00)",
		"actual model: gpt-6-astra · reason: accepted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing %q:\n%s", surface, want, out)
		}
	}
}

func assertRoutingDetail(t *testing.T, surface, out string) {
	t.Helper()
	for _, want := range []string{
		"backend: jev · classifier: jev-1.13.0 · outcome: fallback",
		"candidate: medium → gpt-5.6-terra · confidence: 0.42 · threshold: 0.50",
		"actual model: gpt-6-astra · reason: low-confidence",
		"breaker: 2/3 misses · closed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing %q:\n%s", surface, want, out)
		}
	}
	if strings.Contains(out, "model: gpt-5.6-terra") || strings.Contains(out, "mutated-candidate") {
		t.Errorf("%s confused rejected candidate with actual model or kept mutable metadata:\n%s", surface, out)
	}
}
