package ui

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"
)

func TestMecatuiFunctionalConversationCards_Scenario1_PrepareOwnsImmutableSnapshots(t *testing.T) {
	input := cards.UserInput{Text: "immutable prompt", Media: []string{"image/png (inline)"}}
	layoutInput := cards.PlainLayoutInput{Width: 42, ExpandMark: "ctrl+t"}
	inputSnapshot := cards.SnapshotUser(input)
	layoutSnapshot := cards.SnapshotPlainLayout(layoutInput)
	appearance := cards.UserAppearance{
		Label: testTheme().Style("userLabel"),
		Body:  testTheme().Style("userBlock"),
		Muted: testTheme().Style("muted"),
	}
	before := cards.PrepareUser(inputSnapshot, layoutSnapshot, appearance)

	input.Text = "mutated prompt"
	input.Media[0] = "mutated media"
	layoutInput.Width = 8
	layoutInput.ExpandMark = "changed"
	after := cards.PrepareUser(inputSnapshot, layoutSnapshot, appearance)

	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preparation changed after caller-owned inputs mutated:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario1_BlockRenderKeyTracksContentAndContext(t *testing.T) {
	c := &conversation{}
	c.addTool("call-1", "Read", `{"path":"README.md"}`)
	r := newCacheRenderer()
	base := r.blockRenderKey(&c.blocks[0], false)
	if base.context != (renderContextKey{
		width:             r.width,
		paletteGeneration: r.paletteGeneration,
		hintGeneration:    r.hintGeneration,
		dialect:           r.renderDialect,
	}) {
		t.Fatalf("render context key = %#v", base.context)
	}

	assertChanged := func(name string, changed blockRenderKey) {
		t.Helper()
		if changed == base {
			t.Fatalf("%s did not change the composite block render key", name)
		}
	}

	if !c.resolveTool("call-1", "done", false) {
		t.Fatal("resolve tool")
	}
	assertChanged("content revision", r.blockRenderKey(&c.blocks[0], false))

	block := block{rev: base.revision}
	r.width++
	assertChanged("width", r.blockRenderKey(&block, false))
	r.width--
	assertChanged("expanded state", r.blockRenderKey(&block, true))
	r.paletteGeneration++
	assertChanged("palette generation", r.blockRenderKey(&block, false))
	r.paletteGeneration--
	r.hintGeneration++
	assertChanged("visible-hint generation", r.blockRenderKey(&block, false))
	r.hintGeneration--
	r.renderDialect++
	assertChanged("render dialect", r.blockRenderKey(&block, false))
}
