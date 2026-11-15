package modelrequest

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// requestProjection uses the existing codec budget on the actual output schema.
// Containers reserve their exact child counts before allocation; leaves are
// admitted before copying. A payload-copy-free sizing pass precedes materialization.
// It never charges unused history against output limits. Final generated validation
// and encoding independently check the complete result.
type requestProjection struct {
	budget      codec.Budget
	limits      codec.Limits
	err         error
	materialize bool
	// Generated string checks require actual strings. During sizing only, JSON
	// and not-yet-formatted metadata visit an empty string node and separately
	// account their exact UTF-8 byte lengths, without allocating those strings.
	unformattedTextBytes int
}

func (p *requestProjection) fields(depth int, keys ...string) {
	if p.err != nil {
		return
	}
	p.err = p.budget.CheckMap(len(keys), depth, p.limits.MaxMapPairs)
	for _, key := range keys {
		p.text(key, depth+1)
	}
}

func (p *requestProjection) array(n, depth int) bool {
	if p.err == nil {
		p.err = p.budget.CheckArray(n, depth, p.limits.MaxArrayElements)
	}
	return p.err == nil
}

func (p *requestProjection) text(v string, depth int) {
	if p.err == nil {
		p.err = p.budget.CheckString(v, depth, p.limits.MaxStringBytes)
	}
}

func (p *requestProjection) bytes(n, depth int) {
	if p.err == nil {
		p.err = p.budget.CheckBytes(n, depth, p.limits.MaxByteStringBytes)
	}
}

// textAllocation checks the remaining byte budget before JSON conversion or
// metadata formatting. The containing map already reserved the scalar node.
func (p *requestProjection) textAllocation(n int) bool {
	if p.err == nil && (n > p.limits.MaxStringBytes || n > p.limits.MaxBytes-p.budget.EstimatedBytes()) {
		p.err = codec.ErrLimitExceeded
	}
	return p.err == nil
}

// unformattedText admits one trusted retained/quoted UTF-8 string without
// constructing it. Its scalar node is counted by Budget; finish includes its
// payload bytes in the same MaxBytes estimate. Used only during sizing.
func (p *requestProjection) unformattedText(n, depth int) {
	p.text("", depth)
	if p.err != nil {
		return
	}
	if n > p.limits.MaxStringBytes || n > p.limits.MaxBytes-p.unformattedTextBytes {
		p.err = codec.ErrLimitExceeded
		return
	}
	p.unformattedTextBytes += n
}

func (p *requestProjection) finish() error {
	if p.err == nil && p.unformattedTextBytes > p.limits.MaxBytes-p.budget.EstimatedBytes() {
		p.err = codec.ErrLimitExceeded
	}
	return p.err
}

func (p *requestProjection) appendPart(out []sessionformat.ContentPart, part sessionformat.ContentPart) []sessionformat.ContentPart {
	if !p.materialize || p.err != nil {
		return out
	}
	return append(out, part)
}

func (p *requestProjection) textPart(v string, depth int) sessionformat.ContentPart {
	p.fields(depth, "t")
	p.text(v, depth+1)
	if p.err != nil || !p.materialize {
		return sessionformat.ContentPart{}
	}
	return sessionformat.ContentPart{Text: &v}
}

func (p *requestProjection) content(v session.ContentPartView, depth int) sessionformat.ContentPart {
	if text, ok := v.Text(); ok {
		return p.textPart(text, depth)
	}
	m, ok := v.Media()
	if !ok {
		p.err = errors.New("content has no branch")
		return sessionformat.ContentPart{}
	}
	p.fields(depth, "m")
	p.fields(depth+1, "k", "s", "d", "u", "p", "o", "r", "m", "f", "n", "t")
	p.text(string(m.Kind()), depth+2)
	p.text(string(m.Source()), depth+2)
	p.bytes(m.InlineData().Len(), depth+2)
	for _, text := range []string{m.RemoteURI(), m.ProviderName(), m.ProviderScope(), m.ProviderReferenceID(), m.MIMEType(), m.FormatHint(), m.Filename(), m.Title()} {
		p.text(text, depth+2)
	}
	if p.err != nil || !p.materialize {
		return sessionformat.ContentPart{}
	}
	owned := sessionformat.Media{Kind: m.Kind(), Source: m.Source(), InlineData: m.InlineData().Copy(), RemoteURI: m.RemoteURI(), ProviderName: m.ProviderName(), ProviderScope: m.ProviderScope(), ProviderReferenceID: m.ProviderReferenceID(), MIMEType: m.MIMEType(), FormatHint: m.FormatHint(), Filename: m.Filename(), Title: m.Title()}
	return sessionformat.ContentPart{Media: &owned}
}

func (p *requestProjection) parts(v views.SliceView[sessionformat.ContentPart, session.ContentPartView], frame string, depth int) []sessionformat.ContentPart {
	n := nonemptyParts(v)
	if frame != "" && n != 0 {
		if n > p.limits.MaxArrayElements-2 {
			p.err = codec.ErrLimitExceeded
			return nil
		}
		n += 2
	}
	if !p.array(n, depth) {
		return nil
	}
	opening, closing := "<supplemental-context>\n", "\n</supplemental-context>"
	if frame == "supplemental-instructions" {
		opening, closing = "<supplemental-instructions>\n", "\n</supplemental-instructions>"
	}
	var out []sessionformat.ContentPart
	if p.materialize {
		out = make([]sessionformat.ContentPart, 0, n)
	}
	if frame != "" && n != 0 {
		part := p.textPart(opening, depth+1)
		if p.materialize {
			out = append(out, part)
		}
	}
	for i := 0; i < v.Len() && p.err == nil; i++ {
		part := v.At(i)
		if text, ok := part.Text(); ok && text == "" {
			continue
		}
		projected := p.content(part, depth+1)
		if p.materialize {
			out = append(out, projected)
		}
	}
	if frame != "" && n != 0 && p.err == nil {
		part := p.textPart(closing, depth+1)
		if p.materialize {
			out = append(out, part)
		}
	}
	return out
}

func (p *requestProjection) blocks(v views.SliceView[sessionformat.InstructionBlock, session.InstructionBlockView], depth int) []format.ContentBlock {
	n := 0
	for i := 0; i < v.Len(); i++ {
		if nonemptyParts(v.At(i).Parts()) != 0 {
			n++
		}
	}
	if !p.array(n, depth) {
		return nil
	}
	var out []format.ContentBlock
	if p.materialize {
		out = make([]format.ContentBlock, 0, n)
	}
	for i := 0; i < v.Len() && p.err == nil; i++ {
		parts := v.At(i).Parts()
		if nonemptyParts(parts) == 0 {
			continue
		}
		p.fields(depth+1, "p")
		block := format.ContentBlock{Parts: p.parts(parts, "", depth+2)}
		if p.materialize {
			out = append(out, block)
		}
	}
	return out
}

func (s *chronologicalSelection) conversationCount(maximum int) (int, error) {
	n := 0
	add := func(k int) error {
		if k > maximum-n {
			return codec.ErrLimitExceeded
		}
		n += k
		return nil
	}
	for r := range s.source.Records() {
		if input, ok := r.EffectiveInput(); ok {
			for i := 0; i < input.Inputs().Len(); i++ {
				parts, _, err := s.inputParts(input.Inputs().At(i))
				if err != nil {
					return 0, err
				}
				if nonemptyParts(parts) != 0 {
					if err := add(1); err != nil {
						return 0, err
					}
				}
			}
		}
		if response, ok := r.ModelResponse(); ok {
			_, private := s.private(response)
			if assistantItemCount(response) != 0 || private {
				if err := add(1); err != nil {
					return 0, err
				}
			}
			if err := add(response.CallCount()); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (s *chronologicalSelection) project(limits codec.Limits) (format.RequestV1, error) {
	if _, err := s.projectOutput(limits, false); err != nil {
		return format.RequestV1{}, err
	}
	return s.projectOutput(limits, true)
}

// projectOutput walks the concrete chronological-v1 output schema. Sizing and
// materialization use the same selection and counts, with no intermediate views
// or output-shaped plan. Only source-bounded association sets survive traversal.
func (s *chronologicalSelection) projectOutput(limits codec.Limits, materialize bool) (format.RequestV1, error) {
	budget, err := codec.NewBudget(limits)
	if err != nil {
		return format.RequestV1{}, err
	}
	p := requestProjection{budget: budget, limits: limits, materialize: materialize}
	p.fields(0, "a", "m", "s", "r", "p", "t", "c")
	out := format.RequestV1{API: s.binding.API(), Model: s.binding.Model()}
	p.text(out.API, 1)
	p.text(out.Model, 1)
	if value, ok := s.binding.Settings().ReasoningEffort(); ok {
		out.Settings.ReasoningEffort = &value
	}
	if value, ok := s.binding.Settings().MaxOutputTokens(); ok {
		out.Settings.MaxOutputTokens = &value
	}
	if p.err == nil {
		p.err = out.Settings.CheckCBOR(&p.budget, 1, limits.MaxMapPairs)
	}
	out.RootInstructions = p.blocks(s.root.Blocks(), 1)
	out.UserContextPrefix = p.blocks(s.prefix.Blocks(), 1)
	if p.array(s.catalog.Tools().Len(), 1) {
		if p.materialize {
			out.Tools = make([]sessionformat.ToolDefinition, 0, s.catalog.Tools().Len())
		}
		for i := 0; i < s.catalog.Tools().Len() && p.err == nil; i++ {
			tool := s.catalog.Tools().At(i)
			p.fields(2, "n", "d", "s")
			p.text(tool.Name(), 3)
			p.text(tool.Description(), 3)
			p.bytes(tool.ArgumentSchema().Len(), 3)
			if p.err == nil && p.materialize {
				out.Tools = append(out.Tools, sessionformat.ToolDefinition{Name: tool.Name(), Description: tool.Description(), ArgumentSchema: tool.ArgumentSchema().Copy()})
			}
		}
	}
	if p.err != nil {
		return format.RequestV1{}, p.err
	}
	out.Conversation = s.conversation(&p)
	if err := p.finish(); err != nil {
		return format.RequestV1{}, err
	}
	return out, nil
}

// conversation projects effective contributions and response/result groups in
// chronological order, with the same budget traversal in both passes.
func (s *chronologicalSelection) conversation(p *requestProjection) []format.ConversationItem {
	n, err := s.conversationCount(p.limits.MaxArrayElements)
	if err != nil {
		p.err = err
		return nil
	}
	if !p.array(n, 1) {
		return nil
	}
	var out []format.ConversationItem
	if p.materialize {
		out = make([]format.ConversationItem, 0, n)
	}
	for r := range s.source.Records() {
		if p.err != nil {
			break
		}
		if input, ok := r.EffectiveInput(); ok {
			for i := 0; i < input.Inputs().Len() && p.err == nil; i++ {
				parts, frame, err := s.inputParts(input.Inputs().At(i))
				if err != nil {
					p.err = err
					return nil
				}
				if nonemptyParts(parts) == 0 {
					continue
				}
				p.fields(2, "u")
				p.fields(3, "p")
				projectedParts := p.parts(parts, frame, 4)
				if p.materialize {
					out = append(out, format.ConversationItem{User: &format.UserMessage{Parts: projectedParts}})
				}
			}
		}
		if response, ok := r.ModelResponse(); ok {
			_, private := s.private(response)
			if assistantItemCount(response) != 0 || private {
				assistant := s.assistant(p, response)
				if p.materialize {
					out = append(out, format.ConversationItem{Assistant: assistant})
				}
			}
			for call := range response.Calls() {
				if p.err != nil {
					break
				}
				result := s.toolResult(p, call)
				if p.materialize {
					out = append(out, format.ConversationItem{ToolResult: result})
				}
			}
		}
	}
	return out
}

func (s *chronologicalSelection) assistant(p *requestProjection, v session.ModelResponseView) *format.AssistantMessage {
	p.fields(2, "a")
	private, hasPrivate := s.private(v)
	if hasPrivate {
		p.fields(3, "i", "p")
	} else {
		p.fields(3, "i")
	}
	if !p.array(assistantItemCount(v), 4) {
		return nil
	}
	out := &format.AssistantMessage{}
	if p.materialize {
		out.Items = make([]format.AssistantItem, 0, assistantItemCount(v))
	}
	seen := make(map[string]bool)
	for i := 0; i < v.Items().Len() && p.err == nil; i++ {
		item := v.Items().At(i)
		if call, ok := item.Call(); ok {
			if call.ProviderCallID() == "" || seen[call.ProviderCallID()] {
				p.err = errors.New("missing or duplicate response provider call ID")
				break
			}
			seen[call.ProviderCallID()] = true
			p.fields(5, "c")
			p.fields(6, "i", "n", "a")
			p.text(call.ProviderCallID(), 7)
			p.text(call.Name(), 7)
			p.bytes(call.Arguments().Len(), 7)
			if p.err == nil && p.materialize {
				out.Items = append(out.Items, format.AssistantItem{Call: &format.ToolCall{ProviderCallID: call.ProviderCallID(), Name: call.Name(), Arguments: call.Arguments().Copy()}})
			}
			continue
		}
		content, ok := item.Content()
		if !ok || content.Kind() == sessionformat.ResponseContentReasoning || nonemptyParts(content.Parts()) == 0 {
			continue
		}
		key := "o"
		if content.Kind() == sessionformat.ResponseContentRefusal {
			key = "r"
		}
		p.fields(5, key)
		p.fields(6, "p")
		block := &format.ContentBlock{Parts: p.parts(content.Parts(), "", 7)}
		if p.materialize {
			if key == "o" {
				out.Items = append(out.Items, format.AssistantItem{Output: block})
			} else {
				out.Items = append(out.Items, format.AssistantItem{Refusal: block})
			}
		}
	}
	if hasPrivate && p.err == nil {
		out.Private = p.privateEnvelope(private)
	}
	return out
}

// privateEnvelope budgets the selected whole envelope before copying its bytes.
func (p *requestProjection) privateEnvelope(private session.OpaqueProviderDataView) *sessionformat.OpaqueProviderData {
	p.fields(4, "p", "f", "v", "d")
	p.text(private.Producer(), 5)
	p.text(private.Format(), 5)
	if p.err == nil {
		p.err = p.budget.CheckScalar(5)
	}
	p.bytes(private.Data().Len(), 5)
	if p.err != nil || !p.materialize {
		return nil
	}
	return &sessionformat.OpaqueProviderData{Producer: private.Producer(), Format: private.Format(), Version: private.Version(), Data: private.Data().Copy()}
}
