package ui

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func scenario3Conversation() *conversation {
	c := &conversation{}
	c.addUserWithMedia("functional user card", []string{"image/png (inline)"})
	c.addNotice("functional notice card")
	c.addHook("rewrote arguments", "PreToolUse", "Shell", "modified")
	c.addError("transient failure")
	c.addPermanentError("permanent failure")
	c.addDelivery("nightly", "fire-3", "delivered")
	c.addTurnStat("↑10 ↓2 · 1s")
	c.addTool("call-3", "Read", `{"path":"scenario-three.txt"}`)
	c.resolveTool("call-3", "scenario three result", false)
	return c
}

func TestMecatuiFunctionalConversationCards_Scenario3_RevisionGuardAvoidsSettledCardRehash(t *testing.T) {
	c := scenario3Conversation()
	r := newCacheRenderer()
	r.renderConversationFrame(c, false)
	prepares := r.cardPrepares
	keys := make([][32]byte, len(c.blocks))
	for i := range c.blocks {
		keys[i] = r.blockCache[i].cardKey
		if keys[i] == ([32]byte{}) {
			t.Fatalf("block %d did not retain its canonical prepared identity", i)
		}
	}

	r.renderConversationFrame(c, false)
	if r.cardPrepares != prepares {
		t.Fatalf("settled frame prepared cards: %d → %d", prepares, r.cardPrepares)
	}
	for i, key := range keys {
		if got := r.blockCache[i].cardKey; got != key {
			t.Fatalf("settled block %d identity changed", i)
		}
	}

	c.blocks[1].raw = "changed notice"
	c.blocks[1].rev++ // model a conversation mutation gateway for this focused cache test.
	r.renderConversationFrame(c, false)
	if got := r.cardPrepares; got != prepares+1 {
		t.Fatalf("one changed generation prepared %d cards, want one", got-prepares)
	}
	if r.blockCache[1].cardKey == keys[1] {
		t.Fatal("changed card retained its old prepared identity")
	}
}

func TestMecatuiFunctionalConversationCards_Scenario3_FrameProvenanceMatchesPreparedRows(t *testing.T) {
	c := scenario3Conversation()
	r := newCacheRenderer()
	frame := r.renderConversationFrame(c, true)
	if len(frame.lines) != len(frame.provenance) {
		t.Fatalf("frame lines/provenance = %d/%d", len(frame.lines), len(frame.provenance))
	}
	for i := range c.blocks {
		entry := r.blockCache[i]
		if len(strings.Split(entry.out, "\n")) != len(entry.rows) {
			t.Fatalf("block %d cached lines/rows are not lockstep", i)
		}
		if _, ok := r.blockFrameCache[i]; ok {
			t.Fatalf("functional block %d grew a second frame-row cache", i)
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(renderedRow{}), reflect.TypeOf(renderedFrame{})} {
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Type.Kind() == reflect.String {
				t.Fatalf("%s retains semantic text in %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario3_AnchorsAndSelectionSurviveCardReflow(t *testing.T) {
	c := &conversation{}
	c.addTool("call", "Read", `{"path":"ARGUMENT-MARKER-with-context.txt"}`)
	r := newCacheRenderer()
	r.setWidth(34)
	initial := r.renderConversationFrame(c, true)
	point := scenario3SelectionPoint(t, initial, conversationRegionArguments, "ARGUMENT-MARKER")

	c.addNotice("unrelated append")
	c.resolveTool("call", "late result", false)
	r.setWidth(58)
	reflowed := r.renderConversationFrame(c, true)
	if _, _, ok := resolveSelectionPoint(reflowed, point); !ok {
		t.Fatal("tool-argument selection did not retain exact context after append, result, and reflow")
	}
	anchor := readingAnchor{blockID: point.blockID, region: point.region, sourceOffset: point.sourceOffset, text: true}
	if _, ok := reflowed.rowForAnchor(anchor); !ok {
		t.Fatal("logical reading anchor did not survive card reflow")
	}

	collapsed := r.renderConversationFrame(c, false)
	if _, _, ok := resolveSelectionPoint(collapsed, point); ok {
		t.Fatal("selection survived after collapse hid its prepared argument row")
	}

	c.blocks[0].toolArgs = `{"path":"different.txt"}`
	c.blocks[0].rev++
	changed := r.renderConversationFrame(c, true)
	if _, _, ok := resolveSelectionPoint(changed, point); ok {
		t.Fatal("selection survived after its source changed")
	}
}

func scenario3SelectionPoint(t *testing.T, frame renderedFrame, region regionKind, marker string) selectionPoint {
	t.Helper()
	for i, row := range frame.provenance {
		if row.region != region {
			continue
		}
		text, leading := selectionRowText(frame, row, frame.lines[i])
		at := strings.Index(text, marker)
		if at < 0 {
			continue
		}
		point, ok := selectionPointFor(frame, i, leading+graphemeCount(text[:at])+4)
		if ok {
			return point
		}
	}
	t.Fatalf("marker %q missing from prepared region", marker)
	return selectionPoint{}
}

func TestMecatuiFunctionalConversationCards_Scenario3_IncrementalCacheFastPath(t *testing.T) {
	c := &conversation{}
	for i := 0; i < 256; i++ {
		c.addNotice("settled card")
	}
	c.startAssistant()
	c.appendAssistant("tail")
	r := newCacheRenderer()
	first := r.renderConversationFrame(c, false)
	prepares, renders := r.cardPrepares, r.blockRenders

	c.appendAssistant(" update")
	second := r.renderConversationFrame(c, false)
	if r.cardPrepares != prepares || r.blockRenders-renders != 1 {
		t.Fatalf("tail update: card prepares=%d block renders=%d, want 0/1", r.cardPrepares-prepares, r.blockRenders-renders)
	}
	if r.joinPrefixN != len(c.blocks)-1 {
		t.Fatalf("tail update prefix = %d, want %d", r.joinPrefixN, len(c.blocks)-1)
	}

	prepares, renders = r.cardPrepares, r.blockRenders
	unchanged := r.renderConversationFrame(c, false)
	if r.cardPrepares != prepares || r.blockRenders != renders {
		t.Fatal("unchanged frame prepared or rendered cards")
	}
	if !slices.Equal(second.lines, unchanged.lines) {
		t.Fatal("unchanged frame replaced viewport content with different lines")
	}

	c.blocks[100].raw = "non-tail change"
	c.blocks[100].rev++
	r.renderConversationFrame(c, false)
	if r.cardPrepares != prepares+1 {
		t.Fatalf("non-tail mutation prepared %d cards, want 1", r.cardPrepares-prepares)
	}
	if len(first.provenance) == 0 {
		t.Fatal("initial deep frame lacked provenance")
	}
}

func TestMecatuiFunctionalConversationCards_Scenario3_ScrollbackPerformanceContract(t *testing.T) {
	for _, depth := range []int{64, 256} {
		c := &conversation{}
		for i := 0; i < depth; i++ {
			c.addUser("settled functional card")
		}
		c.startAssistant()
		c.appendAssistant("stream")
		r := newCacheRenderer()
		r.renderConversationFrame(c, false)
		prepares, renders := r.cardPrepares, r.blockRenders
		c.appendAssistant(" delta")
		frame := r.renderConversationFrame(c, false)
		if r.cardPrepares != prepares || r.blockRenders-renders != 1 {
			t.Fatalf("depth %d rebuilt settled cards: prepares=%d renders=%d", depth, r.cardPrepares-prepares, r.blockRenders-renders)
		}
		if len(frame.lines) != len(frame.provenance) {
			t.Fatalf("depth %d frame lines/provenance = %d/%d", depth, len(frame.lines), len(frame.provenance))
		}
	}

	for i := 0; i < reflect.TypeOf(renderedRow{}).NumField(); i++ {
		if field := reflect.TypeOf(renderedRow{}).Field(i); field.Type.Kind() == reflect.String {
			t.Fatalf("row metadata retains O(rendered-bytes) text in %s", field.Name)
		}
	}
}
