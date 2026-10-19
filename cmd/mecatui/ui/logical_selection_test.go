package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestAssistantWordSelectionSurvivesWidthReflow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expand bool
		region regionKind
		word   string
		width  int
	}{
		{"comfort answer", false, conversationRegionBody, "ANSWERWORD", 40},
		{"detail answer", true, conversationRegionBody, "ANSWERWORD", 40},
		{"detail reasoning", true, conversationRegionReasoning, "REASONINGWORD", 40},
		{"wide comfort answer", false, conversationRegionBody, "ANSWERWORD", 100},
		{"wide detail answer", true, conversationRegionBody, "ANSWERWORD", 100},
		{"wide detail reasoning", true, conversationRegionReasoning, "REASONINGWORD", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scenario2Model(t, tc.width, 60, false, phaseIdle, nil)
			m.expandConversation = tc.expand
			m.conv.appendReasoning(strings.Repeat("Several words precede the selected REASONINGWORD with enough following text to wrap across multiple rows.\n\n", 2))
			m.conv.appendAssistant(strings.Repeat("Several words precede the selected ANSWERWORD with enough following text to wrap across multiple rows.\n\n", 2))
			m.conv.endReasoningStream()
			m.refreshView()
			// Select the last occurrence so identical context cannot rebind to the first.
			content := m.vp.GetContent()
			index := strings.LastIndex(content, tc.word)
			if index < 0 {
				t.Fatal("fixture must expose the selected word")
			}
			line := strings.Count(content[:index], "\n")
			if m.conversationView.frame.provenance[line].region != tc.region {
				t.Fatal("fixture must expose the selected word in its assistant region")
			}
			plain := ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line])
			m = m.wordSelect(line, strings.Index(plain, tc.word))
			initialRows := len(m.conversationView.frame.lines)
			narrowRows := 0
			for _, width := range []int{40, 100, 40} {
				m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 60})
				rows := len(m.conversationView.frame.lines)
				if narrowRows == 0 {
					narrowRows = rows
				}
				if (width == 100 && rows >= narrowRows) || (width == 40 && rows != narrowRows) ||
					(tc.width == 100 && width == 40 && rows <= initialRows) {
					t.Fatalf("width %d did not reflow the assistant: rows=%d, narrow=%d", width, rows, narrowRows)
				}
				if !m.sel.active || m.sel.anchorPoint.region != tc.region || m.sel.copied != tc.word ||
					selectedConversationText(m.conversationView.frame, m.vp.GetContent(), m.sel) != tc.word {
					t.Fatalf("width %d lost semantic word selection: %+v", width, m.sel)
				}
				content = m.vp.GetContent()
				lastWord := strings.LastIndex(content, tc.word)
				if lastWord < 0 || m.sel.anchorL != strings.Count(content[:lastWord], "\n") {
					t.Fatalf("width %d rebound selection to a different occurrence", width)
				}
				copied, cmd := m.copySelection()
				m = copied.(Model)
				if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != tc.word {
					t.Fatalf("width %d clipboard = %q, ok=%v, want %q without rail", width, got, ok, tc.word)
				}
			}
		})
	}
}

func TestAssistantSelectionRejectsMutationAndAmbiguity(t *testing.T) {
	for _, region := range []regionKind{conversationRegionBody, conversationRegionReasoning} {
		m := scenario2Model(t, 100, 60, false, phaseIdle, nil)
		m.expandConversation = true
		text := strings.Repeat("before target with after\n\n", 2)
		m.conv.appendReasoning(text)
		m.conv.appendAssistant(text)
		m.conv.endReasoningStream()
		m.refreshView()
		frame := m.conversationView.frame
		line := -1
		for i, row := range frame.provenance {
			if row.region == region && row.text && strings.Contains(ansi.Strip(frame.lines[i]), "target with") {
				line = i // last occurrence, with identical regional context
			}
		}
		if line < 0 {
			t.Fatal("fixture must expose the selected phrase")
		}
		plain := ansi.Strip(frame.lines[line])
		col := strings.Index(plain, "target with")
		m = m.wordSelect(line, col)
		m.sel.headC += len(" with")
		snapshotSelection(&m)
		if m.sel.copied != "target with" || !m.sel.resolveLogical(frame) {
			t.Fatal("unchanged phrase must resolve before mutation")
		}
		for _, mutation := range []struct{ old, replacement string }{
			{"target", "TARGET"},
			{"before", "BEFORE"},
			{"target with", "target  with"},
		} {
			changed := frame
			changed.lines = append([]string(nil), frame.lines...)
			changed.lines[line] = strings.Replace(plain, mutation.old, mutation.replacement, 1)
			selected := m.sel
			if mutation.replacement == "target  with" {
				// Normalized endpoints still match, but copied whitespace changed.
				for _, point := range []selectionPoint{selected.anchorPoint, selected.headPoint} {
					if _, _, ok := resolveSelectionPoint(changed, point); !ok {
						t.Fatal("whitespace mutation must exercise the exact-copy guard")
					}
				}
			}
			if selected.resolveLogical(changed) {
				t.Fatalf("region %v selection survived mutation %q", region, mutation.replacement)
			}
		}
		ambiguous := frame
		ambiguous.lines = append([]string(nil), frame.lines...)
		ambiguous.lines[line] = strings.Replace(plain, "target with", "target    with", 1)
		point, ok := selectionPointFor(ambiguous, line, col+len("target")+1)
		if !ok {
			t.Fatal("fixture must expose an interior whitespace endpoint")
		}
		if _, _, ok := resolveSelectionPoint(ambiguous, point); ok {
			t.Fatalf("region %v ambiguous normalized whitespace endpoint must fail closed", region)
		}
	}
}

func TestAssistantRailSelectionCopyAndDetailToggle(t *testing.T) {
	for _, expand := range []bool{false, true} {
		m := scenario2Model(t, 100, 30, false, phaseIdle, nil)
		m.expandConversation = expand
		m.conv.appendReasoning("summary detail")
		m.conv.appendAssistant("first answer paragraph\n\nsecond answer paragraph")
		m.conv.endReasoningStream()
		m.refreshView()
		first := lineIndexContaining(m.vp.GetContent(), "first answer paragraph")
		m = m.lineSelect(first)
		if m.sel.anchorPoint.blockID == 0 || m.sel.copied != "first answer paragraph" {
			t.Fatalf("line selection lacks semantic bounds: %+v", m.sel)
		}
		m = applyAll(m, f9)
		if !m.sel.active || m.sel.copied != "first answer paragraph" {
			t.Fatal("detail toggle lost assistant line selection")
		}
		first = lineIndexContaining(m.vp.GetContent(), "first answer paragraph")
		last := lineIndexContaining(m.vp.GetContent(), "second answer paragraph")
		lines := strings.Split(m.vp.GetContent(), "\n")
		m.sel = selection{active: true, anchorL: first, anchorC: convBodyXOffset, headL: last, headC: convBodyXOffset + len("second answer paragraph")}
		m.selBase = ""
		snapshotSelection(&m)
		const want = "first answer paragraph\n\nsecond answer paragraph"
		if m.sel.copied != want || m.sel.anchorPoint.sourceOffset != 0 {
			t.Fatalf("multi-line semantic selection = %q, anchor %+v", m.sel.copied, m.sel.anchorPoint)
		}
		_, cmd := m.copySelection()
		if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != want {
			t.Fatalf("clipboard = %q, ok=%v, want %q", got, ok, want)
		}
		// A drag starting on the rail takes the non-semantic fallback, but must
		// still omit the rail from the clipboard and survive an unchanged frame.
		m.sel = selection{active: true, anchorL: first, anchorC: 0, headL: last, headC: graphemeCount(ansi.Strip(lines[last]))}
		m.selBase = ""
		snapshotSelection(&m)
		m.refreshView()
		if !m.sel.active || m.sel.copied != want {
			t.Fatalf("rail-origin drag = %q, active=%v", m.sel.copied, m.sel.active)
		}
	}
}

// TestSelectionPreservesLiveStableText pins that a selection
// is logical text, not a rendered row range. Reflow and a pending stream delta
// must retain its exact ANSI-free copy while its context still resolves. A pending
// delta never changes selection/copy's visible frame before its render tick.
func TestSelectionPreservesLiveStableText(t *testing.T) {
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

	// The delta is deliberately left pending: copy must use the displayed frame,
	// not synchronously render the hidden conversation update.
	visibleContent := m.vp.GetContent()
	visibleFrame := strings.Join(m.conversationView.frame.lines, "\n")
	m.phase = phaseRunning
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\npending tail"})
	if !m.viewDirty {
		t.Fatal("precondition: delta must be pending before the render tick")
	}
	copied, cmd := m.copySelection()
	m = copied.(Model)
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
		t.Fatalf("pending-delta copied payload = %q (ok=%v), want %q", got, ok, marker)
	}
	if !m.viewDirty {
		t.Fatal("copy must leave the pending delta for its ordinary render tick")
	}
	if got := m.vp.GetContent(); got != visibleContent {
		t.Fatalf("copy replaced visible viewport while delta was pending:\n got %q\nwant %q", got, visibleContent)
	}
	if got := strings.Join(m.conversationView.frame.lines, "\n"); got != visibleFrame {
		t.Fatalf("copy replaced visible frame while delta was pending:\n got %q\nwant %q", got, visibleFrame)
	}
	if strings.Contains(ansi.Strip(m.vp.GetContent()), "pending tail") {
		t.Fatal("copy exposed hidden pending conversation content")
	}

	// The ordinary render resolves the visible selection against the newer frame.
	m.refreshView()
	if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != marker {
		t.Fatalf("stable selection after pending-delta render = %q (active=%t)", selectedText(m.vp.GetContent(), m.sel), m.sel.active)
	}
	// Expanding an earlier card changes the frame above the selected text.
	m.expandConversation = true
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

	// A pending delta must leave the displayed complete frame available to a
	// selection gesture. The later ordinary render proves the logical selection.
	t.Run("dirty appendix uses the displayed frame", func(t *testing.T) {
		const path = "DIRTYAPPENDIXMARKER.go"
		m, _ := selModel(t)
		m.conv.addTool("edit", "Edit", `{"path":"`+path+`","old_string":"a","new_string":"b"}`)
		m.conv.resolveTool("edit", "done", false)
		m.conv.recordFileChange(path)
		m.expandConversation = true
		m.refreshView()
		line := -1
		for i, text := range strings.Split(m.vp.GetContent(), "\n") {
			if strings.Contains(ansi.Strip(text), path) {
				line = i
			}
		}
		if line < 0 {
			t.Fatal("precondition: changed-files appendix missing")
		}

		m.phase = phaseRunning
		m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "pending update"})
		if !m.viewDirty {
			t.Fatal("precondition: delta must be pending")
		}
		col := strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), "DIRTYAPPENDIXMARKER")
		m = m.wordSelect(line, col)
		if !m.viewDirty {
			t.Fatal("selection must leave the pending delta for its ordinary render tick")
		}
		if strings.Contains(ansi.Strip(m.vp.GetContent()), "pending update") {
			t.Fatalf("selection exposed hidden pending conversation content: %q", m.vp.GetContent())
		}
		if got := m.sel.anchorPoint.blockID; got != m.conv.testAppendixID() {
			t.Fatalf("appendix selection block = %d, want appendix ID %d", got, m.conv.testAppendixID())
		}
		if !m.conversationView.frame.hasRegion(m.conv.testAppendixID(), conversationRegionAppendix) {
			t.Fatal("displayed complete frame was not used for selection projection")
		}
		m.refreshView()
		if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != "DIRTYAPPENDIXMARKER" {
			t.Fatalf("appendix selection after later refresh = %q (active=%t)", selectedText(m.vp.GetContent(), m.sel), m.sel.active)
		}
	})

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

// TestSelectionWheelScrollReturnsToLiveTail drives the live Model's mouse sequence:
// drag a selection while at the tail, scroll away, then scroll back down. Selection
// styling is only a projection over the displayed content and must not prevent the
// viewport from returning to its current tail.
func TestSelectionWheelScrollReturnsToLiveTail(t *testing.T) {
	m, _ := selModel(t)
	bottom := m.vp.YOffset()
	top := convTopRow(m)
	if bottom == 0 || m.vp.Height() < 2 {
		t.Fatalf("precondition: overflow is required (offset=%d height=%d)", bottom, m.vp.Height())
	}

	// A delta is pending when the user starts selecting. The click must use the
	// currently displayed frame; it may not publish this delta synchronously.
	visible := m.vp.GetContent()
	m.phase = phaseRunning
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\npending current tail"})
	if !m.viewDirty {
		t.Fatal("precondition: streamed tail must be pending")
	}

	// Start a non-empty drag in the tail's visible frame. Go through Update rather
	// than calling selection helpers directly, matching terminal mouse delivery.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 0, top+1)
	if !m.sel.active || m.sel.empty() {
		t.Fatal("precondition: drag should leave an active non-empty selection")
	}
	if got := ansi.Strip(m.vp.GetContent()); got != ansi.Strip(visible) {
		t.Fatalf("selection replaced displayed content while delta was pending:\n got %q\nwant %q", got, ansi.Strip(visible))
	}
	if strings.Contains(ansi.Strip(m.vp.GetContent()), "pending current tail") {
		t.Fatal("selection exposed hidden pending conversation content")
	}
	if !m.viewDirty {
		t.Fatal("selection cleared the ordinary render-tick obligation")
	}
	if got := m.vp.YOffset(); got != bottom {
		t.Fatalf("starting selection moved tail offset %d → %d", bottom, got)
	}

	// The ordinary render installs the newer tail while retaining the visible
	// selection's logical endpoints.
	m = applyAll(m, renderTickMsg{})
	if !strings.Contains(ansi.Strip(m.vp.GetContent()), "pending current tail") || !m.vp.AtBottom() {
		t.Fatal("render tick did not publish and follow the pending current tail")
	}
	bottom = m.vp.YOffset()

	m, _ = pressKey(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if got := m.vp.YOffset(); got >= bottom {
		t.Fatalf("wheel up did not leave tail: offset=%d, tail=%d", got, bottom)
	}
	if !m.sel.active {
		t.Fatal("wheel scroll unexpectedly cleared active selection")
	}

	for !m.vp.AtBottom() {
		m, _ = pressKey(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	if got := m.vp.YOffset(); got != bottom {
		t.Errorf("wheel down returned to offset %d, want current tail %d", got, bottom)
	}
	if m.conversationView.mode != followTail {
		t.Error("returning to tail must restore follow mode")
	}
	if !m.sel.active {
		t.Error("wheel scrolling back to tail must retain active selection")
	}
}

// TestSelectionReleaseAndReplacementKeepTheCompleteViewport exercises the reported
// mouse sequence against the real viewport, rather than inferring correctness from
// its offset. A completed selection remains visible for copying; a new drag during
// a coalesced stream update must retain that complete displayed frame until the
// ordinary render tick publishes the new complete projection.
func TestSelectionReleaseAndReplacementKeepTheCompleteViewport(t *testing.T) {
	const initialTail = "SELECTION_INITIAL_FULL_TAIL"
	const streamedTail = "SELECTION_STREAMED_FULL_TAIL"
	m, _ := selModel(t)
	m.conv.appendAssistant("\n" + initialTail)
	m.refreshView()
	initial := ansi.Strip(m.vp.GetContent())
	if !strings.Contains(initial, initialTail) {
		t.Fatal("precondition: initial tail missing")
	}

	top := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 0, top+1)
	if got := ansi.Strip(m.vp.GetContent()); got != initial {
		t.Fatalf("mouse-down changed complete viewport:\n got %q\nwant %q", got, initial)
	}
	m, _ = releaseMouse(m, 0, top+1)
	if !m.sel.active {
		t.Fatal("precondition: completed selection should remain available for copying")
	}
	if got := ansi.Strip(m.vp.GetContent()); got != initial {
		t.Fatalf("release changed complete viewport:\n got %q\nwant %q", got, initial)
	}

	// Leave a delta pending, then begin another selection while the completed one is
	// still active. The press must retain the displayed complete frame; the ordinary
	// render tick must then publish the complete streamed tail.
	m.phase = phaseRunning
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "\n" + streamedTail})
	if !m.viewDirty {
		t.Fatal("precondition: streamed delta must be pending")
	}
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 0, top+1)
	if got := ansi.Strip(m.vp.GetContent()); got != initial {
		t.Fatalf("replacement mouse-down replaced the displayed frame:\n got %q\nwant %q", got, initial)
	}
	m, _ = releaseMouse(m, 0, top+1)
	if got := ansi.Strip(m.vp.GetContent()); got != initial {
		t.Fatalf("replacement release replaced the displayed frame:\n got %q\nwant %q", got, initial)
	}
	m = applyAll(m, renderTickMsg{})
	expected := m.conversationContent()
	if got := ansi.Strip(m.vp.GetContent()); got != ansi.Strip(expected) {
		t.Fatalf("streamed refresh installed incomplete viewport:\n got %q\nwant %q", got, ansi.Strip(expected))
	}
	if got := ansi.Strip(m.vp.GetContent()); !strings.Contains(got, initialTail) || !strings.Contains(got, streamedTail) {
		t.Fatalf("streamed refresh omitted expected tail:\n%q", got)
	}
}

// TestEmptyReleaseDropsSelectionBaseBeforeTheNextProjection reproduces the stale
// selection-base sequence: the completed selection is cleared by an ordinary click,
// a later frame grows, and the next press must retain that complete current frame.
func TestEmptyReleaseDropsSelectionBaseBeforeTheNextProjection(t *testing.T) {
	const liveTail = "SELECTION_CURRENT_TAIL_MUST_REMAIN_REACHABLE"
	m, _ := selModel(t)
	top := convTopRow(m)

	// Complete a real drag so selBase holds the old selected projection.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	m, _ = motionMouse(m, 0, top+1)
	m, _ = releaseMouse(m, 0, top+1)
	if !m.sel.active || m.selBase == "" {
		t.Fatal("precondition: completed selection must retain its projection base")
	}

	// A normal empty click/release clears selection without resetting click-count
	// state. Its old projection must not survive into a later fresh press.
	m, _ = pressMouse(m, tea.MouseLeft, 5, top)
	m, _ = releaseMouse(m, 5, top)
	if m.sel.active {
		t.Fatal("empty click/release must clear the selection")
	}
	if m.clickCount != 1 {
		t.Fatalf("empty release changed click count to %d, want 1", m.clickCount)
	}
	if m.selBase != "" {
		t.Fatal("empty click/release retained stale selection projection")
	}

	m.conv.appendAssistant("\n" + liveTail)
	m.refreshView()
	want := m.conversationContent()
	if got := m.vp.GetContent(); got != want {
		t.Fatalf("precondition: current viewport content differs from its full projection")
	}

	// A different logical point is a fresh single click, exercising snapshotSelection.
	m, _ = pressMouse(m, tea.MouseLeft, 0, top)
	if got := m.vp.GetContent(); got != want {
		t.Fatalf("fresh click replaced current viewport with stale selection base:\n got %q\nwant %q", got, want)
	}
	m.vp.GotoBottom()
	if got := ansi.Strip(m.vp.View()); !strings.Contains(got, liveTail) {
		t.Fatalf("current tail is not reachable after fresh click: %q", got)
	}
}

// TestLogicalConversationAnchors_Scenario1_ChangingFrame proves that the live
// Model event and mouse-input paths preserve a selected subagent-card goal when
// a live SubagentTool event changes that card's rendered frame.
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

	// This changes the card through the live subagent event path, rather than an
	// unrelated conversation mutation.
	before := m.vp.GetContent()
	m = applyAll(m, client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: "parent", ChildID: "child-1",
		ToolName: "Read", InnerKind: "tool.call", Detail: `{"path":"auth.go"}`, ToolCount: 1,
	})
	if got := m.vp.GetContent(); got == before || !strings.Contains(ansi.Strip(got), "Read") {
		t.Fatalf("subagent tool event did not change the card:\n%s", ansi.Strip(got))
	}
	if !m.sel.active {
		t.Fatal("stable subagent-card selection was cleared by a live subagent event")
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

	t.Run("wrapped tool endpoints use canonical coordinates", func(t *testing.T) {
		const marker = "WRAPPEDTOOLMARKER"
		m, _ := selModel(t)
		m = applyAll(m, tea.WindowSizeMsg{Width: 34, Height: 30})
		m.conv.addTool("read", "Read", `{"path":"a/very/long/path/for/wrapping.txt"}`)
		m.conv.resolveAvailableTool("read", marker+" survives a narrow tool card reflow", false)
		m.expandConversation = true
		m.refreshView()
		line := lineIndexContaining(m.vp.GetContent(), marker)
		if line < 0 {
			t.Fatal("precondition: wrapped tool marker missing")
		}
		col := strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker)
		m = m.wordSelect(line, col)
		if got, want := m.sel.anchorPoint.sourceOffset, m.conversationView.frame.provenance[line].sourceOffset; got != want {
			t.Fatalf("tool selection source offset = %d, want canonical frame offset %d", got, want)
		}
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
		if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != marker {
			t.Fatalf("tool selection after reflow = %q (active=%t), want %q", selectedText(m.vp.GetContent(), m.sel), m.sel.active, marker)
		}
	})
}
