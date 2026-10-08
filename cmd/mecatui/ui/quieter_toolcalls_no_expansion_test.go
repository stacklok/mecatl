package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestQuieterToolcallsNoToolCardExpansion(t *testing.T) {
	for name, fn := range map[string]any{
		"tool":     (*renderer).prepareTypedToolCard,
		"subagent": (*renderer).prepareSubagentCard,
		"team":     (*renderer).prepareTeamCard,
	} {
		if got := reflect.TypeOf(fn).NumIn(); got != 3 { // renderer, card, lifecycle state
			t.Errorf("%s preparer still accepts an expansion parameter (%d inputs)", name, got)
		}
	}
	m := scenario2Model(t, 88, 24, false, phaseRunning, nil)
	m = applyAll(m,
		client.ToolCallMsg{ID: "shell", Name: "Shell", Args: `{"command":"` + strings.Repeat("long argument ", 20) + `"}`},
		client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{"goal":"inspect"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "inspect"},
		client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "sub", ChildID: "child", InnerKind: "message.delta", Text: "private child trace"},
		client.ToolCallMsg{ID: "team", Name: "Team", Args: `{}`},
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team-1", Roster: []client.TeamMemberSpec{{Name: "lead", Lead: true}}},
		client.TeamMsg{Kind: client.TeamMember, ParentCallID: "team", TeamID: "team-1", Member: "lead", InnerKind: "message.delta", Text: "private team trace"},
	)
	before := make(map[scrollback.BlockID]string)
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		s := m.conv.scrollback.SnapshotAt(i)
		before[s.ID] = m.rend.renderSnapshot(i, s, false)
	}
	m, _ = pressKey(m, f9)
	if !m.expandConversation {
		t.Fatal("conversation toggle did not activate")
	}
	for i := 0; i < m.conv.scrollback.Len(); i++ {
		s := m.conv.scrollback.SnapshotAt(i)
		if got := m.rend.renderSnapshot(i, s, true); got != before[s.ID] {
			t.Errorf("%T expanded when conversation details toggled\nbefore: %q\nafter: %q", s.Payload, before[s.ID], got)
		}
	}
	if out := stripANSIstr(m.conversationContent()); strings.Contains(out, "private child trace") || strings.Contains(out, "private team trace") || strings.Contains(out, strings.Repeat("long argument ", 10)) {
		t.Errorf("conversation exposed tool detail: %q", out)
	}
}
