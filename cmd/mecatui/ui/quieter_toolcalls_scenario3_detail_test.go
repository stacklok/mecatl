package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func detailText(m Model) string { return stripANSIstr(m.conversationContent()) }

func TestMecatuiQuieterToolCalls_Scenario3_ConversationDetailToggle(t *testing.T) {
	for _, ph := range []struct {
		name  string
		phase phase
	}{{"idle", phaseIdle}, {"running", phaseRunning}, {"replay", phaseReplay}} {
		t.Run(ph.name, func(t *testing.T) {
			m := scenario2Model(t, 90, 24, false, phaseRunning, nil)
			m = applyAll(m, client.TurnStartMsg{Turn: 1}, client.ReasoningDeltaMsg{Turn: 1, Text: "reason one full text"}, client.AssistantDeltaMsg{Turn: 1, Text: "first answer"}, client.TurnStartMsg{Turn: 2}, client.ReasoningDeltaMsg{Turn: 2, Text: "reason two full text"}, client.AssistantDeltaMsg{Turn: 2, Text: "second answer"}, client.TurnStartMsg{Turn: 3}, client.AssistantDeltaMsg{Turn: 3, Text: "no reasoning answer"}, client.ToolCallMsg{ID: "shell", Name: "Shell", Args: `{"command":"` + strings.Repeat("long argument ", 20) + "PRIVATE_SHELL_TAIL" + `"}`}, client.ToolCallMsg{ID: "edit", Name: "Edit", Args: `{"path":"detail.go","old_string":"before","new_string":"` + strings.Repeat("diff line ", 40) + `"}`}, client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{"goal":"inspect"}`}, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"}, client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub", ChildID: "child", InnerKind: "message.delta", Text: "private trace words", ToolCount: 1}, client.ToolCallMsg{ID: "team", Name: "Team", Args: `{}`}, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "lead", Lead: true}}}, client.TeamMsg{Kind: client.TeamMember, ParentCallID: "team", TeamID: "team-1", Member: "lead", InnerKind: "message.delta", Text: "team private trace"})
			m.conv.addError("transient remains visible")
			m.conv.addPermanentError("provider rejected\npermanent detail hidden")
			m.conv.recordFileChange("detail.go")
			m.phase = ph.phase
			m.refreshView()
			before := detailText(m)
			if !strings.Contains(before, "f9 expand") || !strings.Contains(before, "f9 shows details") || strings.Contains(before, "reason one full text") || strings.Contains(before, "permanent detail hidden") || strings.Contains(before, "changed this session") {
				t.Fatalf("bad collapsed view: %q", before)
			}
			seen := map[scrollback.Kind]int{}
			snapshots := make(map[scrollback.BlockID]string)
			for i := 0; i < m.conv.scrollback.Len(); i++ {
				s := m.conv.scrollback.SnapshotAt(i)
				seen[s.Payload.Kind()]++
				switch s.Payload.(type) {
				case scrollback.ToolCardSnapshot, scrollback.SubagentCardSnapshot, scrollback.TeamCardSnapshot, scrollback.ErrorCardSnapshot:
					if e, ok := s.Payload.(scrollback.ErrorCardSnapshot); ok && e.Permanent {
						continue
					}
					snapshots[s.ID] = m.rend.renderSnapshot(i, s, false)
				case scrollback.AssistantCardSnapshot:
					continue
				}
			}
			for _, kind := range []scrollback.Kind{scrollback.KindAssistant, scrollback.KindTool, scrollback.KindSubagent, scrollback.KindTeam, scrollback.KindError} {
				if seen[kind] == 0 {
					t.Fatalf("missing fixture block kind %v", kind)
				}
			}
			m, _ = pressKey(m, f9)
			after := detailText(m)
			for _, s := range []string{"reason one full text", "reason two full text", "may not reflect its actual process", "permanent detail hidden", "changed this session", "transient remains visible", "no reasoning answer"} {
				if !strings.Contains(after, s) {
					t.Errorf("missing %q in expanded view", s)
				}
			}
			if strings.Contains(after, "private trace words") || strings.Contains(after, "team private trace") || strings.Contains(after, "PRIVATE_SHELL_TAIL") {
				t.Errorf("conversation toggle exposed tool/delegation detail: %q", after)
			}
			for i := 0; i < m.conv.scrollback.Len(); i++ {
				s := m.conv.scrollback.SnapshotAt(i)
				if want, ok := snapshots[s.ID]; ok && m.rend.renderSnapshot(i, s, true) != want {
					t.Errorf("unaffected block %d changed on toggle", s.ID)
				}
			}
			if m.prompt.Value() != "" {
				t.Fatalf("toggle reached prompt: %q", m.prompt.Value())
			}
			m, _ = pressKey(m, f9)
			if got := detailText(m); got != before {
				t.Errorf("collapse did not restore view\ngot %q\nwant %q", got, before)
			}
		})
	}
	t.Run("rebind", func(t *testing.T) {
		m := scenario2Model(t, 90, 24, false, phaseIdle, map[string][]string{"ExpandConversation": {"ctrl+f10"}})
		m.conv.appendAssistant("answer")
		m.conv.addPermanentError("error\nprivate details")
		m.refreshView()
		before := detailText(m)
		if !strings.Contains(before, "ctrl+f10 shows details") || strings.Contains(before, "f9 shows details") {
			t.Fatalf("wrong hint: %q", before)
		}
		m, _ = pressKey(m, f9)
		if detailText(m) != before {
			t.Fatal("f9 changed rebound conversation")
		}
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModCtrl})
		if !strings.Contains(detailText(m), "private details") {
			t.Fatal("rebound key did not expand")
		}
	})
	t.Run("streaming and approval", func(t *testing.T) {
		m := scenario2Model(t, 90, 24, false, phaseRunning, nil)
		m = applyAll(m, client.TurnStartMsg{Turn: 1}, client.ReasoningDeltaMsg{Turn: 1, Text: "streamed summary content"})
		before := detailText(m)
		if !strings.Contains(before, "reasoning… · f9 expand") || strings.Contains(before, "streamed summary content") {
			t.Fatalf("streaming summary not collapsed: %q", before)
		}
		m, _ = pressKey(m, f9)
		if after := detailText(m); !strings.Contains(after, "streamed summary content") || !strings.Contains(after, reasoningCaveat) {
			t.Fatalf("streaming summary not expanded: %q", after)
		}
		m, _ = pressKey(m, f9)
		if detailText(m) != before {
			t.Fatal("streaming summary did not collapse")
		}
		modal := approvalModel(t, pendingAsk{AskID: "sess-test-0001:1:edit-1", Tool: "Edit", Args: `{"path":"a.go","old_string":"old","new_string":"new"}`})
		modal.conv.addPermanentError("error\nprivate payload")
		modal.refreshView()
		body, conversation := modal.View().Content, detailText(modal)
		modal, _ = pressKey(modal, f9)
		if modal.View().Content != body || detailText(modal) != conversation || modal.expandConversation {
			t.Fatal("approval modal did not retain ownership of f9")
		}
	})
}

func TestConversationDetailToggleShowsTurnChromeOnlyOnF9(t *testing.T) {
	m := scenario2Model(t, 90, 24, false, phaseRunning, nil)
	m.conv.addUser("Please check the files")
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ReasoningDeltaMsg{Turn: 1, Text: "why this tool is needed"},
		client.AssistantDeltaMsg{Turn: 1, Text: "I will inspect them."},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 1200, OutputTokens: 100}, DurationMs: 4100},
		client.ToolCallMsg{ID: "one", Name: "Read", Args: `{"path":"one.go"}`},
		client.ToolResultMsg{CallID: "one", Content: "content"},
		client.TurnStartMsg{Turn: 2}, // tool-only turn has no assistant prose
		client.TurnEndMsg{Turn: 2, Usage: client.Usage{InputTokens: 1300, OutputTokens: 110}, DurationMs: 3100},
		client.ToolCallMsg{ID: "two", Name: "Grep", Args: `{"pattern":"needle"}`},
		client.ToolResultMsg{CallID: "two", Content: "found"},
	)
	m.conv.recordFileChange("appendix-only.go")
	m.conv.addNotice("notice stays visible")
	m.phase = phaseIdle
	m.refreshView()
	collapsed := detailText(m)
	for _, want := range []string{"Please check the files", "I will inspect them.", "notice stays visible", "reasoning summary", "✓ Read", "✓ Grep"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("normal conversation lacks %q: %q", want, collapsed)
		}
	}
	for _, hidden := range []string{"● mecatl", "↑1.2K ↓100", "↑1.3K ↓110", "4.1s", "3.1s", "why this tool is needed", "changed this session", "appendix-only.go"} {
		if strings.Contains(collapsed, hidden) {
			t.Errorf("normal conversation shows hidden detail %q: %q", hidden, collapsed)
		}
	}
	m, _ = pressKey(m, f9)
	expanded := detailText(m)
	for _, want := range []string{"● mecatl", "↑1.2K ↓100 · 4.1s", "↑1.3K ↓110 · 3.1s", "why this tool is needed", "changed this session", "  appendix-only.go", "I will inspect them.", "notice stays visible", "✓ Read", "✓ Grep"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("F9 view lacks %q: %q", want, expanded)
		}
	}
	if count := strings.Count(expanded, "● mecatl"); count != 1 {
		t.Errorf("F9 resurrected empty assistant heading: count=%d", count)
	}
	m, _ = pressKey(m, f9)
	if got := detailText(m); got != collapsed {
		t.Errorf("F9 did not restore dense view\n got %q\nwant %q", got, collapsed)
	}
}

func TestMecatuiQuieterToolCalls_Scenario4_ErrorDetailAccess(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := scenario2Model(t, width, 14, false, phaseIdle, nil)
			m.conv.addUser(strings.Repeat("anchor above error\n", 40))
			var lines []string
			for i := 0; i < 300; i++ {
				lines = append(lines, fmt.Sprintf("diagnostic-%03d %s", i, strings.Repeat("detail ", 10)))
			}
			m.conv.addError("transient text as received")
			m.conv.addPermanentError("provider rejected\n" + strings.Join(lines, "\n") + "\x1b[31m\x00")
			m.refreshView()
			collapsed := detailText(m)
			if !strings.Contains(collapsed, "f9 shows details") || strings.Contains(collapsed, "ctrl+t shows details") || strings.Contains(collapsed, lines[299]) {
				t.Fatalf("bad collapsed error: %q", collapsed)
			}
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
			anchor := m.conversationView.anchor.blockID
			if want := uint64(m.conv.scrollback.SnapshotAt(0).ID); anchor != want {
				t.Fatalf("anchor not above error: got %d, want %d", anchor, want)
			}
			m, _ = pressKey(m, f9)
			if m.conversationView.anchor.blockID != anchor {
				t.Fatalf("anchor moved %d -> %d", anchor, m.conversationView.anchor.blockID)
			}
			expanded := detailText(m)
			for i := range lines {
				if !strings.Contains(expanded, fmt.Sprintf("diagnostic-%03d", i)) {
					t.Fatalf("missing payload line %d", i)
				}
			}
			if strings.Count(expanded, "detail") < 3000 {
				t.Fatal("permanent error detail was truncated")
			}
			for _, row := range strings.Split(expanded, "\n") {
				if len([]rune(row)) > width {
					t.Fatalf("row exceeds content width %d: %q", width, row)
				}
			}
			if strings.Contains(expanded, "\x00") || strings.Contains(expanded, "\x1b[31m") {
				t.Fatal("unsafe terminal controls in error")
			}
			if !strings.Contains(expanded, "transient text as received") {
				t.Fatal("transient changed")
			}
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
			found := false
			for i := 0; i < 10; i++ {
				if strings.Contains(stripANSIstr(m.vp.View()), "diagnostic-299") {
					found = true
					break
				}
				m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
			}
			if !found {
				t.Fatal("last diagnostic unreachable by scrolling")
			}
			m, _ = pressKey(m, f9)
			if detailText(m) != collapsed {
				t.Fatal("collapse did not restore brief error")
			}
			m, _ = pressKey(m, ctrlT)
			if s := toolcallsForTest(t, m); s == nil || len(s.entries) != 0 {
				t.Fatalf("non-tool error entered inspector: %#v", s)
			}
		})
	}
}
