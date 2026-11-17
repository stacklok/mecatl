package modelrequest

import (
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// project materializes owned request content from the selected immutable views.
// Final encoding validates the complete request and enforces codec limits.
func (s *chronologicalSelection) project() (format.RequestV1, error) {
	out := format.RequestV1{
		API:              s.binding.API(),
		Model:            s.binding.Model(),
		Settings:         s.binding.Settings().MutableClone(),
		RootInstructions: projectBlocks(s.root.Blocks()),
		UserInstructions: projectBlocks(s.user.Blocks()),
	}
	for i := 0; i < s.catalog.Tools().Len(); i++ {
		out.Tools = append(out.Tools, s.catalog.Tools().At(i).MutableClone())
	}
	conversation, err := s.conversation()
	if err != nil {
		return format.RequestV1{}, err
	}
	out.Conversation = conversation
	return out, nil
}

// projectParts copies nonempty content and optionally frames an inline contribution.
func projectParts(v views.SliceView[sessionformat.ContentPart, session.ContentPartView], frame string) []sessionformat.ContentPart {
	var out []sessionformat.ContentPart
	for i := 0; i < v.Len(); i++ {
		part := v.At(i)
		if text, ok := part.Text(); ok && text == "" {
			continue
		}
		if len(out) == 0 && frame != "" {
			out = append(out, textPart("<"+frame+">\n"))
		}
		out = append(out, part.MutableClone())
	}
	if len(out) != 0 && frame != "" {
		out = append(out, textPart("\n</"+frame+">"))
	}
	return coalesceTextParts(out)
}

// coalesceTextParts joins adjacent text without inserting separators. Media parts
// retain their order and split text runs. Input parts are owned and are not mutated.
func coalesceTextParts(parts []sessionformat.ContentPart) []sessionformat.ContentPart {
	var out []sessionformat.ContentPart
	var text strings.Builder
	flush := func() {
		if text.Len() != 0 {
			out = append(out, textPart(text.String()))
			text.Reset()
		}
	}
	for _, part := range parts {
		if part.Text != nil {
			text.WriteString(*part.Text)
		} else {
			flush()
			out = append(out, part)
		}
	}
	flush()
	return out
}

func textPart(text string) sessionformat.ContentPart {
	return sessionformat.ContentPart{Text: &text}
}

func projectBlocks(v views.SliceView[sessionformat.InstructionBlock, session.InstructionBlockView]) []format.ContentBlock {
	var out []format.ContentBlock
	for i := 0; i < v.Len(); i++ {
		if parts := projectParts(v.At(i).Parts(), ""); len(parts) != 0 {
			out = append(out, format.ContentBlock{Parts: parts})
		}
	}
	return out
}

// conversation projects effective contributions and response/result groups in
// chronological order. Tool results follow their response in requested-call order,
// regardless of where the corresponding settlement facts occur in the source.
func (s *chronologicalSelection) conversation() ([]format.ConversationItem, error) {
	var out []format.ConversationItem
	for r := range s.source.Records() {
		if input, ok := r.EffectiveInput(); ok {
			for i := 0; i < input.Inputs().Len(); i++ {
				parts, frame, err := s.inputParts(input.Inputs().At(i))
				if err != nil {
					return nil, err
				}
				if projected := projectParts(parts, frame); len(projected) != 0 {
					out = append(out, format.ConversationItem{User: &format.UserMessage{Parts: projected}})
				}
			}
		}
		if response, ok := r.ModelResponse(); ok {
			assistant, err := s.assistant(response)
			if err != nil {
				return nil, err
			}
			if len(assistant.Items) != 0 || assistant.Private != nil {
				out = append(out, format.ConversationItem{Assistant: assistant})
			}
			for call := range response.Calls() {
				result, err := s.toolResult(call)
				if err != nil {
					return nil, err
				}
				out = append(out, format.ConversationItem{ToolResult: result})
			}
		}
	}
	return out, nil
}

func (s *chronologicalSelection) assistant(v session.ModelResponseView) (*format.AssistantMessage, error) {
	out := &format.AssistantMessage{}
	seen := make(map[string]bool)
	for i := 0; i < v.Items().Len(); i++ {
		item := v.Items().At(i)
		if call, ok := item.Call(); ok {
			if call.ProviderCallID() == "" || seen[call.ProviderCallID()] {
				return nil, errors.New("missing or duplicate response provider call ID")
			}
			seen[call.ProviderCallID()] = true
			out.Items = append(out.Items, format.AssistantItem{Call: &format.ToolCall{ProviderCallID: call.ProviderCallID(), Name: call.Name(), Arguments: call.Arguments().Copy()}})
			continue
		}
		content, ok := item.Content()
		if !ok || content.Kind() == sessionformat.ResponseContentReasoning {
			continue
		}
		parts := projectParts(content.Parts(), "")
		if len(parts) == 0 {
			continue
		}
		block := &format.ContentBlock{Parts: parts}
		if content.Kind() == sessionformat.ResponseContentRefusal {
			out.Items = append(out.Items, format.AssistantItem{Refusal: block})
		} else {
			out.Items = append(out.Items, format.AssistantItem{Output: block})
		}
	}
	if private, ok := s.private(v); ok {
		owned := private.MutableClone()
		out.Private = &owned
	}
	return out, nil
}
