package session_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// payloadViewWant retains authored values and allocated identities independently
// of either inspected snapshot. Collections and unrelated leaves are nonempty.
type payloadViewWant struct {
	run          session.RunID
	input        session.UserInputID
	parts        []session.ContentPart
	instructions session.Instructions
	catalog      session.ToolCatalog
	binding      session.ModelBinding
	effective    session.EffectiveInput
}

func completePayloadViewFixture(t *testing.T, count, field, sibling int) (map[string]*session.Session, payloadViewWant) {
	h, s, policy := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	want := payloadViewWant{run: batchRun(t, b), parts: richContent()}
	// Vary the selected leaf separately from populated metadata and other leaves.
	want.parts[1].Media.InlineData = bytes.Repeat([]byte{0xa5}, field)
	want.parts[1].Media.Title = strings.Repeat("m", sibling)
	want.parts = append(want.parts, session.ContentPart{Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", Source: session.MediaSourceInline, InlineData: bytes.Repeat([]byte{0x5a}, sibling)}})
	var err error
	want.input, err = b.AddUserInput(want.run, session.UserInput{Parts: want.parts})
	mustBatch(t, err)
	source := session.ContentSource{Description: strings.Repeat("d", sibling), WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}
	for i := range count {
		want.instructions.Blocks = append(want.instructions.Blocks, session.InstructionBlock{Parts: want.parts, Source: source})
		want.catalog.Tools = append(want.catalog.Tools, session.ToolDefinition{Name: "tool" + strconv.Itoa(i), Description: strings.Repeat("t", sibling), ArgumentSchema: []byte(`"` + strings.Repeat("s", field-2) + `"`)})
		want.effective.Inputs = append(want.effective.Inputs, session.InputItem{UserInputID: new(want.input.RunRelative())},
			session.InputItem{Inline: &session.AttributedInput{Parts: want.parts, Actor: "extension/example", Purpose: session.PromptContext, Source: source}})
	}
	// Include present-empty children alongside the nonempty collection siblings.
	want.instructions.Blocks = append(want.instructions.Blocks, session.InstructionBlock{Source: session.ContentSource{Description: "empty"}})
	want.effective.Inputs = append(want.effective.Inputs, session.InputItem{Inline: &session.AttributedInput{Actor: "empty", Purpose: session.PromptInstructions, Source: session.ContentSource{Description: "empty"}}})
	want.binding = session.ModelBinding{Route: strings.Repeat("r", sibling), API: "api", Model: "model", Settings: []byte(`"` + strings.Repeat("v", field-2) + `"`)}
	mustBatch(t, b.ReplaceInstructions(want.run.HistoryID(), want.instructions))
	mustBatch(t, b.ReplaceToolCatalog(want.run.HistoryID(), want.catalog))
	mustBatch(t, b.ReplaceModelBinding(want.run.HistoryID(), want.binding))
	mustBatch(t, b.AddEffectiveInput(want.run, want.effective))
	submitBatch(t, s, b)
	return iteratorSessions(t, h, s, policy), want
}

// inspectPayloadBytes reads the entire borrowed leaf, never an owned export.
func inspectPayloadBytes(got views.Bytes, want []byte) int {
	if got.Len() != len(want) {
		panic("byte length")
	}
	for i, value := range got.All() {
		if value != want[i] || got.At(i) != value {
			panic("byte value")
		}
	}
	return got.Len()
}

func inspectPayloadSource(got session.ContentSourceView, want session.ContentSource) {
	file, present := got.WorkspaceFile()
	if got.Description() != want.Description || present != (want.WorkspaceFile != nil) {
		panic("source")
	}
	if present && file.Path() != want.WorkspaceFile.Path {
		panic("file label")
	}
}

func inspectPayloadParts(got views.SliceView[sessionformat.ContentPart, session.ContentPartView], want []session.ContentPart) int {
	if got.Len() != len(want) {
		panic("part count")
	}
	leafBytes := 0
	for i, part := range got.All() {
		text, textOK := part.Text()
		media, mediaOK := part.Media()
		if part != got.At(i) || textOK != (want[i].Text != nil) || mediaOK != (want[i].Media != nil) {
			panic("part presence/backing")
		}
		if textOK && text != *want[i].Text {
			panic("text")
		}
		if !mediaOK {
			continue
		}
		m := want[i].Media
		data, inline := media.InlineData()
		uri, remote := media.RemoteURI()
		provider, bound := media.ProviderName()
		scope, scoped := media.ProviderScope()
		ref, referenced := media.ProviderReferenceID()
		if media.Kind() != m.Kind || media.MIMEType() != m.MIMEType || media.FormatHint() != m.FormatHint || media.Filename() != m.Filename || media.Title() != m.Title || media.Source() != m.Source ||
			inline != (m.Source == session.MediaSourceInline) || remote != (m.Source == session.MediaSourceRemote) || bound != (m.Source == session.MediaSourceProvider) || scoped != bound || referenced != bound ||
			uri != m.RemoteURI || provider != m.ProviderName || scope != m.ProviderScope || ref != m.ProviderReferenceID {
			panic("media metadata/presence")
		}
		leafBytes += inspectPayloadBytes(data, m.InlineData)
	}
	return leafBytes
}

// traverseCompletePayloadViews visits every scalar, presence bit, full identity,
// collection element and borrowed byte in the expanded context payload families.
func traverseCompletePayloadViews(snap session.Snapshot, want payloadViewWant) int {
	created, createdOK := snap.RecordAt(0).Created()
	started, startedOK := snap.RecordAt(1).RunStarted()
	input, inputOK := snap.RecordAt(2).UserInput()
	instructions, instructionsOK := snap.RecordAt(3).Instructions()
	catalog, catalogOK := snap.RecordAt(4).ToolCatalog()
	binding, bindingOK := snap.RecordAt(5).ModelBinding()
	effective, effectiveOK := snap.RecordAt(6).EffectiveInput()
	if !createdOK || !startedOK || !inputOK || !instructionsOK || !catalogOK || !bindingOK || !effectiveOK || created.HistoryID() != want.run.HistoryID() || started.ID() != want.run || input.ID() != want.input ||
		instructions.HistoryID() != want.run.HistoryID() || catalog.HistoryID() != want.run.HistoryID() || binding.HistoryID() != want.run.HistoryID() || effective.RunID() != want.run {
		panic("root scope/presence")
	}
	leafBytes := inspectPayloadParts(input.Parts(), want.parts)
	if instructions.Blocks().Len() != len(want.instructions.Blocks) || catalog.Tools().Len() != len(want.catalog.Tools) || effective.InputCount() != len(want.effective.Inputs) {
		panic("collection count")
	}
	for i, block := range instructions.Blocks().All() {
		if block != instructions.Blocks().At(i) {
			panic("block backing")
		}
		inspectPayloadSource(block.Source(), want.instructions.Blocks[i].Source)
		leafBytes += inspectPayloadParts(block.Parts(), want.instructions.Blocks[i].Parts)
	}
	for i, tool := range catalog.Tools().All() {
		w := want.catalog.Tools[i]
		if tool != catalog.Tools().At(i) || tool.Name() != w.Name || tool.Description() != w.Description {
			panic("tool")
		}
		leafBytes += inspectPayloadBytes(tool.ArgumentSchema(), w.ArgumentSchema)
	}
	if binding.Route() != want.binding.Route || binding.API() != want.binding.API || binding.Model() != want.binding.Model {
		panic("binding")
	}
	leafBytes += inspectPayloadBytes(binding.Settings(), want.binding.Settings)
	i := 0
	for item := range effective.Inputs() {
		w := want.effective.Inputs[i]
		ref, referenceOK := item.UserInputID()
		inline, inlineOK := item.Inline()
		if item != effective.InputAt(i) || referenceOK != (w.UserInputID != nil) || inlineOK != (w.Inline != nil) {
			panic("input presence/backing")
		}
		if referenceOK && ref != want.input {
			panic("full input reference")
		}
		if inlineOK {
			if inline.Actor() != w.Inline.Actor || inline.Purpose() != w.Inline.Purpose {
				panic("inline labels")
			}
			inspectPayloadSource(inline.Source(), w.Inline.Source)
			leafBytes += inspectPayloadParts(inline.Parts(), w.Inline.Parts)
		}
		i++
	}
	if i != len(want.effective.Inputs) {
		panic("scoped iteration count")
	}
	return leafBytes
}

var payloadViewBytesSink int

func TestCompletePayloadViewTraversal(t *testing.T) {
	for _, axis := range []struct {
		name                  string
		count, field, sibling int
	}{
		{"small", 2, 32, 32}, {"collections", 32, 32, 32}, {"requested-large", 2, 64 << 10, 32}, {"sibling-large", 2, 32, 64 << 10},
	} {
		t.Run(axis.name, func(t *testing.T) {
			sessions, want := completePayloadViewFixture(t, axis.count, axis.field, axis.sibling)
			mediaBytes := axis.field + axis.sibling // The other rich media leaves are empty.
			wantBytes := (1+2*axis.count)*mediaBytes + (axis.count+1)*axis.field
			for seam, s := range sessions {
				t.Run(seam, func(t *testing.T) {
					snap := s.Snapshot()
					traverse := func() { payloadViewBytesSink = traverseCompletePayloadViews(snap, want) }
					traverse()
					if payloadViewBytesSink != wantBytes {
						t.Fatalf("visited %d bytes, want %d", payloadViewBytesSink, wantBytes)
					}
					assertIteratorNoAllocation(t, traverse)
					// Retain selected exports while copying unrelated leaves; each copy
					// must allocate for just that leaf, not its populated DTO siblings.
					catalog, _ := snap.ToolCatalog()
					binding, _ := snap.ModelBinding()
					for _, leaf := range []views.Bytes{catalog.Tools().At(0).ArgumentSchema(), binding.Settings()} {
						var copied []byte
						export := func() { copied = copyInteractionBytes(leaf) }
						if n := testing.AllocsPerRun(100, export); n != 1 {
							t.Fatalf("Copy allocated %g times", n)
						}
						if n := allocatedViewBytes(export); n < uint64(axis.field) || n > uint64(axis.field+4096) {
							t.Fatalf("Copy allocated %d bytes for %d-byte leaf", n, axis.field)
						}
						first := leaf.At(0)
						copied[0] ^= 0xff
						if leaf.At(0) != first {
							t.Fatal("Copy shares backing")
						}
					}
				})
			}
		})
	}
}
