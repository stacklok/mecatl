package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func richContent() []session.ContentPart {
	return []session.ContentPart{
		{Text: new(" /skill example\r\ne\u0301 ")},
		{Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", FormatHint: "raw", Filename: "notes.bin", Title: "Notes", Source: session.MediaSourceInline, InlineData: []byte{0xff, 0, 'x'}}},
		{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceRemote, RemoteURI: "https://example.invalid/a%2Fb?q=x"}},
		{Media: &session.Media{Kind: session.MediaKindAudio, FormatHint: "wav", Source: session.MediaSourceProvider, ProviderName: "provider", ProviderScope: "tenant", ProviderReferenceID: "reference"}},
		{Text: new("")},
		{Media: &session.Media{Kind: session.MediaKindImage, MIMEType: "image/empty", Source: session.MediaSourceInline}},
	}
}
func TestContentOwnedValues(t *testing.T) {
	v := session.UserInput{ID: session.UserInputID{RunID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}, Ordinal: 1}, Parts: richContent()}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := v.Clone()
	v.Parts[1].Media.InlineData[0] = 0
	v.Parts[2].Media.RemoteURI = "urn:changed"
	v.Parts[3].Media.ProviderScope = "changed"
	*v.Parts[4].Text = "changed"
	if !cloned.Equal(session.UserInput{ID: v.ID, Parts: richContent()}) {
		t.Fatal("clone retained storage")
	}
	for _, part := range richContent() {
		clone := part.Clone()
		if !clone.Equal(part) {
			t.Fatal("part clone")
		}
		if part.Text != nil {
			*part.Text = "changed"
			if *clone.Text == "changed" {
				t.Fatal("part clone retained text pointer")
			}
		}
		if part.Media != nil && !part.Media.Clone().Equal(*part.Media) {
			t.Fatal("media clone")
		}
	}
	empty := session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/x"}
	if empty.Validate() != nil || empty.Equal(session.Media{}) {
		t.Fatal("inline presence lost")
	}
	for name, part := range map[string]session.ContentPart{
		"zero": {}, "unknown media": {Media: &session.Media{Kind: "video", Source: session.MediaSourceRemote, RemoteURI: "urn:a"}},
		"invalid text":               {Text: new("\xff")},
		"empty text and media":       {Text: new(""), Media: &empty},
		"media text":                 {Text: new("text"), Media: &empty},
		"no source":                  {Media: &session.Media{Kind: session.MediaKindImage}},
		"foreign fields":             {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/x", RemoteURI: "urn:a"}},
		"missing MIME":               {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline}},
		"document metadata on image": {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceRemote, RemoteURI: "urn:a", Filename: "x"}},
		"bad metadata":               {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceRemote, RemoteURI: "urn:a", MIMEType: "\xff"}},
		"relative URI":               {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceRemote, RemoteURI: "a/b"}},
		"empty provider scope":       {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceProvider, ProviderName: "p", ProviderReferenceID: "i"}},
	} {
		t.Run(name, func(t *testing.T) {
			if part.Validate() == nil {
				t.Fatal("invalid content validated")
			}
		})
	}
}

func TestContentSourceAndAttributedOwnedValues(t *testing.T) {
	source := session.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &session.WorkspaceFileSource{Path: "dir/AGENTS.md"}}
	if source.Validate() != nil || !source.Clone().Equal(source) || !source.WorkspaceFile.Clone().Equal(*source.WorkspaceFile) {
		t.Fatal("valid source")
	}
	clone := source.Clone()
	source.WorkspaceFile.Path = "changed"
	if clone.WorkspaceFile.Path != "dir/AGENTS.md" {
		t.Fatal("source aliases")
	}
	for _, path := range []string{"", ".", "..", "../a", "/a", "a//b", "a/./b", "a/../b", "a/", "\xff"} {
		if (session.WorkspaceFileSource{Path: path}).Validate() == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	if (session.ContentSource{}).Validate() == nil || (session.ContentSource{Description: "\xff"}).Validate() == nil {
		t.Fatal("bad description")
	}
	h := session.AttributedInput{Actor: "extension/尾\n exact ", Purpose: session.PromptContext, Parts: richContent(), Source: clone}
	if h.Validate() != nil || !h.Clone().Equal(h) {
		t.Fatal("valid attribution")
	}
	p := session.InputItem{Inline: &h}
	pc := p.Clone()
	h.Parts[1].Media.InlineData[0] = 0
	h.Source.WorkspaceFile.Path = "changed"
	if pc.Equal(p) || pc.Validate() != nil {
		t.Fatal("inline clone aliases")
	}
	id := session.UserInputID{RunID: session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}, Ordinal: 1}
	ref := session.InputItem{UserInput: &id}
	rc := ref.Clone()
	id.Ordinal = 2
	if rc.Equal(ref) || rc.Validate() != nil || rc.UserInput.Ordinal != 1 {
		t.Fatal("reference clone")
	}
	for _, p := range []session.InputItem{{}, {UserInput: &id, Inline: &h}, {UserInput: new(session.UserInputID)},
		{Inline: &session.AttributedInput{Actor: "a", Purpose: "system", Source: clone}},
		{Inline: &session.AttributedInput{Purpose: session.PromptContext, Source: clone}},
		{Inline: &session.AttributedInput{Actor: "\xff", Purpose: session.PromptContext, Source: clone}},
		{Inline: &session.AttributedInput{Actor: "a", Purpose: session.PromptInstructions}},
	} {
		if p.Validate() == nil {
			t.Fatal("invalid item validated")
		}
	}
	block := session.InstructionBlock{Parts: richContent(), Source: clone}
	bc := block.Clone()
	block.Source.WorkspaceFile.Path = "mutated"
	if bc.Equal(block) || bc.Validate() != nil {
		t.Fatal("block ownership")
	}
	tool := session.ToolDefinition{Name: "read", ArgumentSchema: []byte(" true ")}
	tc := tool.Clone()
	tool.ArgumentSchema[1] = 'x'
	if tc.Equal(tool) || tc.Validate() != nil || tool.Validate() == nil {
		t.Fatal("tool ownership/JSON")
	}
}
