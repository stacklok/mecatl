package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
)

func anchorFrame(rows ...renderedRow) renderedFrame {
	return renderedFrame{lines: make([]string, len(rows)), provenance: rows}
}

func TestADR_0301_AnchorFallbackIsDeterministic(t *testing.T) {
	// The old frame remains authoritative until refreshView captures its visible
	// anchor, even though rendering the replacement reuses scratch backing.
	m := newCoalesceModel(t)
	m.conv.addUser("first")
	m.conv.addUser("second")
	m.conv.addUser("third")
	m.refreshView()
	priorID := m.conv.blocks[0].id
	priorRow := m.view.frame.firstRegionRow(priorID, conversationRegionBody)
	if priorRow < 0 {
		t.Fatal("first block has no body row")
	}
	m.vp.SetHeight(1)
	m.vp.SetYOffset(priorRow)
	m.view.mode = anchored
	// Simulate a reconstructed document rendered through the same scratch-backed
	// renderer while the prior viewport is still visible.
	m.conv.blocks[0].id = 99
	m.conv.blocks[0].rev++
	m.refreshView()
	if got := m.view.anchor.blockID; got != priorID {
		t.Fatalf("captured anchor block ID = %d, want prior viewport block ID %d", got, priorID)
	}

	frame := anchorFrame(
		renderedRow{blockID: 1, region: conversationRegionBody, sourceOffset: 0, text: true},
		renderedRow{blockID: 1, region: conversationRegionBody, sourceOffset: 5, text: true},
		renderedRow{blockID: 1, region: conversationRegionChrome, row: 0},
		renderedRow{blockID: 3, region: conversationRegionBody, sourceOffset: 0, text: true},
	)
	view := conversationView{}

	if got := view.restore(frame, readingAnchor{blockID: 1, region: conversationRegionBody, sourceOffset: 5, text: true}); got != 1 {
		t.Fatalf("exact text anchor row = %d, want 1", got)
	}
	if got := view.restore(frame, readingAnchor{blockID: 1, region: conversationRegionBody, sourceOffset: 4, text: true, bias: towardStart}); got != 0 {
		t.Errorf("start-biased same-region fallback row = %d, want 0", got)
	}
	if got := view.restore(frame, readingAnchor{blockID: 1, region: conversationRegionBody, sourceOffset: 4, text: true, bias: towardEnd}); got != 1 {
		t.Errorf("end-biased same-region fallback row = %d, want 1", got)
	}
	if got := view.restore(frame, readingAnchor{blockID: 1, region: conversationRegionResult, bias: towardStart}); got != 0 {
		t.Errorf("same-block region fallback row = %d, want body row 0", got)
	}
	if got := view.restore(frame, readingAnchor{blockID: 2, region: conversationRegionResult, bias: towardStart}); got != 2 {
		t.Errorf("start-biased adjacent-block fallback row = %d, want 2", got)
	}
	if got := view.restore(frame, readingAnchor{blockID: 2, region: conversationRegionResult, bias: towardEnd}); got != 3 {
		t.Errorf("end-biased adjacent-block fallback row = %d, want 3", got)
	}

	// Tool-card provenance follows the rendered semantic sections, not a fraction
	// of their total rows: a wrapped argument section can be much longer than its
	// one-line result.
	c := &conversation{}
	c.addTool("call-1", "Read", `{"argument_one":"one","argument_two":"two","argument_three":"three","argument_four":"four"}`)
	c.resolveTool("call-1", "RESULT_MARKER", false)
	r := newCacheRenderer()
	r.setWidth(32)
	toolFrame := r.renderConversationFrame(c, true)
	seenArguments, seenResult := false, false
	argumentRow := -1
	for i, line := range toolFrame.lines {
		plain := stripANSIstr(line)
		switch {
		case strings.Contains(plain, "argument_"):
			seenArguments = true
			if argumentRow < 0 {
				argumentRow = i
			}
			if got := toolFrame.provenance[i].region; got != conversationRegionArguments {
				t.Errorf("argument row %d has region %v, want arguments", i, got)
			}
		case strings.Contains(plain, "RESULT_MARKER"):
			seenResult = true
			if got := toolFrame.provenance[i].region; got != conversationRegionResult {
				t.Errorf("result row %d has region %v, want result", i, got)
			}
		}
	}
	if !seenArguments || !seenResult {
		t.Fatalf("semantic test did not find arguments=%t result=%t in frame: %q", seenArguments, seenResult, toolFrame.lines)
	}
	anchor, ok := toolFrame.anchorForRow(argumentRow)
	if !ok {
		t.Fatal("wrapped argument row did not produce an anchor")
	}
	r.setWidth(80)
	reflowed := r.renderConversationFrame(c, true)
	row, ok := reflowed.rowForAnchor(anchor)
	if !ok || reflowed.provenance[row].region != conversationRegionArguments {
		t.Fatalf("argument anchor after reflow = row %d, ok=%t, region=%v; want arguments", row, ok, reflowed.provenance[row].region)
	}
}

func TestADR_0301_CardAndChangedFilesAppendixFallback(t *testing.T) {
	collapsed := anchorFrame(
		renderedRow{blockID: 7, region: conversationRegionChrome, row: 0},
		renderedRow{blockID: 7, region: conversationRegionResult, sourceOffset: 0, text: true},
		renderedRow{blockID: 8, region: conversationRegionBody, sourceOffset: 0, text: true},
	)
	view := conversationView{}
	if got := view.restore(collapsed, readingAnchor{blockID: 7, region: conversationRegionArguments, sourceOffset: 8, text: true, bias: towardStart}); got != 1 {
		t.Errorf("collapsed card fallback row = %d, want result summary row 1", got)
	}

	// The appendix gets an ID when the first change is observed, but it always
	// renders physically last. Its ID can therefore precede a later conversation
	// block; collapse must use physical frame order, not allocation order.
	appendixCollapsed := anchorFrame(
		renderedRow{blockID: 1, region: conversationRegionBody, sourceOffset: 0, text: true},
		renderedRow{blockID: 3, region: conversationRegionBody, sourceOffset: 0, text: true},
	)
	if got := view.restore(appendixCollapsed, readingAnchor{blockID: 2, region: conversationRegionAppendix, bias: towardStart}); got != 1 {
		t.Errorf("collapsed appendix fallback row = %d, want physically preceding conversation row 1", got)
	}

	// A collapsed tool result remains in its result region even when the argument
	// section has a different wrapped height.
	c := &conversation{}
	c.addTool("call-1", "Read", `{"argument_one":"one","argument_two":"two","argument_three":"three","argument_four":"four"}`)
	c.resolveTool("call-1", "RESULT_MARKER", false)
	r := newCacheRenderer()
	r.setWidth(80)
	frame := r.renderConversationFrame(c, false)
	resultRow := -1
	for i, line := range frame.lines {
		if strings.Contains(stripANSIstr(line), "RESULT_MARKER") {
			resultRow = i
			break
		}
	}
	if resultRow < 0 {
		t.Fatal("collapsed card has no visible result row")
	}
	anchor, ok := frame.anchorForRow(resultRow)
	if !ok || anchor.region != conversationRegionResult {
		t.Fatalf("collapsed result anchor = %#v, ok=%t; want result region", anchor, ok)
	}
	if got := view.restore(frame, anchor); got != resultRow {
		t.Errorf("collapsed result anchor restores row %d, want %d", got, resultRow)
	}
}

func TestADR_0301_BottomAlignedAnchorPromotesTailFollow(t *testing.T) {
	vp := viewport.New()
	vp.SetHeight(2)
	view := conversationView{mode: anchored, anchor: readingAnchor{blockID: 2, region: conversationRegionBody, sourceOffset: 0, text: true}}
	frame := anchorFrame(
		renderedRow{blockID: 1, region: conversationRegionBody, sourceOffset: 0, text: true},
		renderedRow{blockID: 2, region: conversationRegionBody, sourceOffset: 0, text: true},
		renderedRow{blockID: 3, region: conversationRegionBody, sourceOffset: 0, text: true},
	)

	view.replace(&vp, frame)
	if view.mode != followTail || !vp.AtBottom() {
		t.Fatalf("bottom-aligned restoration = mode %v atBottom %v, want tail follow at bottom", view.mode, vp.AtBottom())
	}

	vp.ScrollUp(1)
	view.observe(vp)
	if view.mode != anchored {
		t.Errorf("scrolling above bottom mode = %v, want anchored", view.mode)
	}
}
