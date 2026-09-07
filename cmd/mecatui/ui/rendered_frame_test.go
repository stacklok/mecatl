package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestADR_0301_RenderedFrameProvenanceMatchesLines pins ADR 0301's requirement
// that the actual incremental scrollback renderer returns one provenance row per
// visible line without changing the viewport's existing bytes.
func TestADR_0301_RenderedFrameProvenanceMatchesLines(t *testing.T) {
	c := &conversation{}
	c.addUser("hello")
	c.startAssistant()
	c.appendReasoning("considering options")
	c.appendAssistant("a visible answer")
	c.addTool("call-1", "Read", `{"path":"main.go"}`)
	c.resolveTool("call-1", "package main", false)
	c.recordFileChange("main.go")

	r := newCacheRenderer()
	frame := r.renderConversationFrame(c, true)
	want := r.renderConversationLines(c, true)
	if len(frame.lines) != len(want) || strings.Join(frame.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("frame lines differ from incremental renderer\n got: %q\nwant: %q", frame.lines, want)
	}
	if len(frame.provenance) != len(frame.lines) {
		t.Fatalf("provenance rows = %d, lines = %d", len(frame.provenance), len(frame.lines))
	}
	for i, row := range frame.provenance {
		if row.region == conversationRegionUnknown {
			t.Errorf("row %d has no semantic region", i)
		}
	}
	if !frame.hasRegion(c.blocks[1].id, conversationRegionReasoning) || !frame.hasRegion(c.blocks[1].id, conversationRegionBody) {
		t.Fatal("assistant reasoning and body must retain distinct semantic regions")
	}
	if !frame.hasRegion(c.blocks[2].id, conversationRegionArguments) || !frame.hasRegion(c.blocks[2].id, conversationRegionResult) {
		t.Fatal("tool arguments and result must retain distinct semantic regions")
	}
	if frame.appendixID != c.changedFilesAppendixID {
		t.Fatalf("expanded appendix ID = %d, want %d", frame.appendixID, c.changedFilesAppendixID)
	}
}

// TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset pins canonical visible
// text offsets: a body anchor resolves to the same visible grapheme after a
// terminal-width reflow, rather than retaining an unstable wrapped row number.
func TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset(t *testing.T) {
	c := &conversation{}
	c.addUser("short")
	c.startAssistant()
	c.appendAssistant("one two three four five six seven eight nine ten eleven twelve")

	r := newCacheRenderer()
	r.setWidth(100)
	wide := r.renderConversationFrame(c, false)
	anchor, ok := wide.anchorForRow(wide.firstRegionRow(c.blocks[1].id, conversationRegionBody))
	if !ok {
		t.Fatal("body row did not produce a text anchor")
	}
	r.setWidth(24)
	narrow := r.renderConversationFrame(c, false)
	row, ok := narrow.rowForAnchor(anchor)
	if !ok {
		t.Fatal("body anchor did not resolve after reflow")
	}
	if got := narrow.provenance[row].sourceOffset; got != anchor.sourceOffset {
		t.Fatalf("reflow source offset = %d, want %d", got, anchor.sourceOffset)
	}
}

// TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly pins the normal
// collapsed streaming path: frame metadata tracks rows while cached rendered
// strings remain the renderer's sole transcript representation.
func TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly(t *testing.T) {
	for _, depth := range []int{64, 256} {
		t.Run("depth", func(t *testing.T) {
			c := &conversation{}
			for i := 0; i < depth; i++ {
				c.addUser("settled scrollback")
			}
			c.startAssistant()
			c.appendAssistant("stream")
			r := newCacheRenderer()
			first := r.renderConversationFrame(c, false)
			before := r.blockRenders

			c.appendAssistant(" delta")
			second := r.renderConversationFrame(c, false)
			if got := r.blockRenders - before; got != 1 {
				t.Fatalf("streaming frame re-rendered %d blocks at depth %d, want 1", got, depth)
			}
			if len(second.provenance) != len(second.lines) || len(first.provenance) == 0 {
				t.Fatal("frame provenance must be row-linear and present")
			}
			if r.joinPrefixN != len(c.blocks)-1 {
				t.Fatalf("cached prefix covers %d blocks, want settled prefix of %d", r.joinPrefixN, len(c.blocks)-1)
			}
		})
	}

	// A burst of real stream events still produces one frame-coalesced live-block
	// render after its explicit cadence tick, independent of scrollback depth.
	m := newCoalesceModel(t)
	for i := 0; i < 256; i++ {
		m.conv.addUser("settled scrollback")
	}
	m.refreshView()
	before := m.rend.blockRenders
	m = applyAll(m,
		client.AssistantDeltaMsg{Turn: 1, Text: "first"},
		client.AssistantDeltaMsg{Turn: 1, Text: " second"},
		renderTickMsg{},
	)
	if got := m.rend.blockRenders - before; got != 1 {
		t.Fatalf("coalesced frame re-rendered %d blocks, want 1", got)
	}
	if got, want := len(m.view.frame.provenance), len(m.view.frame.lines); got != want {
		t.Fatalf("normal-path provenance rows = %d, lines = %d", got, want)
	}
}
