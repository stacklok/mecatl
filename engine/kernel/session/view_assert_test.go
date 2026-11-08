package session_test

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

// Comparisons walk the public inspection surface without materializing values.
func sourceMatches(v session.ContentSourceView, want session.ContentSource) bool {
	file, ok := v.WorkspaceFile()
	return v.Description() == want.Description && ok == (want.WorkspaceFile != nil) && (!ok || file.Path() == want.WorkspaceFile.Path)
}

func mediaMatches(v session.MediaView, want session.Media) bool {
	data, inline := v.InlineData()
	uri, remote := v.RemoteURI()
	provider, bound := v.ProviderName()
	scope, scopeOK := v.ProviderScope()
	ref, refOK := v.ProviderReferenceID()
	return v.Kind() == want.Kind && v.MIMEType() == want.MIMEType && v.FormatHint() == want.FormatHint && v.Filename() == want.Filename && v.Title() == want.Title && v.Source() == want.Source &&
		inline == (want.Source == session.MediaSourceInline) && bytes.Equal(data.Copy(), want.InlineData) &&
		remote == (want.Source == session.MediaSourceRemote) && uri == want.RemoteURI &&
		bound == (want.Source == session.MediaSourceProvider) && scopeOK == bound && refOK == bound && provider == want.ProviderName && scope == want.ProviderScope && ref == want.ProviderReferenceID
}

func partMatches(v session.ContentPartView, want session.ContentPart) bool {
	text, textOK := v.Text()
	media, mediaOK := v.Media()
	return textOK == (want.Text != nil) && (!textOK || text == *want.Text) && mediaOK == (want.Media != nil) && (!mediaOK || mediaMatches(media, *want.Media))
}

func userInputMatches(v session.UserInputView, want session.UserInput) bool {
	if v.Parts().Len() != len(want.Parts) {
		return false
	}
	for i, part := range want.Parts {
		if !partMatches(v.Parts().At(i), part) {
			return false
		}
	}
	return true
}

func attributedMatches(v session.AttributedInputView, want session.AttributedInput) bool {
	if v.Actor() != want.Actor || v.Purpose() != want.Purpose || !sourceMatches(v.Source(), want.Source) || v.Parts().Len() != len(want.Parts) {
		return false
	}
	for i, part := range want.Parts {
		if !partMatches(v.Parts().At(i), part) {
			return false
		}
	}
	return true
}

func effectiveMatches(v session.EffectiveInputView, want session.EffectiveInput) bool {
	if v.InputCount() != len(want.Inputs) {
		return false
	}
	for i, item := range want.Inputs {
		got := v.InputAt(i)
		id, refOK := got.UserInputID()
		inline, inlineOK := got.Inline()
		if refOK != (item.UserInputID != nil) || inlineOK != (item.Inline != nil) || (refOK && (id.RunRelative() != *item.UserInputID || id.RunID() != v.RunID())) || (inlineOK && !attributedMatches(inline, *item.Inline)) {
			return false
		}
	}
	return true
}

func instructionsMatch(v session.InstructionsView, want session.Instructions) bool {
	if v.Blocks().Len() != len(want.Blocks) {
		return false
	}
	for i, block := range want.Blocks {
		got := v.Blocks().At(i)
		if !sourceMatches(got.Source(), block.Source) || got.Parts().Len() != len(block.Parts) {
			return false
		}
		for j, part := range block.Parts {
			if !partMatches(got.Parts().At(j), part) {
				return false
			}
		}
	}
	return true
}

func catalogMatches(v session.ToolCatalogView, want session.ToolCatalog) bool {
	if v.Tools().Len() != len(want.Tools) {
		return false
	}
	for i, tool := range want.Tools {
		got := v.Tools().At(i)
		if got.Name() != tool.Name || got.Description() != tool.Description || !bytes.Equal(got.ArgumentSchema().Copy(), tool.ArgumentSchema) {
			return false
		}
	}
	return true
}

func bindingMatches(v session.ModelBindingView, want session.ModelBinding) bool {
	return v.Route() == want.Route && v.API() == want.API && v.Model() == want.Model && bytes.Equal(v.Settings().Copy(), want.Settings)
}

func inlineAt(t *testing.T, v session.EffectiveInputView, i int) session.AttributedInputView {
	t.Helper()
	in, ok := v.InputAt(i).Inline()
	if !ok {
		t.Fatal("missing inline branch")
	}
	return in
}

func mediaAt(t *testing.T, part session.ContentPartView) session.MediaView {
	t.Helper()
	m, ok := part.Media()
	if !ok {
		t.Fatal("missing media branch")
	}
	return m
}

func mutateMediaBytes(t *testing.T, part session.ContentPartView) {
	t.Helper()
	data, ok := mediaAt(t, part).InlineData()
	if !ok {
		t.Fatal("missing inline media")
	}
	clear(data.Copy())
}
