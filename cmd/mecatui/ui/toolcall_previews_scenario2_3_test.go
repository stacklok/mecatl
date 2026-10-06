package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func previewFixture(t *testing.T) Model {
	t.Helper()
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Subagent", `{"task":"investigate"}`)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child-A"})
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child-A", InnerKind: "tool.call", ToolName: "Read", Detail: "arguments"})
	return openToolcallsForTest(t, m)
}

func TestMecatuiToolcallPreviews_Scenario2_SubagentRowsAndDetail(t *testing.T) {
	m := previewFixture(t)
	s := toolcallsForTest(t, m)
	m.conv.subagentFleet = []subagentLane{{childID: "child-A", trace: []teamTrace{{kind: teamTraceTool, name: "UNRELATED", detail: "foreign"}}}}
	m.syncToolcalls()
	foreign, _ := s.Render(90, 24)
	if len(s.entries) != 2 || strings.Contains(stripANSIstr(foreign), "UNRELATED") {
		t.Fatalf("inspector borrowed F6 activity: %#v", s.entries)
	}
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "message.delta", Text: "message-not-a-tool"})
	m.syncToolcalls()
	if len(s.entries) != 2 {
		t.Fatalf("child message became tool row: %#v", s.entries)
	}
	if s.entries[0].fullName != "Subagent" {
		t.Fatalf("inventory: %#v", s.entries)
	}
	list, regions := s.Render(90, 24)
	if !strings.Contains(stripANSIstr(list), "preview") || !strings.Contains(stripANSIstr(list), "Read") || !strings.Contains(stripANSIstr(list), "parent parent") || !strings.Contains(stripANSIstr(list), "child child-A") {
		t.Fatalf("preview list: %q", list)
	}
	clicked := false
	for _, region := range regions {
		if s.previewHits[region.hit] == 1 {
			s.HandleMsg(surfaceHitMsg{ID: region.hit})
			clicked = true
			break
		}
	}
	if !clicked || s.selected != 1 || !s.detail {
		t.Fatalf("preview click did not open its own detail: clicked=%t selected=%d detail=%t", clicked, s.selected, s.detail)
	}
	s.refreshDetail(&m.conv.scrollback)
	detail := inspectorDetail(t, s, 90, 24)
	for _, want := range []string{"Read", "arguments", "child-A", "preview", "incomplete"} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q: %q", want, detail)
		}
	}
	for _, fabricated := range []string{"Arguments:", "Result:", "Call: parent", "✓ Read"} {
		if strings.Contains(detail, fabricated) {
			t.Errorf("preview claimed complete child detail %q: %q", fabricated, detail)
		}
	}
	s.selected = 0
	s.refreshDetail(&m.conv.scrollback)
	if parent := inspectorDetail(t, s, 90, 24); !strings.Contains(parent, "investigate") {
		t.Fatalf("parent arguments lost: %q", parent)
	}
}

func TestMecatuiToolcallPreviews_Scenario2_LiveSelectionAndEviction(t *testing.T) {
	// Drive the public reducer while the inspector is already open: live child
	// activity must refresh its derived rows before subagent.end.
	live := newToolcallsInspectorModel(t)
	live = applyAll(live, client.ToolCallMsg{ID: "live-parent", Name: "Subagent", Args: `{"task":"investigate"}`})
	live = openToolcallsForTest(t, live)
	liveInspector := toolcallsForTest(t, live)
	if len(liveInspector.entries) != 1 {
		t.Fatalf("open inspector entries: %#v", liveInspector.entries)
	}
	for _, msg := range []client.SubagentMsg{
		{Kind: client.SubagentStart, ParentCallID: "live-parent", ChildID: "live-child"},
		{Kind: client.SubagentTool, ParentCallID: "live-parent", ChildID: "live-child", InnerKind: "tool.call", ToolName: "Read", Detail: "live args"},
	} {
		updated, _ := live.Update(msg)
		live = updated.(Model)
	}
	liveInspector = toolcallsForTest(t, live)
	if len(liveInspector.entries) != 2 || liveInspector.entries[1].trace.Detail != "live args" || liveInspector.entries[1].trace.Resolved {
		t.Fatalf("live call did not add a pending preview: %#v", liveInspector.entries)
	}
	liveInspector.selected = 1
	updated, _ := live.Update(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "live-parent", ChildID: "live-child", InnerKind: "tool.result", ToolName: "Read", Detail: "live result"})
	live = updated.(Model)
	liveInspector = toolcallsForTest(t, live)
	if liveInspector.selected != 1 || !liveInspector.entries[1].trace.Resolved || liveInspector.entries[1].trace.Detail != "live result" {
		t.Fatalf("live result moved selection or did not update in place: %#v", liveInspector.entries)
	}

	m := previewFixture(t)
	s := toolcallsForTest(t, m)
	s.selected = 1
	s.listFollow = true
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "tool.result", ToolName: "Read", Detail: "result"})
	m.syncToolcalls()
	if s.selected != 1 || !s.entries[1].trace.Resolved || s.entries[1].trace.Detail != "result" {
		t.Fatalf("result moved selection or did not update preview: %#v", s.entries)
	}
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "tool.call", ToolName: "Write", Detail: "next"})
	m.syncToolcalls()
	if s.selected != 1 {
		t.Fatalf("append moved selected trace slot: %d", s.selected)
	}
	s.Render(70, 24)
	s.Render(90, 24)
	if s.selected != 1 {
		t.Fatalf("reflow moved selected trace slot: %d", s.selected)
	}
	_, staleRegions := s.Render(90, 24)
	var stalePreview HitID
	for _, region := range staleRegions {
		if _, ok := s.previewHits[region.hit]; ok {
			stalePreview = region.hit
			break
		}
	}
	if stalePreview == 0 {
		t.Fatal("preview has no hit target")
	}
	for i := 0; i < maxTraceEntries; i++ {
		m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "tool.call", ToolName: "Write"})
	}
	m.syncToolcalls()
	if s.entries[s.selected].fullName != "Subagent" {
		t.Fatalf("eviction selected another preview: %#v", s.entries[s.selected])
	}
	// A hit belongs to the rendered frame, not the list index that happens to
	// occupy its old slot after an eviction.
	s.HandleMsg(surfaceHitMsg{ID: stalePreview})
	if s.selected != 0 || s.detail {
		t.Fatalf("stale preview hit opened a replacement row: selected=%d detail=%t entries=%#v", s.selected, s.detail, s.entries)
	}
	_, freshRegions := s.Render(90, 24)
	var freshPreview HitID
	for _, region := range freshRegions {
		if s.previewHits[region.hit] == 1 {
			freshPreview = region.hit
			break
		}
	}
	if freshPreview == 0 {
		t.Fatal("current preview has no hit target")
	}
	s.HandleMsg(surfaceHitMsg{ID: freshPreview})
	if s.selected != 1 || !s.detail || s.entries[s.selected].fullName != "Write" {
		t.Fatalf("current preview hit did not open its intended row: selected=%d detail=%t entry=%#v", s.selected, s.detail, s.entries[s.selected])
	}
	s.detail = false
	s.Render(90, 24)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.detail {
		t.Fatal("parent no longer selectable")
	}
	pending := previewFixture(t)
	pendingInspector := toolcallsForTest(t, pending)
	pending.conv.resolveTool("parent", "parent failed", true)
	pending.syncToolcalls()
	if glyph, _ := tracePreviewStatus(pendingInspector.entries[1].trace); glyph != "…" {
		t.Fatalf("parent result fabricated child success: %q", glyph)
	}
}

func TestMecatuiToolcallPreviews_Scenario2_ReloadAndSessionIsolation(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context(), Resume: &client.ResumeSelection{Row: client.SessionListItem{ID: "loaded"}, Transcript: client.SessionTranscript{Messages: []client.ConversationMessage{{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "parent", Name: "Subagent", Args: `{"task":"loaded"}`}}}}}}})
	m.width, m.height = 90, 24
	m.relayout()
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected = 0
	s.detail = true
	s.refreshDetail(&m.conv.scrollback)
	if got := inspectorDetail(t, s, 90, 24); !strings.Contains(got, "activity preview unavailable; history may be incomplete") {
		t.Fatalf("reload availability: %q", got)
	}
	if len(s.entries) != 1 {
		t.Fatalf("borrowed previews: %#v", s.entries)
	}
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child-replayed"})
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "tool.call", ToolName: "Grep", Detail: "recent-only"})
	m.syncToolcalls()
	if len(s.entries) != 2 || s.entries[1].trace.Detail != "recent-only" {
		t.Fatalf("recent projection not shown: %#v", s.entries)
	}
	s.selected = 0
	s.refreshDetail(&m.conv.scrollback)
	if got := inspectorDetail(t, s, 90, 24); !strings.Contains(got, "history may be incomplete") || strings.Contains(got, "activity preview unavailable") {
		t.Fatalf("replayed activity caveat: %q", got)
	}
	replacement := newToolcallsInspectorModel(t)
	replacement.conv.addTool("parent", "Subagent", `{"task":"other session"}`)
	replacement = openToolcallsForTest(t, replacement)
	if other := toolcallsForTest(t, replacement); len(other.entries) != 1 || other.entries[0].intent != "other session" {
		t.Fatalf("overlapping call ID borrowed previous session: %#v", other.entries)
	}
}

func TestMecatuiToolcallPreviews_Scenario3_DelegationFamilyParityAndSafety(t *testing.T) {
	trace := traceAppendTool(nil, "Read", "args")
	r := (&renderer{th: testTheme(), traceWidth: 50}).renderTrace(trace)
	if strings.Contains(stripANSIstr(r), "✓ Read") || !strings.Contains(stripANSIstr(r), "Read") {
		t.Fatalf("call-only should be neutral: %q", r)
	}
	trace = traceMarkToolResult(trace, "Read", "result", false)
	if !strings.Contains(stripANSIstr((&renderer{th: testTheme(), traceWidth: 50}).renderTrace(trace)), "✓ Read — result") {
		t.Fatalf("observed result: %#v", trace)
	}
	failed := traceMarkToolResult(traceAppendTool(nil, "Grep", "pattern"), "Grep", "not found", true)
	if got := stripANSIstr((&renderer{th: testTheme(), traceWidth: 50}).renderTrace(failed)); !strings.Contains(got, "✗ Grep — not found") {
		t.Fatalf("observed error not rendered: %q", got)
	}
	if orphan := traceMarkToolResult(nil, "Read", "orphan", false); !orphan[0].unattributed || orphan[0].resolved {
		t.Fatalf("result-only fabricated call: %#v", orphan)
	}
	trace = traceAppendTool(trace, "Read", "second")
	trace = traceMarkToolResult(trace, "Read", "ambiguous", true)
	if trace[0].detail != "result" || trace[1].detail != "second" || !strings.Contains(stripANSIstr((&renderer{th: testTheme(), traceWidth: 50}).renderTrace(trace)), "unattributed") {
		t.Fatalf("ambiguous result changed a call: %#v", trace)
	}
	trace = traceMarkToolResult(trace, "", "nameless", true)
	if !strings.Contains(stripANSIstr((&renderer{th: testTheme(), traceWidth: 50}).renderTrace(trace)), "unattributed") {
		t.Fatalf("nameless result lost: %#v", trace)
	}
	mixed := traceAppendTool(nil, "Read", "pending")
	mixed = traceAppendTool(mixed, "Read", "previously resolved")
	mixed[1].resolved = true
	mixed = traceMarkToolResult(mixed, "Read", "ambiguous", false)
	if mixed[0].resolved || !mixed[len(mixed)-1].unattributed {
		t.Fatalf("resolved duplicate allowed a guessed match: %#v", mixed)
	}
	blocked := traceAppendTool(nil, "Glob", "first")
	for i := 0; i < maxTraceEntries; i++ {
		blocked = traceAppendTool(blocked, "Read", "later")
	}
	blocked = traceMarkToolResult(blocked, "Read", "must-not-attach", false)
	if blocked[len(blocked)-1].name != "unattributed result (Read)" || blocked[len(blocked)-2].resolved {
		t.Fatalf("unresolved eviction guessed a result: %#v", blocked)
	}
	// Exercise each real delegation reducer into its owning UI surface.
	subagent := newMCPModel(t, aztec(), nil)
	subagent = seedSubagents(subagent, "sub-parent",
		startSub("sub-parent", "sub-child", "inspect"),
		toolSubPreview("sub-parent", "sub-child", "tool.call", "Read", "sub args", 1),
	)
	if card := subagent.conv.testSubagentCard("sub-parent"); card == nil || len(card.trace) != 1 || card.trace[0].detail != "sub args" || card.trace[0].resolved {
		t.Fatalf("Subagent event did not produce pending inline trace: %#v", card)
	}
	subagentModel, _ := subagent.Update(toolSubPreview("sub-parent", "sub-child", "tool.result", "Read", "sub result", 1))
	subagent = subagentModel.(Model)
	if card := subagent.conv.testSubagentCard("sub-parent"); card == nil || !card.trace[0].resolved || card.trace[0].detail != "sub result" {
		t.Fatalf("Subagent event did not update its inline trace in place: %#v", card)
	}
	parallel := newMCPModel(t, aztec(), nil)
	parallel = seedParallel(parallel, "parallel-parent",
		startPar("parallel-parent", "all", 1),
		branchStartPar("parallel-parent", 0, "branch", "inspect"),
		branchToolParPreview("parallel-parent", 0, "tool.call", "Read", "parallel args", 1),
	)
	parallelModel, _ := parallel.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	parallel = parallelModel.(Model)
	parallelModel, _ = parallel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	parallel = parallelModel.(Model)
	if view := stripANSIstr(parallel.View().Content); !strings.Contains(view, "Read") || !strings.Contains(view, "parallel args") {
		t.Fatalf("Parallel F6 omitted live pending trace: %q", view)
	}
	team := newMCPModel(t, aztec(), nil)
	team.conv.addTool("team-parent", "Team", `{}`)
	for _, msg := range []client.TeamMsg{
		{Kind: client.TeamStart, ParentCallID: "team-parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "member"}}},
		{Kind: client.TeamMember, ParentCallID: "team-parent", TeamID: "team", Member: "member", InnerKind: "tool.call", ToolName: "Read", Detail: "team args"},
	} {
		teamModel, _ := team.Update(msg)
		team = teamModel.(Model)
	}
	teamModel, _ := team.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	team = teamModel.(Model)
	teamModel, _ = team.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	team = teamModel.(Model)
	if view := stripANSIstr(team.View().Content); !strings.Contains(view, "Read") || !strings.Contains(view, "team args") {
		t.Fatalf("Team F6 omitted live pending trace: %q", view)
	}

	m := previewFixture(t)
	s := toolcallsForTest(t, m)
	const hostile = "unsafe\x1b[2J"
	long := hostile + strings.Repeat("z", 200)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", InnerKind: "tool.result", ToolName: "Read", Detail: long})
	m.syncToolcalls()
	if !s.entries[1].trace.Resolved {
		t.Fatalf("observed result missing: %#v", s.entries[1])
	}
	for _, width := range []int{50, 90} {
		list, _ := s.Render(width, 20)
		inline := (&renderer{th: testTheme(), traceWidth: width}).renderTrace(traceFromScroll(s.entries[1].prefix))
		for surface, body := range map[string]string{"list": list, "inline": inline} {
			if strings.Contains(body, "\x1b[2J") {
				t.Errorf("%s emitted hostile control", surface)
			}
			for _, row := range strings.Split(stripANSIstr(body), "\n") {
				if len([]rune(row)) > width {
					t.Errorf("%s overflow at %d: %q", surface, width, row)
				}
			}
		}
		if !strings.Contains(stripANSIstr(list), "unsafe[2J") || !strings.Contains(stripANSIstr(inline), "unsafe[2J") {
			t.Errorf("latest preview differs across views: list=%q inline=%q", list, inline)
		}
	}
}

func TestMecatuiToolcallPreviews_Scenario3_OwnerBoundaries(t *testing.T) {
	m := previewFixture(t)
	m.subagents = subagentState{view: subagentFocus, child: "child-A"}
	m.parallel = parallelState{view: parallelGroupView, group: "parallel"}
	m.conv.addTool("parallel", "Parallel", "{}")
	m.conv.addTool("team", "Team", "{}")
	m.syncToolcalls()
	s := toolcallsForTest(t, m)
	if len(s.entries) != 4 {
		t.Fatalf("inspector should add only Subagent preview: %#v", s.entries)
	}
	if _, ok := m.conv.scrollback.SnapshotAt(s.entries[0].index).Payload.(scrollback.SubagentCardSnapshot); !ok {
		t.Fatal("preview not parented to scrollback")
	}
	if m.subagents.view != subagentFocus || m.subagents.child != "child-A" || m.parallel.view != parallelGroupView || m.parallel.group != "parallel" {
		t.Fatalf("inspector moved F6 selection: sub=%#v parallel=%#v", m.subagents, m.parallel)
	}
	approval := newToolcallsInspectorModel(t)
	approval.phase = phaseAwaitingApproval
	got, _ := approval.runToolcalls()
	if got.(Model).modal != nil {
		t.Fatal("inspector stole approval surface")
	}
}
