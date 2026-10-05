package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiQuieterToolCalls_Scenario3_RebindAndLegacyAlias(t *testing.T) {
	overrides := map[string][]string{
		"Toolcalls":          {"ctrl+f10", "ctrl+f11"},
		"ExpandConversation": {"ctrl+f12", "ctrl+f13"},
	}
	m := scenario2Model(t, 100, 30, false, phaseIdle, overrides)
	m = applyAll(m, client.TurnStartMsg{Turn: 1}, client.ReasoningDeltaMsg{Turn: 1, Text: "secret reasoning"}, client.AssistantDeltaMsg{Turn: 1, Text: "answer"})
	m.conv.addPermanentError("short error\nprivate error details")
	m.conv.recordFileChange("changed.go")
	m = applyAll(m, client.ToolCallMsg{ID: "long", Name: "Shell", Args: `{"command":"` + strings.Repeat("private argument ", 90) + `"}`})
	m.refreshView()
	collapsed := detailText(m)
	for _, want := range []string{"ctrl+f12 expand", "ctrl+f12 shows details", "ctrl+f10 inspect"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsed hint missing %q: %q", want, collapsed)
		}
	}
	for _, forbidden := range []string{"ctrl+f13 expand", "ctrl+f13 shows details", "ctrl+f11 inspect", "f9 expand", "f9 shows details", "expand all tool"} {
		if strings.Contains(collapsed, forbidden) {
			t.Errorf("collapsed hint advertises %q: %q", forbidden, collapsed)
		}
	}
	help := stripANSIstr(helpBody(aztec(), allOnCaps(), m.helpKeyMarkings()))
	for _, row := range []struct{ action, chords string }{
		{"open tool calls", "ctrl+f10/ctrl+f11"},
		{"open full approval details", "ctrl+f10/ctrl+f11"},
		{"expand turn details", "ctrl+f12/ctrl+f13"},
	} {
		found := false
		for _, line := range strings.Split(help, "\n") {
			if strings.Contains(line, row.action) {
				found = true
				if !strings.Contains(line, row.chords) {
					t.Errorf("%s row missing full binding %q: %q", row.action, row.chords, line)
				}
			}
		}
		if !found {
			t.Errorf("help missing %q: %q", row.action, help)
		}
	}
	if strings.Contains(strings.ToLower(help), "expand all tool") {
		t.Errorf("retired action in help: %q", help)
	}
	approval := scenario2Model(t, 100, 30, false, phaseRunning, overrides)
	const askID = "sess-test-0001:1:shell-1"
	approval = applyAll(approval, client.PermissionAskMsg{AskID: askID, Tool: "Shell", Args: `{"command":"echo hi"}`, Reason: "approval required"})
	if view := stripANSIstr(approval.View().Content); !strings.Contains(view, "ctrl+f10 full args") || strings.Contains(view, "ctrl+f11 full args") {
		t.Errorf("approval compact hint did not use first chord: %q", view)
	}
	approval, _ = pressKey(approval, tea.KeyPressMsg{Code: tea.KeyF11, Mod: tea.ModCtrl})
	if s := assertApprovalPending(t, approval, askID, ""); !s.argsViewOpen {
		t.Error("second legacy chord did not open approval details")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF11, Mod: tea.ModCtrl})
	if toolcallsForTest(t, m) == nil {
		t.Fatal("second legacy chord did not open inspector")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF13, Mod: tea.ModCtrl})
	if expanded := detailText(m); !strings.Contains(expanded, "private error details") || !strings.Contains(expanded, "secret reasoning") || !strings.Contains(expanded, "changed this session") {
		t.Errorf("second conversation chord did not expand details: %q", expanded)
	}
}
