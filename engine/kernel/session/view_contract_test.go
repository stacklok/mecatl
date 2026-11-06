package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestRecursiveViewsZero(t *testing.T) {
	var user session.UserInputView
	var effective session.EffectiveInputView
	var instructions session.InstructionsView
	var catalog session.ToolCatalogView
	var binding session.ModelBindingView
	if user.ID() != (session.UserInputID{}) || user.PartCount() != 0 || effective.RunID() != (session.RunID{}) || effective.InputCount() != 0 || instructions.BlockCount() != 0 || catalog.ToolCount() != 0 || !bindingMatches(binding, session.ModelBinding{}) {
		t.Fatal("zero root view")
	}
	var item session.InputItemView
	if id, ok := item.UserInput(); ok || id != (session.UserInputID{}) {
		t.Fatal("zero reference branch")
	}
	if inline, ok := item.Inline(); ok || inline != (session.AttributedInputView{}) {
		t.Fatal("zero inline branch")
	}
	var inline session.AttributedInputView
	var block session.InstructionBlockView
	if inline.Actor() != "" || inline.Purpose() != "" || inline.PartCount() != 0 || inline.Source() != (session.ContentSourceView{}) || block.PartCount() != 0 || block.Source() != (session.ContentSourceView{}) {
		t.Fatal("zero required children")
	}
	var part session.ContentPartView
	if part.Kind() != "" {
		t.Fatal("zero content kind")
	}
	if text, ok := part.Text(); ok || text != "" {
		t.Fatal("zero text branch")
	}
	if media, ok := part.Media(); ok || media != (session.MediaView{}) {
		t.Fatal("zero media branch")
	}
	var media session.MediaView
	if media.MediaType() != "" || media.Format() != "" || media.Filename() != "" || media.Title() != "" || media.Source() != "" {
		t.Fatal("zero media metadata")
	}
	if data, ok := media.Data(); ok || data != nil {
		t.Fatal("zero inline data")
	}
	for name, get := range map[string]func() (string, bool){"URI": media.URI, "Provider": media.Provider, "Scope": media.Scope, "ReferenceID": media.ReferenceID} {
		if value, ok := get(); ok || value != "" {
			t.Errorf("zero media %s", name)
		}
	}
	var source session.ContentSourceView
	if source.Description() != "" {
		t.Fatal("zero source")
	}
	if file, ok := source.WorkspaceFile(); ok || file != (session.WorkspaceFileSourceView{}) {
		t.Fatal("zero workspace branch")
	}
	if (session.WorkspaceFileSourceView{}).Path() != "" {
		t.Fatal("zero workspace path")
	}
	var tool session.ToolDefinitionView
	if tool.Name() != "" || tool.Description() != "" || tool.ArgumentSchema() != nil {
		t.Fatal("zero tool")
	}
}

func TestRecursiveViewsPresenceBytesAndBounds(t *testing.T) {
	p := sessionPolicy()
	p.PayloadLimits.MaxDepth = 16
	p.PayloadLimits.MaxCollectionElements = 512
	p.PayloadLimits.MaxNodes = 2048
	s := newSession(t, p)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	id, err := b.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: richContent()})
	mustBatch(t, err)
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{
		{Parts: []session.ContentPart{{Kind: session.ContentText}}, Source: session.ContentSource{Description: "empty text"}},
		{Source: session.ContentSource{Description: "empty block"}},
	}}))
	mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope, Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte(" false ")}}}))
	mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: scope, Route: "route", API: "api", Model: "model", Settings: []byte(" {\"n\":1.00} ")}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{UserInput: &id},
		{Inline: &session.AttributedInput{Actor: " actor/尾\n ", Purpose: session.PromptContext, Source: session.ContentSource{Description: "present empty"}}},
		{Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptInstructions, Parts: richContent(), Source: session.ContentSource{Description: "rich"}}},
	}}))
	submitBatch(t, s, b)
	v := s.Snapshot()
	user := lookupInput(t, v, run, id.Ordinal)
	instructions, _ := v.Instructions()
	catalog, _ := v.ToolCatalog()
	binding, _ := v.ModelBinding()
	effective, _ := v.RecordAt(6).EffectiveInput()
	if ref, ok := effective.InputAt(0).UserInput(); !ok || ref != id {
		t.Fatal("reference lost scope")
	}
	if inline, ok := effective.InputAt(0).Inline(); ok || inline != (session.AttributedInputView{}) {
		t.Fatal("reference exposed inline")
	}
	empty := inlineAt(t, effective, 1)
	if ref, ok := effective.InputAt(1).UserInput(); ok || ref != (session.UserInputID{}) {
		t.Fatal("inline exposed reference")
	}
	if empty.PartCount() != 0 || empty.Actor() != " actor/尾\n " || empty.Purpose() != session.PromptContext || empty.Source().Description() != "present empty" {
		t.Fatal("present empty inline changed")
	}
	if file, ok := empty.Source().WorkspaceFile(); ok || file != (session.WorkspaceFileSourceView{}) {
		t.Fatal("absent file branch")
	}
	textPart := instructions.BlockAt(0).PartAt(0)
	if text, ok := textPart.Text(); !ok || text != "" {
		t.Fatal("empty text lost presence")
	}
	if media, ok := textPart.Media(); ok || media != (session.MediaView{}) {
		t.Fatal("text exposed media")
	}
	if text, ok := user.PartAt(1).Text(); ok || text != "" {
		t.Fatal("media exposed text")
	}
	if data, ok := mediaAt(t, user.PartAt(5)).Data(); !ok || len(data) != 0 {
		t.Fatal("empty inline bytes lost presence")
	}
	remote := mediaAt(t, user.PartAt(2))
	if data, ok := remote.Data(); ok || data != nil {
		t.Fatal("remote exposed inline bytes")
	}
	if provider, ok := remote.Provider(); ok || provider != "" {
		t.Fatal("remote exposed provider")
	}
	provider := mediaAt(t, user.PartAt(3))
	if uri, ok := provider.URI(); ok || uri != "" {
		t.Fatal("provider exposed URI")
	}
	media := mediaAt(t, user.PartAt(1))
	data, _ := media.Data()
	other, _ := media.Data()
	data[0] = 0
	if other[0] != 0xff {
		t.Fatal("byte exports share storage")
	}
	mutateMediaBytes(t, user.PartAt(1))
	clear(catalog.ToolAt(0).ArgumentSchema())
	clear(binding.Settings())
	if !mediaMatches(media, session.Media{MediaType: "application/octet-stream", Format: "raw", Filename: "notes.bin", Title: "Notes", Source: session.MediaInline, Data: []byte{0xff, 0, 'x'}}) || string(catalog.ToolAt(0).ArgumentSchema()) != " false " || string(binding.Settings()) != " {\"n\":1.00} " {
		t.Fatal("byte getter exposed accepted storage")
	}

	rich := inlineAt(t, effective, 2)
	for _, collection := range []struct {
		name  string
		count int
		at    func(int)
	}{
		{"zero user", 0, func(i int) { (session.UserInputView{}).PartAt(i) }},
		{"zero effective", 0, func(i int) { (session.EffectiveInputView{}).InputAt(i) }},
		{"zero instructions", 0, func(i int) { (session.InstructionsView{}).BlockAt(i) }},
		{"zero catalog", 0, func(i int) { (session.ToolCatalogView{}).ToolAt(i) }},
		{"zero inline", 0, func(i int) { (session.AttributedInputView{}).PartAt(i) }},
		{"zero block", 0, func(i int) { (session.InstructionBlockView{}).PartAt(i) }},
		{"user", user.PartCount(), func(i int) { user.PartAt(i) }},
		{"effective", effective.InputCount(), func(i int) { effective.InputAt(i) }},
		{"instructions", instructions.BlockCount(), func(i int) { instructions.BlockAt(i) }},
		{"catalog", catalog.ToolCount(), func(i int) { catalog.ToolAt(i) }},
		{"inline", rich.PartCount(), func(i int) { rich.PartAt(i) }},
		{"block", instructions.BlockAt(0).PartCount(), func(i int) { instructions.BlockAt(0).PartAt(i) }},
		{"empty inline", 0, func(i int) { empty.PartAt(i) }},
		{"empty block", 0, func(i int) { instructions.BlockAt(1).PartAt(i) }},
	} {
		t.Run(collection.name, func(t *testing.T) {
			for _, index := range []int{-1, collection.count, collection.count + 1} {
				func() {
					defer func() {
						if recover() == nil {
							t.Errorf("index %d did not panic", index)
						}
					}()
					collection.at(index)
				}()
			}
		})
	}
}
