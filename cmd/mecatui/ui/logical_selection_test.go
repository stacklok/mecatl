package ui

import (
	"strings"
	"testing"

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
}

// TestLogicalConversationAnchors_Scenario1_ChangingFrame proves that a live
// subagent card's stable goal remains selected and copyable while another card
// update changes the rendered frame.
func TestLogicalConversationAnchors_Scenario1_ChangingFrame(t *testing.T) {
	const marker = "SUBAGENTGOALMARKER"
	m, _ := selModel(t)
	m.conv.addTool("parent", "Subagent", `{"prompt":"investigate"}`)
	m.conv.setSubagentStart("parent", marker+" inspect auth", "", "", "", "")
	m.phase = phaseIdle
	m.refreshView()
	line := lineIndexContaining(m.vp.GetContent(), marker)
	if line < 0 {
		t.Fatal("precondition: subagent goal missing")
	}
	m = m.wordSelect(line, strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker))
	if got := m.sel.snapshot; got != marker {
		t.Fatalf("precondition: selected goal = %q, want %q", got, marker)
	}

	// An unrelated tail update must not disturb the card-local logical selection.
	m.conv.appendAssistant("unrelated tail update")
	m.refreshView()
	if !m.sel.active {
		t.Fatal("stable subagent-card selection was cleared")
	}
	if got := selectedText(m.vp.GetContent(), m.sel); got != marker {
		t.Fatalf("subagent-card selection = %q, want %q", got, marker)
	}
	copied, cmd := m.copySelection()
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
		t.Fatalf("subagent-card copied payload = %q (ok=%v), want %q", got, ok, marker)
	}
	if got := copied.(Model).statusMsg; !strings.Contains(ansi.Strip(got), "copied") {
		t.Fatalf("copy status = %q, want copied status", got)
	}
}
