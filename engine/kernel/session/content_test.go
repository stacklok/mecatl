package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func richContent() session.Content {
	return session.Content{
		{Kind: session.ContentText, Text: " /skill example\r\ne\u0301 "},
		{Kind: session.ContentDocument, Media: &session.MediaContent{MediaType: "application/octet-stream", Format: "raw", Filename: "notes.bin", Title: "Notes", Source: session.MediaSource{Inline: &session.InlineMedia{Data: []byte{0xff, 0, 'x'}}}}},
		{Kind: session.ContentImage, Media: &session.MediaContent{Source: session.MediaSource{Remote: &session.RemoteMedia{URI: "https://example.invalid/a%2Fb?q=x"}}}},
		{Kind: session.ContentAudio, Media: &session.MediaContent{Format: "wav", Source: session.MediaSource{Provider: &session.ProviderMedia{Provider: "provider", Scope: "tenant", ID: "reference"}}}},
		{Kind: session.ContentText, Text: "\ntrailing"},
		{Kind: session.ContentImage, Media: &session.MediaContent{MediaType: "image/empty", Source: session.MediaSource{Inline: &session.InlineMedia{}}}},
	}
}
func TestContentOwnedValues(t *testing.T) {
	content := richContent()
	if err := content.Validate(); err != nil {
		t.Fatal(err)
	}
	cloned := content.Clone()
	if !cloned.Equal(content) {
		t.Fatal("clone changed exact content")
	}
	content[1].Media.Source.Inline.Data[0] = 0
	content[2].Media.Source.Remote.URI = "urn:changed"
	content[3].Media.Source.Provider.Scope = "changed"
	content[4].Text = "changed"
	if !cloned.Equal(richContent()) {
		t.Fatal("clone retained nested storage")
	}
	if !(session.Content(nil)).Equal(session.Content{}) {
		t.Fatal("nil/empty content differ")
	}
	for _, part := range richContent() {
		if !part.Clone().Equal(part) {
			t.Fatal("part clone")
		}
		if part.Media != nil {
			media := *part.Media
			if !media.Clone().Equal(media) || !media.Source.Clone().Equal(media.Source) {
				t.Fatal("media clone")
			}
		}
	}
	inline := session.InlineMedia{Data: []byte{0xff}}
	c := inline.Clone()
	c.Data[0] = 0
	if inline.Data[0] != 0xff || inline.Equal(c) || inline.Validate() != nil {
		t.Fatal("inline ownership/validation")
	}
	remote := session.RemoteMedia{URI: "urn:exact:label"}
	provider := session.ProviderMedia{Provider: "p", Scope: "s", ID: "i"}
	if remote.Validate() != nil || !remote.Clone().Equal(remote) || provider.Validate() != nil || !provider.Clone().Equal(provider) {
		t.Fatal("scalar source values")
	}
	empty := session.MediaSource{Inline: &session.InlineMedia{}}
	if empty.Validate() != nil || empty.Equal(session.MediaSource{}) {
		t.Fatal("inline presence lost")
	}
	for name, invalid := range map[string]session.Content{
		"zero part":                  {{}},
		"unknown":                    {{Kind: "video"}},
		"invalid text":               {{Kind: session.ContentText, Text: "\xff"}},
		"text media":                 {{Kind: session.ContentText, Media: &session.MediaContent{}}},
		"missing media":              {{Kind: session.ContentImage}},
		"media text":                 {{Kind: session.ContentDocument, Text: "text", Media: &session.MediaContent{}}},
		"no source":                  {{Kind: session.ContentImage, Media: &session.MediaContent{}}},
		"two sources":                {{Kind: session.ContentImage, Media: &session.MediaContent{Source: session.MediaSource{Inline: &session.InlineMedia{}, Remote: &remote}}}},
		"missing inline type":        {{Kind: session.ContentImage, Media: &session.MediaContent{Source: empty}}},
		"document metadata on image": {{Kind: session.ContentImage, Media: &session.MediaContent{Filename: "x", Source: session.MediaSource{Remote: &remote}}}},
		"bad metadata":               {{Kind: session.ContentImage, Media: &session.MediaContent{MediaType: "\xff", Source: session.MediaSource{Remote: &remote}}}},
		"relative URI":               {{Kind: session.ContentImage, Media: &session.MediaContent{Source: session.MediaSource{Remote: &session.RemoteMedia{URI: "a/b"}}}}},
		"empty provider scope":       {{Kind: session.ContentImage, Media: &session.MediaContent{Source: session.MediaSource{Provider: &session.ProviderMedia{Provider: "p", ID: "i"}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if invalid.Validate() == nil {
				t.Fatal("invalid content validated")
			}
		})
	}
}

func TestContentSourceAndPreparedOwnedValues(t *testing.T) {
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
	h := session.HarnessPrompt{Purpose: session.PromptContext, Content: richContent(), Source: clone}
	if h.Validate() != nil || !h.Clone().Equal(h) {
		t.Fatal("valid harness")
	}
	p := session.PreparedPrompt{Harness: &h}
	pc := p.Clone()
	h.Content[1].Media.Source.Inline.Data[0] = 0
	if pc.Equal(p) || pc.Validate() != nil {
		t.Fatal("harness clone aliases")
	}
	index := uint64(0)
	authored := session.PreparedPrompt{AuthoredMessage: &index}
	ac := authored.Clone()
	index = 1
	if ac.Equal(authored) || ac.Validate() != nil || *ac.AuthoredMessage != 0 {
		t.Fatal("authored pointer clone")
	}
	for _, p := range []session.PreparedPrompt{{}, {AuthoredMessage: &index, Harness: &h}, {Harness: &session.HarnessPrompt{Purpose: "system", Source: clone}}, {Harness: &session.HarnessPrompt{Purpose: session.PromptInstructions}}} {
		if p.Validate() == nil {
			t.Fatal("invalid prepared prompt validated")
		}
	}
	message := session.AuthoredMessage{Content: richContent()}
	mc := message.Clone()
	message.Content[1].Media.Source.Inline.Data[0] = 0
	if mc.Equal(message) || mc.Validate() != nil {
		t.Fatal("authored ownership")
	}
	block := session.InstructionBlock{Content: richContent(), Source: clone}
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
