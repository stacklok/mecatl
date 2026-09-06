package ui

import (
	"testing"

	"charm.land/bubbles/v2/viewport"
)

func anchorFrame(rows ...renderedRow) renderedFrame {
	return renderedFrame{lines: make([]string, len(rows)), provenance: rows}
}

func TestADR_0301_AnchorFallbackIsDeterministic(t *testing.T) {
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
	if got := view.restore(collapsed, readingAnchor{blockID: 9, region: conversationRegionAppendix, bias: towardStart}); got != 2 {
		t.Errorf("collapsed appendix fallback row = %d, want preceding conversation row 2", got)
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
