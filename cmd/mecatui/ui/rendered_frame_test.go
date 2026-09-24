package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"

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
	c.resolveTool("call-1", "package main", false, client.ContentBlock{
		Kind: client.ContentBlockResourceLink,
		Name: "rendered artifact",
		URL:  "file:///workspace/main.go",
	})
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
	for i, line := range frame.lines {
		if strings.Contains(stripANSIstr(line), "rendered artifact") && frame.provenance[i].region != conversationRegionResult {
			t.Fatalf("artifact row %d has region %v, want enclosing tool result", i, frame.provenance[i].region)
		}
	}
	if frame.appendixID != c.changedFilesAppendixID() {
		t.Fatalf("expanded appendix ID = %d, want %d", frame.appendixID, c.changedFilesAppendixID())
	}
}

// TestExpandedReasoningProvenanceUsesWrappedRows pins the narrow-width path where
// the caveat and reasoning body wrap into more rows than the unwrapped source.
func TestExpandedReasoningProvenanceUsesWrappedRows(t *testing.T) {
	c := &conversation{}
	c.startAssistant()
	c.appendReasoning("first deliberately long reasoning sentence wraps across several visible rows")
	c.appendAssistant("BODY-START appears only after the reasoning summary")

	r := newCacheRenderer()
	r.setWidth(24)
	frame := r.renderConversationFrame(c, true)
	if got, want := len(frame.provenance), len(frame.lines); got != want {
		t.Fatalf("provenance rows = %d, rendered lines = %d", got, want)
	}

	assistant := &c.blocks[0]
	wantReasoningRows := len(strings.Split(r.renderReasoning(assistant, true), "\n"))
	if wantReasoningRows <= 3 {
		t.Fatalf("narrow reasoning render has %d rows, want wrapping beyond the unwrapped rows", wantReasoningRows)
	}
	firstReasoning := frame.firstRegionRow(assistant.id, conversationRegionReasoning)
	if firstReasoning < 0 {
		t.Fatal("expanded reasoning rows missing from provenance")
	}
	for i := 0; i < wantReasoningRows; i++ {
		if got := frame.provenance[firstReasoning+i].region; got != conversationRegionReasoning {
			t.Fatalf("reasoning render row %d has region %v, want reasoning", i, got)
		}
	}
	firstBody := firstReasoning + wantReasoningRows
	if got := frame.provenance[firstBody].region; got != conversationRegionBody {
		t.Fatalf("row after all %d rendered reasoning rows has region %v, want body", wantReasoningRows, got)
	}
	if got := stripANSIstr(frame.lines[firstBody]); !strings.Contains(got, "BODY-START") {
		t.Fatalf("first body row = %q, want BODY-START", got)
	}
}

// TestExpandedReasoningAnchorSurvivesReflow keeps the visible reasoning text at
// its logical grapheme location while leaving the header and caveat as chrome.
func TestExpandedReasoningAnchorSurvivesReflow(t *testing.T) {
	c := &conversation{}
	c.startAssistant()
	c.appendReasoning("first reasoning detail wraps before the REASONING-ANCHOR semantic text and continues after it")
	c.appendAssistant("answer body keeps the viewport from following the tail")

	r := newCacheRenderer()
	r.setWidth(30)
	narrow := r.renderConversationFrame(c, true)
	assistant := c.blocks[0]
	anchorRow := -1
	for i, line := range narrow.lines {
		row := narrow.provenance[i]
		if row.blockID == assistant.id && row.region == conversationRegionReasoning && strings.Contains(stripANSIstr(line), "REASONING-ANCHOR") {
			if !row.text {
				t.Fatal("expanded reasoning text row must be text-bearing")
			}
			anchorRow = i
			break
		}
	}
	if anchorRow < 0 {
		t.Fatalf("reasoning anchor text not found in %q", narrow.lines)
	}
	for i, line := range narrow.lines {
		row := narrow.provenance[i]
		if row.blockID == assistant.id && row.region == conversationRegionReasoning &&
			(strings.Contains(stripANSIstr(line), "reasoning summary") || strings.Contains(stripANSIstr(line), reasoningCaveat)) && row.text {
			t.Fatalf("reasoning presentation row %q must not be text-bearing", line)
		}
	}

	vp := viewport.New()
	vp.SetHeight(1)
	vp.SetContentLines(narrow.lines)
	vp.SetYOffset(anchorRow)
	view := conversationView{mode: anchored, frame: narrow}
	r.setWidth(100)
	wide := r.renderConversationFrame(c, true)
	view.replace(&vp, wide)

	row := wide.provenance[vp.YOffset()]
	if row.region != conversationRegionReasoning || !row.text || !strings.Contains(stripANSIstr(wide.lines[vp.YOffset()]), "REASONING-ANCHOR") {
		t.Fatalf("restored row = %#v %q, want text-bearing reasoning row containing anchor", row, wide.lines[vp.YOffset()])
	}
}

// TestToolCardPreparationIsSharedWithFrameProvenance pins phase 1 of the tool-card
// repair: a matching block cache entry owns the one prepared card used for both
// its rendered output and frame provenance. The same cache axes must invalidate it.
func TestToolCardPreparationIsSharedWithFrameProvenance(t *testing.T) {
	c := &conversation{}
	c.addTool("call", "Read", `{"path":"main.go"}`)
	r := newCacheRenderer()

	assertFrame := func(step string, wantPrepares int) {
		t.Helper()
		got := r.renderConversationFrame(c, false)
		if r.toolCardPrepares != wantPrepares {
			t.Fatalf("%s: prepareToolCard calls = %d, want %d", step, r.toolCardPrepares, wantPrepares)
		}
		entry, ok := r.blocks.rendered[0]
		if !ok || len(entry.rows) == 0 {
			t.Fatalf("%s: matching tool block cache entry did not retain structural rows", step)
		}

		fresh := newCacheRenderer()
		fresh.setWidth(r.width)
		want := fresh.renderConversationFrame(c, false)
		if strings.Join(got.lines, "\n") != strings.Join(want.lines, "\n") {
			t.Fatalf("%s: cached frame lines diverged from fresh render", step)
		}
		if !slices.Equal(got.provenance, want.provenance) {
			t.Fatalf("%s: cached frame provenance diverged from fresh render", step)
		}
	}

	assertFrame("fresh", 1)
	assertFrame("steady cache hit", 1)
	if !c.resolveTool("call", "package main", false) {
		t.Fatal("resolveTool failed")
	}
	assertFrame("tool mutation", 2)
	r.setWidth(48)
	assertFrame("resize", 3)
	// render at the changed expand axis directly so it covers the block-cache key.
	frame := r.renderConversationFrame(c, true)
	if r.toolCardPrepares != 4 {
		t.Fatalf("expand change: prepareToolCard calls = %d, want 4", r.toolCardPrepares)
	}
	fresh := newCacheRenderer()
	fresh.setWidth(r.width)
	want := fresh.renderConversationFrame(c, true)
	if strings.Join(frame.lines, "\n") != strings.Join(want.lines, "\n") || !slices.Equal(frame.provenance, want.provenance) {
		t.Fatal("expand change: cached frame diverged from fresh render")
	}
}

// TestToolCardStructuralProvenanceSurvivesCacheReplacementAndReflow pins that tool
// rows retain only structural spans. Canonical text is recovered from the existing
// rendered line, never by parsing card decoration or retaining prepared sections.
func TestToolCardStructuralProvenanceSurvivesCacheReplacementAndReflow(t *testing.T) {
	c := &conversation{}
	c.addTool("call", "Read", `{"path":"TOOLARGMARKER deliberately wraps across the card"}`)
	c.resolveTool("call", "TOOLRESULTMARKER deliberately wraps across the card   ", false)
	r := newCacheRenderer()
	r.setWidth(32)
	narrow := r.renderConversationFrame(c, true)

	for _, typ := range []reflect.Type{reflect.TypeOf(renderedRow{}), reflect.TypeOf(blockEntry{}), reflect.TypeOf(renderedFrame{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type == reflect.TypeOf((*preparedToolCard)(nil)) || field.Type == reflect.TypeOf(preparedToolCard{}) {
				t.Fatalf("%s retains prepared tool-card text through %q", typ.Name(), field.Name)
			}
		}
	}
	for i := 0; i < reflect.TypeOf(renderedRow{}).NumField(); i++ {
		field := reflect.TypeOf(renderedRow{}).Field(i)
		if field.Type.Kind() == reflect.String {
			t.Fatalf("provenance retains text field %q", field.Name)
		}
	}

	var points []selectionPoint
	for _, tc := range []struct {
		region regionKind
		marker string
	}{
		{conversationRegionArguments, "TOOLARGMARKER"},
		{conversationRegionResult, "TOOLRESULTMARKER"},
	} {
		found := false
		for i, row := range narrow.provenance {
			if row.region != tc.region {
				continue
			}
			text, leading := selectionRowText(narrow, row, narrow.lines[i])
			if !strings.Contains(text, tc.marker) {
				continue
			}
			if row.span != graphemeCount(text) || row.span == 0 {
				t.Fatalf("%s row lacks structural span: %#v", tc.marker, row)
			}
			offset := strings.Index(text, tc.marker) + len("TOOL")
			point, ok := selectionPointFor(narrow, i, leading+offset)
			if !ok {
				t.Fatalf("%s selection point was rejected", tc.marker)
			}
			if point.region != tc.region || point.sourceOffset != row.sourceOffset+offset || point.before == "" || point.after == "" {
				t.Fatalf("%s point = %#v, row = %#v", tc.marker, point, row)
			}
			points = append(points, point)
			found = true
			break
		}
		if !found {
			t.Fatalf("%s row missing from frame", tc.marker)
		}
	}

	if len(points) != 2 {
		t.Fatalf("selection points = %d, want 2", len(points))
	}
	trailingSpacesRetained := false
	for i, row := range narrow.provenance {
		if row.region != conversationRegionResult {
			continue
		}
		text, _ := selectionRowText(narrow, row, narrow.lines[i])
		trailingSpacesRetained = trailingSpacesRetained || strings.HasSuffix(text, "   ")
	}
	if !trailingSpacesRetained {
		t.Fatal("tool result provenance lost trailing semantic spaces to card padding")
	}

	// This mirrors conversationView's visible-frame ownership: its copied rows and
	// existing rendered lines remain enough after the renderer cache is replaced.
	visible := narrow
	visible.provenance = append([]renderedRow(nil), narrow.provenance...)
	visible.lines = append([]string(nil), narrow.lines...)

	r.setWidth(64)
	wide := r.renderConversationFrame(c, true)
	for _, point := range points {
		if _, _, ok := resolveSelectionPoint(visible, point); !ok {
			t.Fatalf("old frame lost exact structural source for %#v", point)
		}
		if _, _, ok := resolveSelectionPoint(wide, point); !ok {
			t.Fatalf("wrapped point did not resolve after reflow: %#v", point)
		}
	}
}

// TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset pins canonical visible
// text offsets: a body anchor resolves to the same visible grapheme after a
// terminal-width reflow, rather than retaining an unstable wrapped row number.
func TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset(t *testing.T) {
	c := &conversation{}
	c.addUser("short")
	c.startAssistant()
	c.appendAssistant("one two three four five e\u0301 six seven eight nine ten eleven twelve")

	r := newCacheRenderer()
	r.setWidth(24)
	narrow := r.renderConversationFrame(c, false)
	var anchor readingAnchor
	found := false
	for i, line := range narrow.lines {
		if row := narrow.provenance[i]; row.blockID == c.blocks[1].id && row.region == conversationRegionBody && row.text && strings.Contains(stripANSIstr(line), "eight") {
			anchor, found = narrow.anchorForRow(i)
			break
		}
	}
	if !found {
		t.Fatalf("wrapped continuation containing eight not found in %q", narrow.lines)
	}
	if want := graphemeCount("one two three fourfive e\u0301 six seven"); anchor.sourceOffset != want {
		t.Fatalf("continuation source offset = %d, want canonical offset %d", anchor.sourceOffset, want)
	}

	r.setWidth(100)
	wide := r.renderConversationFrame(c, false)
	row, ok := wide.rowForAnchor(anchor)
	if !ok {
		t.Fatal("continuation anchor did not resolve after reflow")
	}
	if got := wide.provenance[row].sourceOffset; got > anchor.sourceOffset {
		t.Fatalf("reflow source offset = %d, must begin at or before canonical offset %d", got, anchor.sourceOffset)
	}
	if got := stripANSIstr(wide.lines[row]); !strings.Contains(got, "eight") {
		t.Fatalf("reflow restored row %q, want canonical continuation containing eight", got)
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
			if r.blocks.prefixN != len(c.blocks)-1 {
				t.Fatalf("cached prefix covers %d blocks, want settled prefix of %d", r.blocks.prefixN, len(c.blocks)-1)
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
	if got, want := len(m.conversationView.frame.provenance), len(m.conversationView.frame.lines); got != want {
		t.Fatalf("normal-path provenance rows = %d, lines = %d", got, want)
	}
}

// TestToolCardFrameHardwrapsLongCollapsedArgumentRowsRegression pins the
// review-batch-shaped collapsed card that previously left an unbroken argument
// row for lipgloss to wrap during decoration. That yielded more visible card
// rows than prepared provenance rows and panicked frame assembly.
func TestToolCardFrameHardwrapsLongCollapsedArgumentRowsRegression(t *testing.T) {
	const argument = "review_batch_5d3f2bc9d6204a508d6b79a8b2e3f1c4"

	c := &conversation{}
	c.addTool("review-batch", "review-batch", `{"`+argument+`":"ready"}`)
	r := newCacheRenderer()
	r.setWidth(24)

	frame := r.renderConversationFrame(c, false)
	if got, want := len(frame.provenance), len(frame.lines); got != want {
		t.Fatalf("provenance rows = %d, rendered lines = %d", got, want)
	}

	offset := 0
	argumentRows := 0
	for i, row := range frame.provenance {
		if row.region != conversationRegionArguments {
			continue
		}
		if !row.text {
			t.Fatal("argument row is not semantic text")
		}
		text, _ := selectionRowText(frame, row, frame.lines[i])
		if row.sourceOffset != offset {
			t.Fatalf("argument source offset = %d, want %d for %q", row.sourceOffset, offset, text)
		}
		offset += graphemeCount(text)
		argumentRows++
	}
	if argumentRows < 2 {
		t.Fatalf("long collapsed argument rendered in %d rows, want hard-wrapped rows", argumentRows)
	}
}

// TestToolCardFrameProvenanceSurvivesNarrowResizeRegression covers the narrow
// card path where lipgloss reflows ANSI-styled card sections while applying the
// frame. Resizing through every practical terminal width must retain one semantic
// row per rendered line for both unresolved arguments and resolved card results.
func TestToolCardFrameProvenanceSurvivesNarrowResizeRegression(t *testing.T) {
	const (
		green = "\x1b[38;5;42m"
		blue  = "\x1b[38;5;39m"
		reset = "\x1b[0m"
	)

	c := &conversation{}
	c.addTool("unresolved", "Read", `{"path":"`+green+`UNRESOLVED-`+strings.Repeat("argument-", 12)+reset+`"}`)
	c.addTool("resolved", "Read", `{"path":"`+blue+`RESOLVED-`+strings.Repeat("argument-", 12)+reset+`"}`)
	c.resolveTool("resolved", blue+`RESOLVED-`+strings.Repeat("result ", 24)+reset, false)
	r := newCacheRenderer()

	widths := make([]int, 0, 124)
	for width := 1; width <= 120; width++ {
		widths = append(widths, width)
	}
	// Revisit both extremes after the contiguous sweep: cache reuse across repeated
	// narrow/wide transitions is the resize path that previously lost row metadata.
	widths = append(widths, 1, 120, 1, 120)
	for _, width := range widths {
		r.setWidth(width)
		frame := r.renderConversationFrame(c, false)
		if got, want := len(frame.provenance), len(frame.lines); got != want {
			t.Fatalf("width %d: provenance rows = %d, rendered lines = %d", width, got, want)
		}

		want := make([]string, 0, len(frame.lines))
		for i := range c.blocks {
			if i > 0 {
				for range blockBlankLinesAfter(c.blocks, i) {
					want = append(want, "")
				}
			}
			want = append(want, strings.Split(r.renderBlock(i, &c.blocks[i], false), "\n")...)
		}
		want = append(want, "")
		if !slices.Equal(frame.lines, want) {
			t.Fatalf("width %d: frame changed rendered bytes\n got: %q\nwant: %q", width, frame.lines, want)
		}

		seen := map[uint64]map[regionKind]bool{}
		for row, provenance := range frame.provenance {
			if provenance.region == conversationRegionUnknown {
				t.Fatalf("width %d: line %d has no provenance", width, row)
			}
			if seen[provenance.blockID] == nil {
				seen[provenance.blockID] = map[regionKind]bool{}
			}
			seen[provenance.blockID][provenance.region] = true
		}
		for _, tc := range []struct {
			blockID uint64
			region  regionKind
			name    string
		}{
			{c.blocks[0].id, conversationRegionArguments, "unresolved arguments"},
			{c.blocks[1].id, conversationRegionArguments, "resolved arguments"},
			{c.blocks[1].id, conversationRegionResult, "resolved result"},
		} {
			if !seen[tc.blockID][tc.region] {
				t.Fatalf("width %d: %s has no provenance row", width, tc.name)
			}
		}
	}
}
