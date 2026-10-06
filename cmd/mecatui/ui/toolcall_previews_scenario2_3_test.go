package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestDelegationTraceRejectsMalformedIDsPerLane(t *testing.T) {
	valid := strings.Repeat("é", 128)
	trace := traceAppendTool(nil, valid, "Read", "a", "a")
	trace = traceAppendTool(trace, valid, "Read", "b", "b")
	for _, id := range []string{"bad\xff", strings.Repeat("x", 257)} {
		trace = traceAppendTool(trace, id, "Read", "must not retain", "a")
		trace = traceMarkToolResult(trace, id, "Read", "must not resolve", true, "a")
	}
	if len(trace) != 2 || trace[0].id != valid || trace[1].id != valid || trace[0].resolved || trace[1].resolved {
		t.Fatalf("malformed ID retained: %+v", trace)
	}
	trace = traceMarkToolResult(trace, valid, "Read", "done", false, "b")
	if trace[0].resolved || !trace[1].resolved || trace[0].detail != "a" {
		t.Fatalf("lane correlation lost: %+v", trace)
	}
}

func TestDelegationTraceRetentionAndPairing(t *testing.T) {
	var trace []teamTrace
	for i := 0; i < 129; i++ {
		id := fmt.Sprint(i)
		trace = traceAppendTool(trace, id, "Read", id)
		trace = traceAppendMessage(trace, "message")
	}
	calls, messages := 0, 0
	for _, row := range trace {
		if row.kind == teamTraceTool {
			calls++
		} else {
			messages++
		}
	}
	if calls != 128 || messages != 12 || trace[0].id == "0" {
		t.Fatalf("retention: calls=%d messages=%d first=%+v", calls, messages, trace[0])
	}
	before := len(trace)
	trace = traceMarkToolResult(trace, "0", "Read", "evicted", false)
	trace = traceMarkToolResult(trace, "missing", "Read", "orphan", false)
	trace = traceMarkToolResult(trace, "", "Read", "legacy", false)
	if len(trace) != before {
		t.Fatal("result fabricated a row")
	}
	trace = traceMarkToolResult(trace, "128", "Read", "resolved", true)
	for _, row := range trace {
		if row.kind != teamTraceTool {
			continue
		}
		if row.id == "128" {
			if !row.resolved || !row.isError || row.detail != "resolved" {
				t.Fatalf("wrong match: %+v", row)
			}
		} else if row.resolved {
			t.Fatalf("other call resolved: %+v", row)
		}
	}
	trace = traceMarkToolResult(trace, "128", "Read", "duplicate", false)
	if trace[len(trace)-2].detail == "duplicate" {
		t.Fatal("duplicate result changed status")
	}
}

func TestDelegationOutOfOrderAndDuplicateCalls(t *testing.T) {
	trace := traceMarkToolResult(nil, "id", "Read", "early", false)
	trace = traceAppendTool(trace, "id", "Read", "args")
	trace = traceAppendTool(trace, "id", "Read", "replayed args")
	if len(trace) != 1 || trace[0].resolved || trace[0].detail != "args" {
		t.Fatalf("out of order or replay fabricated a call: %+v", trace)
	}
	trace = traceMarkToolResult(trace, "id", "Read", "done", false)
	trace = traceAppendTool(trace, "id", "Read", "late replay")
	if len(trace) != 1 || !trace[0].resolved || trace[0].detail != "done" {
		t.Fatalf("late replay changed call: %+v", trace)
	}
}

func TestDelegationLegacyPairingAndLaneScoping(t *testing.T) {
	trace := traceAppendTool(nil, "", "Read", "legacy")
	trace = traceMarkToolResult(trace, "", "Read", "legacy result", false)
	trace = traceAppendTool(trace, "id", "Read", "identified")
	if !trace[0].resolved || trace[1].resolved {
		t.Fatalf("legacy matched ID call: %+v", trace)
	}
	trace = traceMarkToolResult(trace, "", "Read", "again", true)
	if trace[0].isError || trace[0].detail != "legacy result" {
		t.Fatalf("duplicate changed legacy: %+v", trace)
	}
	trace = traceMarkToolResult(trace, "id", "Read", "identified result", true)
	if !trace[1].resolved || !trace[1].isError {
		t.Fatalf("ID result: %+v", trace)
	}
	mixed := traceAppendTool(traceAppendTool(nil, "", "Read", "legacy"), "id", "Read", "identified")
	mixed = traceMarkToolResult(mixed, "", "Read", "uncertain", false)
	if mixed[0].resolved || mixed[1].resolved {
		t.Fatalf("mixed IDs guessed legacy: %+v", mixed)
	}
	ambiguous := traceAppendTool(traceAppendTool(nil, "", "Read", "a"), "", "Read", "b")
	ambiguous = traceMarkToolResult(ambiguous, "", "Read", "ambiguous", false)
	if len(ambiguous) != 2 || ambiguous[0].resolved || ambiguous[1].resolved {
		t.Fatalf("ambiguous legacy: %+v", ambiguous)
	}
	for i := 0; i < maxTraceEntries; i++ {
		ambiguous = traceAppendTool(ambiguous, fmt.Sprint(i), "Read", "next")
	}
	ambiguous = traceMarkToolResult(ambiguous, "", "Read", "late", false)
	if ambiguous[len(ambiguous)-1].resolved {
		t.Fatal("legacy result after eviction guessed status")
	}
	// Identical IDs in different child lanes cannot cross-contaminate.
	other := traceAppendTool(nil, "id", "Read", "other")
	if other[0].resolved || other[0].detail != "other" {
		t.Fatal("lane state leaked")
	}
}

func TestSubagentScrollbackRetentionWithInterleavedMessages(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Subagent", `{}`)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child"})
	for i := 0; i < 129; i++ {
		id := fmt.Sprint(i)
		m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: id, ToolName: "Read", Detail: id})
		m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "message.delta", Text: "message"})
	}
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "0", ToolName: "Read", Detail: "evicted"})
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "128", ToolName: "Read", Detail: "ok"})
	card, ok := m.conv.subagentCard("parent")
	if !ok || len(card.Update.Trace) != 140 {
		t.Fatalf("serialized trace length: %d", len(card.Update.Trace))
	}
	calls := 0
	for _, row := range card.Update.Trace {
		if row.Kind != toolKind {
			continue
		}
		calls++
		if row.ID == "0" || (row.Resolved != (row.ID == "128")) {
			t.Fatalf("evicted or wrongly matched: %+v", row)
		}
	}
	if calls != 128 {
		t.Fatalf("retained calls = %d", calls)
	}
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected, s.detail = 0, true
	s.refreshDetail(&m.conv.scrollback)
	if len(s.entries) != 1 || len(s.detailEntry.childTools) != 128 {
		t.Fatalf("parent detail retention: %+v", s.detailEntry)
	}
}

func TestSubagentSharedParentChildLaneRetentionAndPairing(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Subagent", `{}`)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "a"})
	send := func(child, kind, id, detail string) {
		m.conv.applySubagentTyped(client.SubagentMsg{
			Kind: client.SubagentTool, ParentCallID: "parent", ChildID: child,
			InnerKind: kind, ChildToolCallID: id, ToolName: "Read", Detail: detail, Text: detail,
		})
	}
	// A resumed child has its own tool-call ID namespace under the same parent.
	send("a", "tool.call", "shared", "a args")
	send("b", "tool.call", "shared", "b args")
	send("a", "tool.result", "shared", "a result")
	before, _ := m.conv.subagentCard("parent")
	if len(before.Update.Trace) != 2 || !before.Update.Trace[0].Resolved || before.Update.Trace[0].Detail != "a result" || before.Update.Trace[1].Resolved || before.Update.Trace[1].Detail != "b args" {
		t.Fatalf("shared ID crossed child lanes: %+v", before.Update.Trace)
	}
	for i := 0; i < 128; i++ {
		id := fmt.Sprintf("a-%d", i)
		send("a", "tool.call", id, id)
		send("a", "message.delta", "", id)
		if i < 127 {
			id = fmt.Sprintf("b-%d", i)
			send("b", "tool.call", id, id)
			send("b", "message.delta", "", id)
		}
	}
	send("b", "tool.result", "shared", "b result") // same ID as a, but b's call is retained
	send("a", "tool.result", "shared", "evicted")
	send("b", "tool.result", "b-0", "b retained")
	send("a", "tool.result", "a-0", "a retained")
	card, ok := m.conv.subagentCard("parent")
	if !ok {
		t.Fatal("missing parent card")
	}
	calls := map[string]int{}
	messages := map[string]int{}
	for _, row := range card.Update.Trace {
		switch row.Kind {
		case toolKind:
			calls[row.Lane]++
			switch row.Lane + "/" + row.ID {
			case "a/shared":
				t.Fatalf("evicted call retained: %+v", row)
			case "b/shared":
				if !row.Resolved || row.Detail != "b result" {
					t.Fatalf("wrong shared ID match: %+v", row)
				}
			case "b/b-0":
				if !row.Resolved || row.Detail != "b retained" {
					t.Fatalf("wrong b match: %+v", row)
				}
			case "a/a-0":
				if !row.Resolved || row.Detail != "a retained" {
					t.Fatalf("wrong a match: %+v", row)
				}
			default:
				if row.Resolved {
					t.Fatalf("cross-lane result: %+v", row)
				}
			}
		case "message":
			messages[row.Lane]++
		}
	}
	if calls["a"] != 128 || calls["b"] != 128 || messages["a"] != 12 || messages["b"] != 12 {
		t.Fatalf("per-lane retention: calls=%v messages=%v", calls, messages)
	}
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected, s.detail = 0, true
	s.refreshDetail(&m.conv.scrollback)
	if len(s.entries) != 1 || len(s.detailEntry.childTools) != 256 {
		t.Fatalf("parent detail lost child lanes: entries=%d tools=%d", len(s.entries), len(s.detailEntry.childTools))
	}
	for _, row := range s.detailEntry.childTools {
		if row.Lane == "b" && row.ID == "shared" && (!row.Resolved || row.Detail != "b result") {
			t.Fatalf("parent detail has wrong result: %+v", row)
		}
	}
}

func TestSubagentSharedParentLegacyBlockIsPerChild(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Subagent", `{}`)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "a"})
	send := func(child, kind, id, detail string) {
		m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: child, InnerKind: kind, ChildToolCallID: id, ToolName: "Read", Detail: detail})
	}
	send("a", "tool.call", "", "old legacy")
	send("b", "tool.call", "", "b legacy")
	for i := 0; i < 128; i++ {
		send("a", "tool.call", fmt.Sprint(i), "a call")
	}
	send("a", "tool.call", "", "new legacy")
	send("b", "tool.result", "", "b result")
	send("a", "tool.result", "", "must not guess")
	card, _ := m.conv.subagentCard("parent")
	for _, row := range card.Update.Trace {
		if row.Lane == "b" && (!row.Resolved || row.Detail != "b result") {
			t.Fatalf("other lane blocked legacy: %+v", row)
		}
		if row.Lane == "a" && row.ID == "" && row.Resolved {
			t.Fatalf("evicted legacy matched new call: %+v", row)
		}
	}
}

func TestToolcallsParentDetailLiveChildSummary(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = applyAll(m, client.ToolCallMsg{ID: "parent", Name: "Subagent", Args: `{"task":"investigate"}`})
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.detail = true
	s.selected = 0
	for _, msg := range []client.SubagentMsg{
		{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child"},
		{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "one", ToolName: "Read", Detail: "args"},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	s = toolcallsForTest(t, m)
	if len(s.entries) != 1 || !strings.Contains(inspectorDetail(t, s, 90, 24), "… Read · pending — args") {
		t.Fatalf("live detail not updated: %+v", s.detailEntry)
	}
	updated, _ := m.Update(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "one", ToolName: "Read", Detail: "done", IsError: true})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected != 0 || len(s.entries) != 1 || !strings.Contains(inspectorDetail(t, s, 90, 24), "✗ Read · error — done") {
		t.Fatalf("result did not refresh parent: %+v", s.detailEntry)
	}
	updated, _ = m.Update(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "two", ToolName: "Grep", Detail: "next"})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if len(s.entries) != 1 || !strings.Contains(inspectorDetail(t, s, 90, 24), "… Grep · pending") {
		t.Fatal("child call entered list or detail did not refresh")
	}
	if m.subagents.view != subagentRoster {
		t.Fatal("inspector changed F6 state")
	}
	for i := 0; i < 30; i++ {
		updated, _ = m.Update(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: fmt.Sprint(i + 10), ToolName: "Read"})
		m = updated.(Model)
	}
	s = toolcallsForTest(t, m)
	s.Render(60, 12)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	s.Render(60, 12)
	before := s.window.Offset()
	updated, _ = m.Update(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "two", ToolName: "Grep", Detail: "live result"})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	s.Render(60, 12)
	if s.selected != 0 || s.window.Offset() != before || !strings.Contains(strings.Join(s.styledToolcallDetailLines(*s.detailEntry), "\n"), "live result") {
		t.Fatalf("live scroll/selection lost: selected=%d offset=%d want=%d", s.selected, s.window.Offset(), before)
	}
}

func TestDelegationReducerIDPairingAcrossFamilies(t *testing.T) {
	sub := newMCPModel(t, aztec(), nil)
	sub = seedSubagents(sub, "sub-parent", startSub("sub-parent", "child", "goal"))
	for _, event := range []client.SubagentMsg{
		{Kind: client.SubagentTool, ParentCallID: "sub-parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "first", ToolName: "Read", Detail: "first"},
		{Kind: client.SubagentTool, ParentCallID: "sub-parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "second", ToolName: "Read", Detail: "second"},
		{Kind: client.SubagentTool, ParentCallID: "sub-parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "first", ToolName: "Read", Detail: "done"},
	} {
		updated, _ := sub.Update(event)
		sub = updated.(Model)
	}
	if card := sub.conv.testSubagentCard("sub-parent"); card == nil || !card.trace[0].resolved || card.trace[1].resolved || card.trace[1].detail != "second" {
		t.Fatalf("subagent pairing: %+v", card)
	}
	par := newMCPModel(t, aztec(), nil)
	par = seedParallel(par, "parallel-parent", startPar("parallel-parent", "all", 1), branchStartPar("parallel-parent", 0, "branch", "goal"))
	for _, event := range []client.ParallelMsg{
		{Kind: client.ParallelBranchTool, ParentCallID: "parallel-parent", BranchIndex: 0, InnerKind: "tool.call", ChildToolCallID: "first", ToolName: "Read", Detail: "first"},
		{Kind: client.ParallelBranchTool, ParentCallID: "parallel-parent", BranchIndex: 0, InnerKind: "tool.call", ChildToolCallID: "second", ToolName: "Read", Detail: "second"},
		{Kind: client.ParallelBranchTool, ParentCallID: "parallel-parent", BranchIndex: 0, InnerKind: "tool.result", ChildToolCallID: "first", ToolName: "Read", Detail: "done"},
	} {
		updated, _ := par.Update(event)
		par = updated.(Model)
	}
	trace := par.conv.parallelGroups[0].branches[0].trace
	if len(trace) != 2 || !trace[0].resolved || trace[1].resolved {
		t.Fatalf("parallel pairing: %+v", trace)
	}
	team := newMCPModel(t, aztec(), nil)
	team.conv.addTool("team-parent", "Team", `{}`)
	updated, _ := team.Update(client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team-parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "a"}, {Name: "b"}}})
	team = updated.(Model)
	for _, event := range []client.TeamMsg{
		{Kind: client.TeamMember, ParentCallID: "team-parent", Member: "a", InnerKind: "tool.call", ChildToolCallID: "shared", ToolName: "Read"},
		{Kind: client.TeamMember, ParentCallID: "team-parent", Member: "b", InnerKind: "tool.call", ChildToolCallID: "shared", ToolName: "Read"},
		{Kind: client.TeamMember, ParentCallID: "team-parent", Member: "a", InnerKind: "tool.result", ChildToolCallID: "shared", ToolName: "Read"},
	} {
		updated, _ = team.Update(event)
		team = updated.(Model)
	}
	card, ok := team.conv.teamCard("team-parent")
	if !ok || !card.Update.Lanes[0].Trace[0].Resolved || card.Update.Lanes[1].Trace[0].Resolved {
		t.Fatalf("team lane scope: %+v", card)
	}
}

func TestParallelBranchPreviewRetentionAndPairingPerBranch(t *testing.T) {
	var c conversation
	send := func(branch int, kind, id, detail string) {
		c.parallelBranchTool(client.ParallelMsg{
			ParentCallID: "parent", BranchIndex: branch, InnerKind: kind,
			ChildToolCallID: id, ToolName: "Read", Detail: detail, Text: detail,
		})
	}
	for branch := 0; branch < 2; branch++ {
		send(branch, "tool.call", "shared", "shared args")
	}
	for i := 0; i < 127; i++ {
		for branch := 0; branch < 2; branch++ {
			id := fmt.Sprintf("%d-%d", branch, i)
			if i == 0 {
				id = "retained" // Same ID and name in both branches.
			}
			send(branch, "tool.call", id, id+" args")
			if i < 12 {
				send(branch, "message.delta", "", fmt.Sprintf("message %d", i))
			}
		}
	}
	send(0, "tool.call", "extra", "extra args") // 129th call in branch 0 only.
	send(0, "tool.result", "shared", "evicted result")
	send(0, "tool.result", "retained", "branch 0 result")
	send(1, "tool.result", "1-1", "branch 1 result")
	if len(c.parallelGroups) != 1 || len(c.parallelGroups[0].branches) != 2 {
		t.Fatalf("parallel groups: %+v", c.parallelGroups)
	}
	for branch, lane := range c.parallelGroups[0].branches {
		assertDelegationLanePreview(t, lane.trace, branch, "branch")
	}
}

func TestTeamMemberPreviewRetentionAndPairingPerMember(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Team", `{}`)
	m.conv.applyTeamTyped(client.TeamMsg{Kind: client.TeamStart, ParentCallID: "parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "a"}, {Name: "b"}}})
	send := func(member, kind, id, detail string) {
		m.conv.applyTeamTyped(client.TeamMsg{
			Kind: client.TeamMember, ParentCallID: "parent", Member: member,
			InnerKind: kind, ChildToolCallID: id, ToolName: "Read", Detail: detail, Text: detail,
		})
	}
	for _, member := range []string{"a", "b"} {
		send(member, "tool.call", "shared", "shared args")
	}
	for i := 0; i < 127; i++ {
		for branch, member := range []string{"a", "b"} {
			id := fmt.Sprintf("%d-%d", branch, i)
			if i == 0 {
				id = "retained" // Same ID and name in both members.
			}
			send(member, "tool.call", id, id+" args")
			if i < 12 {
				send(member, "message.delta", "", fmt.Sprintf("message %d", i))
			}
		}
	}
	send("a", "tool.call", "extra", "extra args") // 129th call in member a only.
	send("a", "tool.result", "shared", "evicted result")
	send("a", "tool.result", "retained", "branch 0 result")
	send("b", "tool.result", "1-1", "branch 1 result")
	card, ok := m.conv.teamCard("parent")
	if !ok || len(card.Update.Lanes) != 2 {
		t.Fatalf("team card: %+v", card)
	}
	for branch, lane := range card.Update.Lanes {
		assertDelegationLanePreview(t, traceFromScroll(lane.Trace), branch, lane.Name)
	}
}

func assertDelegationLanePreview(t *testing.T, trace []teamTrace, branch int, label string) {
	t.Helper()
	calls, messages := 0, 0
	sharedSeen, retainedSeen := false, false
	for _, row := range trace {
		switch row.kind {
		case teamTraceMessage:
			if want := fmt.Sprintf("message %d", messages); row.text != want {
				t.Fatalf("%s message %d = %q, want %q", label, messages, row.text, want)
			}
			messages++
		case teamTraceTool:
			calls++
			if branch == 0 && row.id == "shared" {
				t.Fatalf("%s retained evicted call: %+v", label, row)
			}
			wantDetail := row.id + " args"
			if row.id == "shared" {
				sharedSeen = true
				wantDetail = "shared args"
			}
			if row.id == "retained" {
				retainedSeen = true
			}
			if (branch == 0 && row.id == "retained") || (branch == 1 && row.id == "1-1") {
				wantDetail = fmt.Sprintf("branch %d result", branch)
			}
			resolvedID := "retained"
			if branch == 1 {
				resolvedID = "1-1"
			}
			if row.resolved != (row.id == resolvedID) || row.detail != wantDetail {
				t.Fatalf("%s wrong result or preview: %+v, want detail %q", label, row, wantDetail)
			}
		}
	}
	if calls != 128 || messages != 12 || !retainedSeen || sharedSeen != (branch == 1) {
		t.Fatalf("%s retained %d calls, %d messages, shared=%t, retained=%t; want 128, 12, %t, true", label, calls, messages, sharedSeen, retainedSeen, branch == 1)
	}
}

func TestDelegationIDLessResultRequiresToolName(t *testing.T) {
	for _, family := range []string{"Subagent", "Parallel", "Team"} {
		t.Run(family, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.conv.addTool("parent", family, `{}`)
			var send func(kind, name, detail string)
			var trace func() []teamTrace
			switch family {
			case "Subagent":
				m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child"})
				send = func(kind, name, detail string) {
					m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: kind, ToolName: name, Detail: detail})
				}
				trace = func() []teamTrace {
					card, _ := m.conv.subagentCard("parent")
					return traceFromScroll(card.Update.Trace)
				}
			case "Parallel":
				send = func(kind, name, detail string) {
					m.conv.parallelBranchTool(client.ParallelMsg{ParentCallID: "parent", BranchIndex: 0, InnerKind: kind, ToolName: name, Detail: detail})
				}
				trace = func() []teamTrace { return m.conv.parallelGroups[0].branches[0].trace }
			case "Team":
				m.conv.applyTeamTyped(client.TeamMsg{Kind: client.TeamStart, ParentCallID: "parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "member"}}})
				send = func(kind, name, detail string) {
					m.conv.applyTeamTyped(client.TeamMsg{Kind: client.TeamMember, ParentCallID: "parent", Member: "member", InnerKind: kind, ToolName: name, Detail: detail})
				}
				trace = func() []teamTrace {
					card, _ := m.conv.teamCard("parent")
					return traceFromScroll(card.Update.Lanes[0].Trace)
				}
			}
			send("tool.call", "Read", "args")
			for _, name := range []string{"", "Grep"} {
				send("tool.result", name, "must not match")
				if got := trace(); len(got) != 1 || got[0].resolved || got[0].detail != "args" {
					t.Fatalf("ID-less result without matching name %q correlated: %+v", name, got)
				}
			}
			send("tool.result", "Read", "matched")
			if got := trace(); len(got) != 1 || !got[0].resolved || got[0].detail != "matched" {
				t.Fatalf("unique named legacy result not paired: %+v", got)
			}
		})
	}
}

func TestChildSummarySanitizesBoundedPreview(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("parent", "Subagent", `{}`)
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", ChildID: "child"})
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "id", ToolName: "Read", Detail: "args"})
	m.conv.applySubagentTyped(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "id", ToolName: "Read", Detail: "unsafe\x1b[2J" + strings.Repeat("z", 300)})
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected, s.detail = 0, true
	s.refreshDetail(&m.conv.scrollback)
	rows := strings.Join(s.styledToolcallDetailLines(*s.detailEntry), "\n")
	if strings.Contains(rows, "\x1b[2J") || !strings.Contains(stripANSIstr(rows), "unsafe[2J") || len([]rune(s.detailEntry.childTools[0].Detail)) > maxTraceDetailLen {
		t.Fatalf("unsafe or unbounded summary: %q", rows)
	}
	for _, width := range []int{12, 20} {
		content := s.styledToolcallDetailLinesAtWidth(*s.detailEntry, width)
		for i, row := range toolcallDetailRows(*s.detailEntry) {
			if row.kind != toolcallChildSummary {
				continue
			}
			if got := toolcallRowCounts(content, width)[i]; got != 1 {
				t.Errorf("width %d child summary rows = %d, want one", width, got)
			}
			if got := ansi.StringWidth(content[i]); got > width {
				t.Errorf("width %d child summary width = %d", width, got)
			}
		}
		s.window, s.width, s.follow = new(bounded.Viewport), 0, false
		body := s.renderDetail(width, 24, "Tool calls", func(_ lipgloss.Style, text string) string { return text })
		if strings.Contains(body, "\x1b[2J") {
			t.Fatalf("width %d rendered unsafe child text: %q", width, body)
		}
	}
}

func TestChildSummaryResizePreservesAnchor(t *testing.T) {
	s := &toolcallsState{deps: surfaceDeps{theme: testTheme()}, detailEntry: &toolcallDetail{
		name: "Subagent", callID: "parent", historyCaveat: true,
		childTools:     []scrollback.TraceEntry{{Kind: toolKind, ToolName: "Read", Detail: strings.Repeat("preview ", 40)}},
		intent:         `{"task":"` + strings.Repeat("task ", 30) + `"}`,
		resultReceived: true, result: scrollback.ToolResult{Body: strings.Repeat("result\n", 20)},
	}}
	entry := *s.detailEntry
	wide, narrow := 60, 12
	wideContent := s.styledToolcallDetailLinesAtWidth(entry, wide)
	narrowContent := s.styledToolcallDetailLinesAtWidth(entry, narrow)
	child := -1
	wideStart, narrowStart := 0, 0
	for i, row := range toolcallDetailRows(entry) {
		if row.kind == toolcallChildSummary {
			child = i
			break
		}
		wideStart += toolcallRowCounts(wideContent, wide)[i]
		narrowStart += toolcallRowCounts(narrowContent, narrow)[i]
	}
	if child < 0 || toolcallRowCounts(wideContent, wide)[child] != 1 || toolcallRowCounts(narrowContent, narrow)[child] != 1 {
		t.Fatal("child summary did not remain a single physical row")
	}
	line := func(_ lipgloss.Style, text string) string { return text }
	s.renderDetail(wide, 5, "Tool calls", line)
	s.follow = false
	s.window.SetOffset(wideStart, len(toolcallRowCounts(wideContent, wide)))
	s.recordAnchor()
	s.renderDetail(narrow, 5, "Tool calls", line)
	if got := s.window.Offset(); got != narrowStart {
		t.Fatalf("resize offset = %d, want child summary at %d", got, narrowStart)
	}
}

func TestToolcallsApprovalOwnership(t *testing.T) {
	approval := newToolcallsInspectorModel(t)
	approval.phase = phaseAwaitingApproval
	got, _ := approval.runToolcalls()
	if got.(Model).modal != nil {
		t.Fatal("inspector stole approval surface")
	}
	approval = shellAskModel(t, longShellArgs)
	askID := approvalSurfaceOf(t, approval).ask.AskID
	approval, _ = pressKey(approval, ctrlT)
	approval, _ = pressKey(approval, tea.KeyPressMsg{Code: 'r'})
	approval, _ = pressKey(approval, tea.KeyPressMsg{Code: tea.KeyTab})
	approval, _ = pressKey(approval, tea.KeyPressMsg{Code: tea.KeyRight})
	before := assertApprovalPending(t, approval, askID, "")
	if !before.argsViewOpen || !before.argsViewRaw || before.ask.focusedVerdict != client.VerdictDeny {
		t.Fatalf("approval precondition lost: %#v", before)
	}
	updated, _ := approval.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	after := assertApprovalPending(t, updated.(Model), askID, "")
	if !after.argsViewOpen || !after.argsViewRaw || after.ask.focusedVerdict != client.VerdictDeny {
		t.Fatalf("F6 changed approval-owned state: %#v", after)
	}
	if _, ok := updated.(Model).modal.(*toolcallsState); ok {
		t.Fatal("F6 opened inspector over approval")
	}
}
