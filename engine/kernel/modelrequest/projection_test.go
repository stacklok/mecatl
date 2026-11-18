package modelrequest

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestCoalesceTextParts(t *testing.T) {
	media := session.ContentPart{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, InlineData: []byte{1, 2, 3}, MIMEType: "image/png"}}
	for _, tc := range []struct {
		name     string
		in, want []session.ContentPart
	}{
		{"nil", nil, nil},
		{"empty", textParts("", ""), nil},
		{"exact bytes", textParts("", "α", " \n", "尾", ""), textParts("α \n尾")},
		{"media only", []session.ContentPart{media, media}, []session.ContentPart{media, media}},
		{"media boundaries", append(append(textParts("a", "b"), media, media), textParts("", "c", "d")...), append(append(textParts("ab"), media, media), textParts("cd")...)},
		{"leading trailing media", append(append([]session.ContentPart{media}, textParts("a", "", "b")...), media), append(append([]session.ContentPart{media}, textParts("ab")...), media)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := (format.ContentBlock{Parts: tc.in}).Clone()
			got := coalesceTextParts(tc.in)
			if !(format.ContentBlock{Parts: got}).Equal(format.ContentBlock{Parts: tc.want}) {
				t.Fatalf("coalesced parts: %#v; want %#v", got, tc.want)
			}
			if !(format.ContentBlock{Parts: tc.in}).Equal(before) {
				t.Fatal("coalescing mutated input")
			}
		})
	}
}

func TestConstructCoalescesWithinContentSequences(t *testing.T) {
	media := session.ContentPart{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, InlineData: []byte{1, 2, 3}, MIMEType: "image/png"}}
	var previous contentid.CID
	for _, split := range []bool{false, true} {
		name := "whole"
		if split {
			name = "split"
		}
		t.Run(name, func(t *testing.T) {
			parts := func() []session.ContentPart {
				if split {
					return textParts("", "a", "b", "")
				}
				return textParts("ab")
			}
			source := session.ContentSource{Description: "prepared test content"}
			blocks := []session.InstructionBlock{{Key: "first", Parts: parts(), Source: source}, {Key: "second", Parts: parts(), Source: source}}
			f := newConstructionFixture(t)
			requireConstruction(t, f.b.ReplaceRootInstructions(f.run.HistoryID(), session.RootInstructions{Blocks: blocks}))
			requireConstruction(t, f.b.ReplaceUserInstructions(f.run.HistoryID(), session.UserInstructions{Blocks: blocks}))
			inputParts := append(parts(), media)
			inputParts = append(inputParts, parts()...)
			input, err := f.b.AddUserInput(f.run, session.UserInput{Parts: inputParts})
			requireConstruction(t, err)
			requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{
				{UserInputID: new(input.RunRelative())},
				{Inline: &session.AttributedInput{Actor: "prepared", Purpose: session.PromptContext, Parts: inputParts, Source: source}},
				inline(session.PromptInstructions, "", ""),
			}}))
			response := f.response([]session.ResponseItem{
				{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: parts()}},
				{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: parts()}},
				{Content: &session.ResponseContent{Kind: session.ResponseContentRefusal, Parts: parts()}},
				{Call: &session.RequestedCall{ProviderCallID: "first"}},
				{Call: &session.RequestedCall{ProviderCallID: "second"}},
			}, nil)
			for call := range response.Calls() {
				requireConstruction(t, f.b.AddToolDisposition(call.ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedUserDenied}}))
			}
			built := f.build(format.ParametersV1{})
			block := format.ContentBlock{Parts: textParts("ab")}
			want := format.RequestV1{API: "api", Model: "model",
				RootInstructions: []format.ContentBlock{block, block}, UserInstructions: []format.ContentBlock{block, block},
				Conversation: []format.ConversationItem{
					{User: &format.UserMessage{Parts: []session.ContentPart{textPart("ab"), media, textPart("ab")}}},
					{User: &format.UserMessage{Parts: []session.ContentPart{textPart("<supplemental-context>\nab"), media, textPart("ab\n</supplemental-context>")}}},
					{Assistant: &format.AssistantMessage{Items: []format.AssistantItem{
						{Output: &block}, {Output: &block}, {Refusal: &block},
						{Call: &format.ToolCall{ProviderCallID: "first"}}, {Call: &format.ToolCall{ProviderCallID: "second"}},
					}}},
					{ToolResult: &format.ToolResult{ProviderCallID: "first", Outcome: format.ToolOutcomeNotInvoked, Parts: textParts("User denied tool execution")}},
					{ToolResult: &format.ToolResult{ProviderCallID: "second", Outcome: format.ToolOutcomeNotInvoked, Parts: textParts("User denied tool execution")}},
				},
			}
			if !built.Request().MutableClone().Equal(want) {
				t.Fatalf("content boundaries changed: %#v", built.Request().MutableClone())
			}
			encoded, err := codec.Encode(want, testLimits())
			requireConstruction(t, err)
			if built.Request().CID() != contentid.Sum(encoded) || split && built.Request().CID() != previous {
				t.Fatal("text segmentation changed canonical request identity")
			}
			previous = built.Request().CID()
			capture := f.view()
			id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
			requireConstruction(t, err)
			f.submit()
			loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy, testConfig())
			requireConstruction(t, err)
			rebuilt, err := Reconstruct(loaded.Snapshot(), id, testLimits())
			requireConstruction(t, err)
			if !rebuilt.MutableClone().Equal(want) || rebuilt.CID() != built.Request().CID() {
				t.Fatal("Submit/Load/Reconstruct changed coalesced content")
			}
			standing, _ := loaded.Snapshot().RootInstructions()
			if standing.Blocks().At(0).Parts().Len() != len(parts()) || !bytes.Equal(rebuilt.MutableClone().Conversation[0].User.Parts[1].Media.InlineData, []byte{1, 2, 3}) {
				t.Fatal("projection changed retained history or media")
			}
			requireConstruction(t, f.b.Discard())
		})
	}
}
