package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestWorkspaceInviteBoundedProjection(t *testing.T) {
	for _, width := range []int{12, 40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := scenario2Model(t, width, 30, false, phaseIdle, map[string][]string{"Workspace": {"ctrl+f12"}})
			inspections := 0
			m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) {
				inspections++
				return WorkspaceSnapshot{}, nil
			}
			m.conv.addUser("before observations")
			m.expandConversation = true
			m.refreshView()
			if strings.Contains(m.conversationContent(), "/workspace") || m.conversationView.frame.appendixID != 0 {
				t.Fatal("fabricated appendix without observations")
			}
			m.conv.recordFileChange("not-rendered-path-0000")
			m.refreshView()
			one := m.conversationContent()
			for i := 1; i < 10000; i++ {
				m.conv.recordFileChange(fmt.Sprintf("not-rendered-path-%04d", i))
			}
			m.refreshView()
			many := m.conversationContent()
			if many != one || many != m.vp.GetContent() || inspections != 0 {
				t.Fatal("appendix grew with path count, projection diverged, or rendering inspected workspace")
			}
			if strings.Contains(many, "not-rendered-path") {
				t.Fatal("F9 enumerated paths")
			}
			if width == 100 && !strings.Contains(many, "ctrl+f12 workspace · /workspace") {
				t.Fatal("missing effective remapped key and command fallback")
			}
			frame := m.conversationView.frame
			row := frame.firstRegionRow(m.conv.testAppendixID(), conversationRegionAppendix)
			if row < 0 || row != len(frame.lines)-1 || len(frame.lines) != len(frame.provenance) {
				t.Fatal("invitation lost single-row appendix provenance")
			}
			if lipgloss.Width(frame.lines[row]) > width {
				t.Fatalf("invite exceeds width: %q", frame.lines[row])
			}
			_ = m.View()
			m, _ = pressKey(m, f9)
			if strings.Contains(m.conversationContent(), "/workspace") || inspections != 0 || len(m.conv.testChangedFiles()) != 10000 {
				t.Fatal("collapse inspected workspace or changed accumulator")
			}
		})
	}
}

func TestWorkspaceInviteAnchorsAndInspectorOffTail(t *testing.T) {
	m := scenario2Model(t, 100, 24, false, phaseRunning, nil)
	first := m.conv.addUser(strings.Repeat("reading position\n", 40))
	m.conv.recordFileChange("observed-before-later-block")
	last := m.conv.addUser("physically preceding appendix")
	m.expandConversation = true
	m.refreshView()
	frame := m.conversationView.frame
	appendixRow := frame.firstRegionRow(m.conv.testAppendixID(), conversationRegionAppendix)
	anchor, ok := frame.anchorForRow(appendixRow)
	if !ok {
		t.Fatal("missing appendix anchor")
	}
	m.expandConversation = false
	m.refreshView()
	collapsed := m.conversationView.frame
	fallback := m.conversationView.restore(collapsed, anchor)
	if collapsed.provenance[fallback].blockID != uint64(last) {
		t.Fatal("collapsed appendix did not fall back to physically preceding block")
	}

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.conversationView.mode != anchored || m.conversationView.anchor.blockID != uint64(first) {
		t.Fatal("precondition: reading off tail")
	}
	before := m.conversationView.anchor
	m, _ = pressKey(m, f9)
	if m.conversationView.anchor != before {
		t.Fatal("invitation toggle displaced reading anchor")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10})
	if _, ok := m.modal.(*workspaceState); !ok {
		t.Fatal("F10 did not open inspector")
	}
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "streaming while inspecting"}, renderTickMsg{})
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.conversationView.mode != anchored || m.conversationView.anchor != before || m.vp.AtBottom() {
		t.Fatal("inspector/streaming lost off-tail reading position")
	}
	if m.conversationContent() != m.vp.GetContent() {
		t.Fatal("content and displayed projection diverged")
	}
}
