package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestADR_0350_Scenario7_UserJourney pins AC7.1 from the Jev router plan: the
// compact fallback cue, expanded card, and all three F6 family details preserve
// actual-model authority and optional-number presence from real client events.
func TestADR_0350_Scenario7_UserJourney(t *testing.T) {
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

	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		client.ToolCallMsg{ID: "sub-call", Name: "Subagent", Args: `{"prompt":"inspect routing"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub-call", ChildID: "sub-child", Goal: "inspect routing", Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision},
		client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub-call", ChildID: "sub-child", InnerKind: "tool.call", ToolName: "Read", ToolCount: 1},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "sub-call", ChildID: "sub-child", Stop: "end_turn"},
		client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: "parallel-call", Join: "all", BranchCount: 1},
		client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: "parallel-call", BranchIndex: 0, ChildID: "parallel-child", BranchLabel: "branch-1", Goal: "inspect routing", Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision},
		client.ParallelMsg{Kind: client.ParallelBranchTool, ParentCallID: "parallel-call", BranchIndex: 0, InnerKind: "tool.call", ToolName: "Grep", ToolCount: 1},
		client.ToolCallMsg{ID: "team-call", Name: "Team", Args: `{}`},
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team-call", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "lead", Lead: true, Model: "gpt-6-astra", RoutingReason: "low-confidence", RoutingDecision: decision}}},
		client.TeamMsg{Kind: client.TeamMember, ParentCallID: "team-call", TeamID: "team-1", Member: "lead", InnerKind: "tool.call", ToolName: "Read"},
	)

	// The event owns an immutable decision snapshot. Later caller mutation must not
	// alter any card, roster, or focus view.
	decision.CandidateModel = "mutated-candidate"

	var subBlock *block
	for i := range m.conv.blocks {
		if m.conv.blocks[i].toolID == "sub-call" {
			subBlock = &m.conv.blocks[i]
			break
		}
	}
	if subBlock == nil {
		t.Fatal("Subagent tool block was not created")
	}
	r := newTestRenderer()
	compact := stripANSIstr(r.renderBlock(0, subBlock, false))
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

	expanded := stripANSIstr(r.renderBlock(0, subBlock, true))
	assertRoutingDetail(t, "expanded completed Subagent card", expanded)
	if got := strings.Count(expanded, "candidate: medium"); got != 1 {
		t.Errorf("expanded fallback duplicated compact candidate cue %d times:\n%s", got, expanded)
	}
	var teamBlock *block
	for i := range m.conv.blocks {
		if m.conv.blocks[i].toolID == "team-call" {
			teamBlock = &m.conv.blocks[i]
			break
		}
	}
	if teamBlock == nil {
		t.Fatal("Team tool block was not created")
	}
	assertRoutingDetail(t, "expanded Team card", stripANSIstr(r.renderBlock(0, teamBlock, true)))

	views := map[string]string{}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	m = mm.(Model)
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 70})
	m = mm.(Model)
	m.agentsTab = tabSubagents
	m.subagents = subagentState{view: subagentFocus, child: "sub-child"}
	views["Subagent focus"] = stripANSIstr(m.View().Content)

	m.agentsTab = tabParallel
	m.parallel = parallelState{view: parallelGroupView, group: "parallel-call"}
	views["Parallel focus"] = stripANSIstr(m.View().Content)

	m.agentsTab = tabTeams
	m.team = teamState{view: teamFocus, member: "lead"}
	views["Team focus"] = stripANSIstr(m.View().Content)

	for name, out := range views {
		t.Run(name, func(t *testing.T) { assertRoutingDetail(t, name, out) })
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
	zero := 0.0
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
