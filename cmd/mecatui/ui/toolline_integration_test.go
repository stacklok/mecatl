package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestToolLineEventToUISurfaces(t *testing.T) {
	const args = `{"path":"src/auth.go"}`
	const result = "     1\tRESULT BODY MUST NOT BE CALL INTENT"
	check := func(t *testing.T, trace []teamTrace) {
		t.Helper()
		if len(trace) != 1 || trace[0].intent != "src/auth.go" || !trace[0].resolved || !strings.Contains(trace[0].detail, "RESULT BODY") {
			t.Fatalf("call/result projection: %+v", trace)
		}
		r := newTestRenderer()
		for _, out := range []string{r.renderTrace(trace), strings.Join(renderedTraceLines(r.th, defaultHelpKeys(), 80, trace), "\n")} {
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "✓ Read · src/auth.go") || strings.Contains(plain, "RESULT BODY") || strings.Contains(plain, "success") {
				t.Fatalf("trace displayed result instead of call: %q", plain)
			}
		}
	}
	checkAgents := func(t *testing.T, m Model, tab agentsTab) {
		t.Helper()
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 50}, tea.KeyPressMsg{Code: tea.KeyF6})
		if m.agentsTab != tab {
			t.Fatalf("F6 tab = %v, want %v", m.agentsTab, tab)
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if out := ansi.Strip(m.View().Content); !strings.Contains(out, "✓ Read · src/auth.go") || strings.Contains(out, "RESULT BODY") {
			t.Fatalf("F6 focus lost bounded call-side intent: %q", out)
		}
	}
	t.Run("Subagent", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "parent", startSub("parent", "child", "inspect"))
		for _, msg := range []client.SubagentMsg{
			{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.call", ChildToolCallID: "read", ToolName: "Read", Detail: args},
			{Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child", InnerKind: "tool.result", ChildToolCallID: "read", ToolName: "Read", Detail: result},
		} {
			updated, _ := m.Update(msg)
			m = updated.(Model)
		}
		card, ok := m.conv.subagentCard("parent")
		if !ok {
			t.Fatal("missing card")
		}
		check(t, traceFromScroll(card.Update.Trace))
		checkAgents(t, m, tabSubagents)
		if card.Update.Trace[0].Intent != "src/auth.go" {
			t.Fatalf("lost scrollback intent: %+v", card.Update.Trace)
		}
		s := &toolcallsState{deps: m.surfaceDeps()}
		out := strings.Join(s.styledToolcallDetailLines(toolcallDetail{name: "Subagent", childTools: card.Update.Trace}), "\n")
		if plain := ansi.Strip(out); !strings.Contains(plain, "✓ Read · src/auth.go") || strings.Contains(plain, "RESULT BODY") {
			t.Fatalf("inspector: %q", plain)
		}
	})
	t.Run("Parallel", func(t *testing.T) {
		m := seedParallel(newMCPModel(t, aztec(), nil), "parent", startPar("parent", "all", 1), branchStartPar("parent", 0, "branch", "inspect"))
		for _, kind := range []string{"tool.call", "tool.result"} {
			detail := args
			if kind == "tool.result" {
				detail = result
			}
			updated, _ := m.Update(client.ParallelMsg{Kind: client.ParallelBranchTool, ParentCallID: "parent", BranchIndex: 0, InnerKind: kind, ChildToolCallID: "read", ToolName: "Read", Detail: detail})
			m = updated.(Model)
		}
		check(t, m.conv.parallelGroups[0].branches[0].trace)
		checkAgents(t, m, tabParallel)
	})
	t.Run("Team", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m.conv.addTool("parent", "Team", `{}`)
		updated, _ := m.Update(client.TeamMsg{Kind: client.TeamStart, ParentCallID: "parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "scout"}}})
		m = updated.(Model)
		for _, kind := range []string{"tool.call", "tool.result"} {
			detail := args
			if kind == "tool.result" {
				detail = result
			}
			updated, _ = m.Update(client.TeamMsg{Kind: client.TeamMember, ParentCallID: "parent", Member: "scout", InnerKind: kind, ChildToolCallID: "read", ToolName: "Read", Detail: detail})
			m = updated.(Model)
		}
		card, ok := m.conv.teamCard("parent")
		if !ok {
			t.Fatal("missing team")
		}
		check(t, traceFromScroll(card.Update.Lanes[0].Trace))
		checkAgents(t, m, tabTeams)
	})
	t.Run("TruncatedAndHostile", func(t *testing.T) {
		trace := traceAppendTool(nil, "bad", "Read", `{"path":"never-completed`)
		trace = traceMarkToolResult(trace, "bad", "Read", result, false)
		if trace[0].intent != "Read" || strings.Contains(ansi.Strip(newTestRenderer().renderTrace(trace)), "RESULT BODY") {
			t.Fatalf("truncated arguments fabricated intent: %+v", trace)
		}
		trace = traceAppendTool(nil, "hostile", "Read", `{"path":"src/\u001b[2Jauth.go"}`)
		trace = traceMarkToolResult(trace, "hostile", "Read", result, true)
		plain := ansi.Strip(newTestRenderer().renderTrace(trace))
		if !strings.Contains(plain, "✗ Read · src/[2Jauth.go · failed") || strings.Contains(plain, "\x1b[2J") || strings.Contains(plain, "RESULT BODY") {
			t.Fatalf("hostile call or result escaped: %q", plain)
		}
		t.Run("CallIntentBeyondVisualPreview", func(t *testing.T) {
			path := strings.Repeat("p", maxTraceMessageLen-len(`{"path":""}`))
			trace := traceAppendTool(nil, "long", "Read", `{"path":"`+path+`"}`)
			trace = traceMarkToolResult(trace, "long", "Read", result, false)
			if got := trace[0].intent; !strings.HasPrefix(got, strings.Repeat("p", 70)) || len([]rune(got)) != maxTraceDetailLen || strings.Contains(ansi.Strip(newTestRenderer().renderTrace(trace)), "RESULT BODY") {
				t.Fatalf("bounded call-side intent lost behind 80-rune visual preview: %+v", trace)
			}
			truncated := traceAppendTool(nil, "too-long", "Read", `{"path":"`+path+`x"}`)
			if truncated[0].intent != "Read" || len([]rune(truncated[0].detail)) != maxTraceDetailLen {
				t.Fatalf("200-rune ingress cap did not keep truncated JSON honest: %+v", truncated[0])
			}
		})
	})
	t.Run("TopLevel", func(t *testing.T) {
		m := newToolcallsInspectorModel(t)
		m = applyAll(m, client.ToolCallMsg{ID: "read", Name: "Read", Args: args}, client.ToolResultMsg{CallID: "read", Content: result})
		entries := m.toolcallEntries()
		if len(entries) != 1 {
			t.Fatalf("entries: %+v", entries)
		}
		r := newTestRenderer()
		for _, out := range []string{r.renderSettledToolLine(1, entries[0].toolcallProjection).text, entries[0].summary()} {
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "Read · src/auth.go") || strings.Contains(plain, "RESULT BODY") {
				t.Fatalf("top-level: %q", plain)
			}
		}
		opened, _ := m.runToolcalls()
		s := toolcallsForTest(t, opened.(Model))
		list, _ := s.Render(80, 12)
		if plain := ansi.Strip(list); !strings.Contains(plain, "✓ Read · src/auth.go") || strings.Contains(plain, "RESULT BODY") {
			t.Fatalf("list: %q", plain)
		}
		s.selected, s.detail = 0, true
		s.refreshDetail(&m.conv.scrollback)
		rows := toolcallDetailLines(*s.detailEntry)
		if rows[0] != "✓ Read · src/auth.go" || !strings.Contains(strings.Join(rows[1:], "\n"), "RESULT BODY MUST NOT BE CALL INTENT") {
			t.Fatalf("inspector identity/result boundary: %q", rows)
		}
		fallback := toolcallDetailLines(toolcallDetail{name: "Read", intent: "not json", state: toolcallPending})
		if fallback[0] != "… Read · running" {
			t.Fatalf("malformed arguments duplicated tool name: %q", fallback[0])
		}
	})
}
