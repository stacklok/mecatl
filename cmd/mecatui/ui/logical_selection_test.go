package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestADR_0301_SelectionPreservesLiveStableText pins ADR 0301 §4: a selection
// is logical text, not a rendered row range. Reflow and a pending stream delta
// must retain its exact ANSI-free copy while its context still resolves.
func TestADR_0301_SelectionPreservesLiveStableText(t *testing.T) {
	const marker = "LOGICALSELECTIONMARKER"
	m, _ := selModel(t)
	m.conv.addUser("request")
	m.conv.addTool("tool", "Bash", `{"cmd":"seq 40"}`)
	m.conv.resolveTool("tool", strings.TrimRight(strings.Repeat("tool body\n", 40), "\n"), false)
	m.conv.appendAssistant(marker + " survives reflow")
	m.phase = phaseIdle
	m.refreshView()
	line := lineIndexContaining(m.vp.GetContent(), marker)
	if line < 0 {
		t.Fatal("precondition: marker line missing")
	}
	m = m.wordSelect(line, strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker))
	if got := m.sel.snapshot; got != marker {
		t.Fatalf("precondition: copied text = %q, want %q", got, marker)
	}

	// The delta is deliberately left pending: copy must resolve from the live
	// logical frame rather than the stale viewport projection.
	m.phase = phaseRunning
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\npending tail"})
	if !m.viewDirty {
		t.Fatal("precondition: delta must be pending before the render tick")
	}
	// Copy before the pending delta is flushed: the clipboard must receive the
	// original ANSI-free logical text, not stale or altered viewport coordinates.
	copied, cmd := m.copySelection()
	m = copied.(Model)
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
		t.Fatalf("pending-delta copied payload = %q (ok=%v), want %q", got, ok, marker)
	}
	// Expanding an earlier card changes the frame above the selected text.
	m.expandTools = true
	m.refreshView()
	if !m.sel.active {
		t.Fatal("stable selection was cleared by reflow")
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != marker {
		t.Fatalf("reflowed selection = %q, want %q", got, marker)
	}
	copied, cmd = m.copySelection()
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
		t.Fatalf("reflowed copied payload = %q (ok=%v), want %q", got, ok, marker)
	}
	if got := copied.(Model).statusMsg; !strings.Contains(ansi.Strip(got), "copied") {
		t.Fatalf("copy status = %q, want copied status", got)
	}

	t.Run("duplicate context requires canonical offset", func(t *testing.T) {
		frame := renderedFrame{
			lines: []string{"duplicated endpoint", "duplicated endpoint"},
			provenance: []renderedRow{
				{blockID: 1, region: conversationRegionBody, sourceOffset: 0, text: true},
				{blockID: 1, region: conversationRegionBody, sourceOffset: 20, text: true},
			},
		}
		point := selectionPoint{
			blockID: 1, region: conversationRegionBody, sourceOffset: 30,
			before: "duplicated", after: " endpoint",
		}
		line, col, ok := resolveSelectionPoint(frame, point)
		if !ok || line != 1 || col != 10 {
			t.Fatalf("duplicate context resolved to (%d, %d, %v), want (1, 10, true)", line, col, ok)
		}

		ambiguous := point
		ambiguous.sourceOffset = 10
		frame.provenance[1].sourceOffset = 0
		if _, _, ok := resolveSelectionPoint(frame, ambiguous); ok {
			t.Fatal("ambiguous duplicate canonical offsets must fail closed")
		}
	})
}

// TestLogicalConversationAnchors_Scenario1_ChangingFrame proves that the live
// Model event and mouse-input paths preserve a selected subagent-card goal when
// an unrelated streamed update changes the rendered frame.
func TestLogicalConversationAnchors_Scenario1_ChangingFrame(t *testing.T) {
	const marker = "SUBAGENTGOALMARKER"
	m, _ := selModel(t)
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ToolCallMsg{ID: "parent", Name: "Subagent", Args: `{"prompt":"investigate"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "parent", Goal: marker + " inspect auth"},
	)
	line := lineIndexContaining(m.vp.GetContent(), marker)
	if line < 0 {
		t.Fatal("precondition: subagent goal missing from Model event render")
	}
	y := convTopRow(m) + line - m.vp.YOffset()
	x := strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker)
	if x < 0 || y < convTopRow(m) || y >= convTopRow(m)+m.vp.Height() {
		t.Fatalf("precondition: goal is not selectable at (%d,%d)", x, y)
	}
	m, _ = pressMouse(m, tea.MouseLeft, x, y)
	m, _ = pressMouse(m, tea.MouseLeft, x, y)
	if got := selectedText(m.vp.GetContent(), m.sel); got != marker {
		t.Fatalf("selected live subagent goal = %q, want %q", got, marker)
	}

	// This is the coalesced live-delta path, not a direct conversation mutation.
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: "unrelated tail update"},
		renderTickMsg{},
	)
	if !m.sel.active {
		t.Fatal("stable subagent-card selection was cleared by a live delta")
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != marker {
		t.Fatalf("selection after changing frame = %q, want %q", got, marker)
	}
	copied, cmd := m.copySelection()
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
		t.Fatalf("copied live-card payload = %q (ok=%v), want %q", got, ok, marker)
	}
	if got := copied.(Model).statusMsg; !strings.Contains(ansi.Strip(got), "copied") {
		t.Fatalf("copy status = %q, want copied status", got)
	}
}
