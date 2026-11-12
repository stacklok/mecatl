package session_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestRecursiveViewsZero(t *testing.T) {
	var user session.UserInputView
	var effective session.EffectiveInputView
	var instructions session.InstructionsView
	var catalog session.ToolCatalogView
	var binding session.ModelBindingView
	if user.ID() != (session.UserInputID{}) || user.Parts().Len() != 0 || effective.RunID() != (session.RunID{}) || effective.Inputs().Len() != 0 || instructions.Blocks().Len() != 0 || catalog.Tools().Len() != 0 || !bindingMatches(binding, session.ModelBinding{}) {
		t.Fatal("zero root view")
	}
	var item session.InputItemView
	if id, ok := item.UserInputID(); ok || id != (session.UserInputID{}) {
		t.Fatal("zero reference branch")
	}
	if inline, ok := item.Inline(); ok || inline != (session.AttributedInputView{}) {
		t.Fatal("zero inline branch")
	}
	var inline session.AttributedInputView
	var block session.InstructionBlockView
	if inline.Actor() != "" || inline.Purpose() != "" || inline.Parts().Len() != 0 || inline.Source() != (session.ContentSourceView{}) || block.Parts().Len() != 0 || block.Source() != (session.ContentSourceView{}) {
		t.Fatal("zero required children")
	}
	var part session.ContentPartView
	if text, ok := part.Text(); ok || text != "" {
		t.Fatal("zero text branch")
	}
	if media, ok := part.Media(); ok || media != (session.MediaView{}) {
		t.Fatal("zero media branch")
	}
	var media session.MediaView
	if media.Kind() != "" || media.MIMEType() != "" || media.FormatHint() != "" || media.Filename() != "" || media.Title() != "" || media.Source() != "" {
		t.Fatal("zero media metadata")
	}
	if data := media.InlineData(); data.Copy() != nil {
		t.Fatal("zero inline data")
	}
	for name, get := range map[string]func() string{"RemoteURI": media.RemoteURI, "ProviderName": media.ProviderName, "ProviderScope": media.ProviderScope, "ProviderReferenceID": media.ProviderReferenceID} {
		if value := get(); value != "" {
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
	if tool.Name() != "" || tool.Description() != "" || tool.ArgumentSchema().Copy() != nil {
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
	id, err := b.AddUserInput(run, session.UserInput{Parts: richContent()})
	mustBatch(t, err)
	mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{Blocks: []session.InstructionBlock{
		{Parts: []session.ContentPart{{Text: new("")}}, Source: session.ContentSource{Description: "empty text"}},
		{Source: session.ContentSource{Description: "empty block"}},
	}}))
	mustBatch(t, b.ReplaceToolCatalog(scope, session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte(" false ")}}}))
	mustBatch(t, b.ReplaceModelBinding(scope, session.ModelBinding{Route: "route", API: "api", Model: "model", Settings: []byte(" {\"n\":1.00} ")}))
	mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{
		{UserInputID: new(id.RunRelative())},
		{Inline: &session.AttributedInput{Actor: " actor/尾\n ", Purpose: session.PromptContext, Source: session.ContentSource{Description: "present empty"}}},
		{Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptInstructions, Parts: richContent(), Source: session.ContentSource{Description: "rich"}}},
	}}))
	submitBatch(t, s, b)
	v := s.Snapshot()
	user := lookupInput(t, v, run, 1)
	instructions, _ := v.Instructions()
	catalog, _ := v.ToolCatalog()
	binding, _ := v.ModelBinding()
	effective, _ := v.RecordAt(6).EffectiveInput()
	if ref, ok := effective.Inputs().At(0).UserInputID(); !ok || ref != id {
		t.Fatal("reference lost scope")
	}
	if inline, ok := effective.Inputs().At(0).Inline(); ok || inline != (session.AttributedInputView{}) {
		t.Fatal("reference exposed inline")
	}
	empty := inlineAt(t, effective, 1)
	if ref, ok := effective.Inputs().At(1).UserInputID(); ok || ref != (session.UserInputID{}) {
		t.Fatal("inline exposed reference")
	}
	if empty.Parts().Len() != 0 || empty.Actor() != " actor/尾\n " || empty.Purpose() != session.PromptContext || empty.Source().Description() != "present empty" {
		t.Fatal("present empty inline changed")
	}
	if file, ok := empty.Source().WorkspaceFile(); ok || file != (session.WorkspaceFileSourceView{}) {
		t.Fatal("absent file branch")
	}
	textPart := instructions.Blocks().At(0).Parts().At(0)
	if text, ok := textPart.Text(); !ok || text != "" {
		t.Fatal("empty text lost presence")
	}
	for _, part := range []session.ContentPartView{textPart, user.Parts().At(0)} {
		want, present := part.Text()
		var text string
		var ok bool
		if n := testing.AllocsPerRun(100, func() {
			text, ok = part.Text()
		}); n != 0 {
			t.Fatalf("text navigation allocated %g times", n)
		}
		if !present || !ok || text != want {
			t.Fatal("text navigation changed")
		}
		if want != "" {
			if n := testing.AllocsPerRun(10, func() {
				value, _ := part.Text()
				text = strings.Clone(value)
			}); n == 0 {
				t.Fatal("text-copy control was optimized away")
			}
			runtime.KeepAlive(text)
		}
	}
	if media, ok := textPart.Media(); ok || media != (session.MediaView{}) {
		t.Fatal("text exposed media")
	}
	if text, ok := user.Parts().At(1).Text(); ok || text != "" {
		t.Fatal("media exposed text")
	}
	if media := mediaAt(t, user.Parts().At(5)); media.Source() != session.MediaSourceInline || len(media.InlineData().Copy()) != 0 {
		t.Fatal("empty inline bytes lost presence")
	}
	remote := mediaAt(t, user.Parts().At(2))
	if remote.Source() != session.MediaSourceRemote || remote.InlineData().Len() != 0 {
		t.Fatal("remote exposed inline bytes")
	}
	if provider := remote.ProviderName(); provider != "" {
		t.Fatal("remote exposed provider")
	}
	provider := mediaAt(t, user.Parts().At(3))
	if provider.Source() != session.MediaSourceProvider || provider.RemoteURI() != "" {
		t.Fatal("provider exposed URI")
	}
	media := mediaAt(t, user.Parts().At(1))
	data := media.InlineData()
	other := media.InlineData()
	copied, otherCopy := data.Copy(), other.Copy()
	copied[0] = 0
	if otherCopy[0] != 0xff || other.At(0) != 0xff {
		t.Fatal("byte exports share storage")
	}
	mutateMediaBytes(t, user.Parts().At(1))
	clear(catalog.Tools().At(0).ArgumentSchema().Copy())
	clear(binding.Settings().Copy())
	if !mediaMatches(media, session.Media{Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", FormatHint: "raw", Filename: "notes.bin", Title: "Notes", Source: session.MediaSourceInline, InlineData: []byte{0xff, 0, 'x'}}) || string(catalog.Tools().At(0).ArgumentSchema().Copy()) != " false " || string(binding.Settings().Copy()) != " {\"n\":1.00} " {
		t.Fatal("byte getter exposed accepted storage")
	}

	rich := inlineAt(t, effective, 2)
	for _, collection := range []struct {
		name  string
		count int
		at    func(int)
	}{
		{"zero user", 0, func(i int) { (session.UserInputView{}).Parts().At(i) }},
		{"zero effective", 0, func(i int) { (session.EffectiveInputView{}).Inputs().At(i) }},
		{"zero instructions", 0, func(i int) { (session.InstructionsView{}).Blocks().At(i) }},
		{"zero catalog", 0, func(i int) { (session.ToolCatalogView{}).Tools().At(i) }},
		{"zero inline", 0, func(i int) { (session.AttributedInputView{}).Parts().At(i) }},
		{"zero block", 0, func(i int) { (session.InstructionBlockView{}).Parts().At(i) }},
		{"user", user.Parts().Len(), func(i int) { user.Parts().At(i) }},
		{"effective", effective.Inputs().Len(), func(i int) { effective.Inputs().At(i) }},
		{"instructions", instructions.Blocks().Len(), func(i int) { instructions.Blocks().At(i) }},
		{"catalog", catalog.Tools().Len(), func(i int) { catalog.Tools().At(i) }},
		{"inline", rich.Parts().Len(), func(i int) { rich.Parts().At(i) }},
		{"block", instructions.Blocks().At(0).Parts().Len(), func(i int) { instructions.Blocks().At(0).Parts().At(i) }},
		{"empty inline", 0, func(i int) { empty.Parts().At(i) }},
		{"empty block", 0, func(i int) { instructions.Blocks().At(1).Parts().At(i) }},
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
