package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestDelegationAvailableResultAcrossUISurfaces(t *testing.T) {
	const args = `{"path":"src/auth.go"}`
	const body = "RESULT BODY MUST NOT APPEAR"
	for _, family := range []string{"Subagent", "Parallel", "Team"} {
		for _, earlyError := range []bool{false, true} {
			t.Run(family+map[bool]string{false: "/success", true: "/failure"}[earlyError], func(t *testing.T) {
				m := newMCPModel(t, aztec(), nil)
				var tab agentsTab
				switch family {
				case "Subagent":
					m = seedSubagents(m, "parent", startSub("parent", "child", "inspect"))
					tab = tabSubagents
				case "Parallel":
					m = seedParallel(m, "parent", startPar("parent", "all", 1), branchStartPar("parent", 0, "child", "inspect"))
					tab = tabParallel
				case "Team":
					m.conv.addTool("parent", "Team", `{}`)
					m = applyAll(m, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "parent", TeamID: "team", Roster: []client.TeamMemberSpec{{Name: "scout"}}})
					tab = tabTeams
				}
				send := func(kind, id, detail string, failed bool) {
					t.Helper()
					switch family {
					case "Subagent":
						m = applyAll(m, client.EventToMsg(&mecatlv1.Event{Type: "subagent.tool", Subagent: &mecatlv1.Subagent{ParentCallId: "parent", ChildId: "child", InnerKind: kind, ChildToolCallId: id, ToolName: "Read", Detail: detail, IsError: failed}}))
					case "Parallel":
						m = applyAll(m, client.EventToMsg(&mecatlv1.Event{Type: "parallel.branch", Parallel: &mecatlv1.Parallel{Kind: "branch_tool", ParentCallId: "parent", BranchIndex: 0, InnerKind: kind, ChildToolCallId: id, ToolName: "Read", Detail: detail, IsError: failed}}))
					case "Team":
						m = applyAll(m, client.EventToMsg(&mecatlv1.Event{Type: "team.member", Team: &mecatlv1.Team{ParentCallId: "parent", Member: "scout", InnerKind: kind, ChildToolCallId: id, ToolName: "Read", Detail: detail, IsError: failed}}))
					}
				}
				trace := func() []teamTrace {
					t.Helper()
					switch family {
					case "Subagent":
						card, _ := m.conv.subagentCard("parent")
						return traceFromScroll(card.Update.Trace)
					case "Parallel":
						return m.conv.parallelGroups[0].branches[0].trace
					default:
						card, _ := m.conv.teamCard("parent")
						return traceFromScroll(card.Update.Lanes[0].Trace)
					}
				}
				check := func(want string, firstResolved, firstProvisional, firstError bool) {
					t.Helper()
					rows := trace()
					if len(rows) != 2 || rows[0].intent != "src/auth.go" || rows[0].resolved != firstResolved || rows[0].provisional != firstProvisional || rows[0].isError != firstError || rows[1].resolved {
						t.Fatalf("wrong trace: %+v", rows)
					}
					m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 50}, tea.KeyPressMsg{Code: tea.KeyF6})
					if m.agentsTab != tab {
						t.Fatalf("F6 tab = %v, want %v", m.agentsTab, tab)
					}
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
					for _, out := range []string{ansi.Strip(newTestRenderer().renderTrace(rows)), ansi.Strip(m.View().Content)} {
						if !strings.Contains(out, want) || !strings.Contains(out, "… Read · other.go · pending") || strings.Contains(out, body) {
							t.Fatalf("delegation trace = %q, want %q and pending", out, want)
						}
					}
					if family != "Parallel" {
						var tools []teamTrace
						if family == "Subagent" {
							card, _ := m.conv.subagentCard("parent")
							tools = traceFromScroll(card.Update.Trace)
						} else {
							card, _ := m.conv.teamCard("parent")
							tools = traceFromScroll(card.Update.Lanes[0].Trace)
						}
						out := strings.Join(toolcallDetailLines(toolcallDetail{name: family, childTools: scrollTrace(tools)}), "\n")
						if !strings.Contains(out, want) || strings.Contains(out, body) {
							t.Fatalf("parent inspector = %q", out)
						}
					}
					m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
				}
				send("tool.call", "same", args, false)
				send("tool.call", "second", `{"path":"other.go"}`, false)
				check("… Read · src/auth.go · pending", false, false, false)
				send("tool.result.available", "same", body, earlyError)
				wantEarly := "✓ Read · src/auth.go · result received · finalizing"
				if earlyError {
					wantEarly = "✗ Read · src/auth.go · failed · finalizing"
				}
				check(wantEarly, true, true, earlyError)
				send("tool.result.available", "same", "duplicate", !earlyError)
				if rows := trace(); rows[0].isError != earlyError || rows[0].detail != body {
					t.Fatalf("duplicate availability changed result: %+v", rows)
				}
				send("tool.result", "same", "canonical", !earlyError)
				wantFinal := "✗ Read · src/auth.go · failed"
				if earlyError {
					wantFinal = "✓ Read · src/auth.go"
				}
				check(wantFinal, true, false, !earlyError)
				send("tool.result.available", "same", "late availability", earlyError)
				send("tool.result", "same", "duplicate canonical", earlyError)
				if rows := trace(); rows[0].provisional || rows[0].isError != !earlyError || rows[0].detail != "canonical" {
					t.Fatalf("replay changed canonical result: %+v", rows)
				}
			})
		}
	}
}

func TestDelegationAvailableResultPairing(t *testing.T) {
	trace := traceAppendTool(nil, "same", "Read", "first", "a")
	trace = traceAppendTool(trace, "same", "Read", "second", "b")
	trace = traceSetToolResult(trace, "same", "Read", "available", true, true, "a")
	trace = traceSetToolResult(trace, "same", "Read", "wrong lane", false, false, "c")
	trace = traceSetToolResult(trace, "same", "Read", "canonical", false, false, "a")
	if !trace[0].resolved || trace[0].provisional || trace[0].isError || trace[0].detail != "canonical" || trace[1].resolved {
		t.Fatalf("lane correlation: %+v", trace)
	}
	legacy := traceAppendTool(nil, "", "Read", "args")
	ambiguous := traceAppendTool(traceAppendTool(nil, "", "Read", "first"), "", "Read", "second")
	ambiguous = traceSetToolResult(ambiguous, "", "Read", "unmatched", true, true)
	if ambiguous[0].resolved || ambiguous[1].resolved {
		t.Fatalf("availability guessed repeated name: %+v", ambiguous)
	}
	legacy = traceSetToolResult(legacy, "", "Read", "available", true, true)
	legacy = traceSetToolResult(legacy, "", "Read", "canonical", false, false)
	if !legacy[0].provisional || !legacy[0].isError || legacy[0].detail != "available" {
		t.Fatalf("ID-less canonical guessed a provisional match: %+v", legacy)
	}
	for i := 0; i <= maxTraceEntries; i++ {
		trace = traceAppendTool(trace, string(rune('a'+i)), "Read", "next", "a")
	}
	trace = traceSetToolResult(trace, "same", "Read", "evicted", true, true, "a")
	trace = traceSetToolResult(trace, "same", "Read", "evicted canonical", true, false, "a")
	retained := false
	for _, row := range trace {
		if row.lane == "b" && row.id == "same" {
			retained = true
			if row.resolved || row.detail != "second" {
				t.Fatalf("evicted result corrupted retained lane: %+v", row)
			}
		}
	}
	if !retained {
		t.Fatal("other lane's call was lost")
	}
	for _, row := range trace {
		if row.lane == "a" && row.id == "same" {
			t.Fatalf("evicted call retained: %+v", row)
		}
	}
}

func TestDelegationAvailabilityGolden(t *testing.T) {
	var trace []teamTrace
	for _, id := range []string{"pending", "success", "failure", "canonical"} {
		trace, _ = routeTraceEvent(trace, "", "tool.call", id, "Read", `{"path":"`+id+`.go"}`, "", false)
	}
	trace, _ = routeTraceEvent(trace, "", "tool.result.available", "success", "Read", "hidden result", "", false)
	trace, _ = routeTraceEvent(trace, "", "tool.result.available", "failure", "Read", "hidden error", "", true)
	trace, _ = routeTraceEvent(trace, "", "tool.result.available", "canonical", "Read", "hidden provisional", "", true)
	trace, _ = routeTraceEvent(trace, "", "tool.result", "canonical", "Read", "hidden canonical", "", false)
	compareGolden(t, "delegation_availability.golden", []byte(ansi.Strip(newTestRenderer().renderTrace(trace))+"\n"))
}
