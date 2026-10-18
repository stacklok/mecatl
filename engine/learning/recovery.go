package learning

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// ReflectionInput returns the verified source bindings behind a display projection.
// It compares the projection to a fresh canonical projection so serialized or
// modified display DTOs cannot establish evidence authority.
func (p Projection) ReflectionInput() (Input, error) {
	if p.verifiedInput == nil {
		return Input{}, ErrInvalidEvidence
	}
	canonical, err := ProjectVerifiedInput(*p.verifiedInput)
	if err != nil {
		return Input{}, err
	}
	got, err := json.Marshal(p)
	if err != nil {
		return Input{}, err
	}
	want, err := json.Marshal(canonical)
	if err != nil || !bytes.Equal(got, want) {
		return Input{}, ErrInvalidEvidence
	}
	return *p.verifiedInput, nil
}

// LegacyVerificationInput reconstructs the frozen run-input view from proven
// event-source occurrences. It is verification-only; never send it to a model or
// executable lookup. Use ProjectVerifiedInput for model-facing recovered work.
func LegacyVerificationInput(trajectory Trajectory, events []session.Event) (Input, error) {
	bindings := make(map[session.ToolCallID]string)
	var originalEvents []session.Event
	var identifiers []string
	legacy := false
	for _, event := range events {
		original, ids, err := legacyEvent(event)
		if err != nil {
			return Input{}, err
		}
		if event.LegacyIdentity() != nil {
			legacy = true
			if original.ToolCall != nil {
				callID := legacyEventAttachment(event)
				if callID == "" || original.ToolCall.ID == "" || event.ToolCall.Name != original.ToolCall.Name {
					return Input{}, ErrInvalidEvidence
				}
				if _, duplicate := bindings[callID]; duplicate {
					return Input{}, ErrInvalidEvidence
				}
				bindings[callID] = string(original.ToolCall.ID)
			}
		}
		originalEvents = append(originalEvents, original)
		identifiers = append(identifiers, ids...)
	}
	if !legacy {
		return Input{}, ErrInvalidEvidence
	}
	messages, ids, err := legacyVerificationMessages(trajectory.Messages, bindings)
	if err != nil {
		return Input{}, err
	}
	identifiers = append(identifiers, ids...)
	originalTrajectory := trajectory
	originalTrajectory.Messages = messages
	in := NewInput(originalTrajectory, originalEvents, nil, nil)
	in.Protocol = ReflectionEvidenceLegacyV0
	in.previewMessages = session.CloneMessages(trajectory.Messages)
	for _, event := range events {
		in.previewEvents = append(in.previewEvents, session.CloneEventIdentity(event))
	}
	in.legacyIdentifiers = identifiers
	return in, nil
}

func legacyVerificationMessages(source []session.Message, bindings map[session.ToolCallID]string) ([]session.Message, []string, error) {
	messages := session.CloneMessages(source)
	var identifiers []string
	for i, message := range messages {
		if message.Legacy != nil {
			if !strings.HasPrefix(message.Legacy.Namespace, "sessnap/") && len(message.Legacy.Unsupported) > 0 {
				return nil, nil, ErrInvalidEvidence
			}
			original, ids, err := legacyMessage(message)
			if err != nil {
				return nil, nil, err
			}
			messages[i] = original
			identifiers = append(identifiers, ids...)
			continue
		}
		for j, call := range message.ToolCalls {
			provider, ok := bindings[call.ID]
			if !ok {
				return nil, nil, ErrInvalidEvidence
			}
			messages[i].ToolCalls[j].ID = session.ToolCallID(provider)
		}
		if message.ToolResult != nil {
			provider, ok := bindings[message.ToolResult.CallID]
			if !ok {
				return nil, nil, ErrInvalidEvidence
			}
			messages[i].ToolResult.CallID = session.ToolCallID(provider)
		}
	}
	return messages, identifiers, nil
}

// ProjectVerifiedInput preserves frozen verification inputs privately while
// producing sanitized model-facing recovered evidence with optional proven links.
func ProjectVerifiedInput(in Input) (Projection, error) {
	projection, err := ProjectInput(in)
	if err != nil {
		return Projection{}, err
	}
	if in.Protocol == ReflectionEvidenceV2 || in.Manifest != nil && in.Manifest.Protocol == ReflectionEvidenceV2 {
		return projection, nil // ProjectInput already redacted this display view.
	}
	for i := range projection.Messages {
		item := &projection.Messages[i]
		ref, err := MessageEvidenceRef(in, i, messageToolCallID(in.Trajectory.Messages[i]))
		if err != nil {
			return Projection{}, err
		}
		citation, err := CanonicalEvidenceCitation(in, ref)
		if err != nil {
			return Projection{}, err
		}
		item.Evidence.ToolCallID = citation.ToolCallID
		for j := range item.ToolCalls {
			item.ToolCalls[j].ID = ""
		}
		if item.ToolResult != nil {
			item.ToolResult.CallID = ""
		}
	}
	for i := range projection.Events {
		item := &projection.Events[i]
		ref, err := EventEvidenceRef(in, i, eventToolCallID(in.Events[i]))
		if err != nil {
			return Projection{}, err
		}
		citation, err := CanonicalEvidenceCitation(in, ref)
		if err != nil {
			return Projection{}, err
		}
		item.Evidence.ToolCallID = citation.ToolCallID
		if item.ToolCall != nil {
			item.ToolCall.ID = ""
		}
		if item.ToolResult != nil {
			item.ToolResult.CallID = ""
		}
	}
	return redactEvidenceProjection(projection, in.legacyIdentifiers)
}

func redactEvidenceProjection(projection Projection, identifiers []string) (Projection, error) {
	for i := range projection.Messages {
		redactMessageProjection(&projection.Messages[i], identifiers)
	}
	for i := range projection.Events {
		redactEventProjection(&projection.Events[i], identifiers)
	}
	return projection, nil
}

func redactMessageProjection(message *MessageProjection, identifiers []string) {
	message.Text = redactLegacyText(message.Text, identifiers)
	for i := range message.ToolCalls {
		message.ToolCalls[i].Name = redactLegacyText(message.ToolCalls[i].Name, identifiers)
	}
	redactResultProjection(message.ToolResult, identifiers)
	redactPartProjections(message.Parts, identifiers)
}

func redactEventProjection(event *EventProjection, identifiers []string) {
	event.Text = redactLegacyText(event.Text, identifiers)
	if event.ToolCall != nil {
		event.ToolCall.Name = redactLegacyText(event.ToolCall.Name, identifiers)
	}
	redactResultProjection(event.ToolResult, identifiers)
}

func redactResultProjection(result *ResultProjection, identifiers []string) {
	if result == nil {
		return
	}
	result.Content = redactLegacyText(result.Content, identifiers)
	redactPartProjections(result.Parts, identifiers)
}

func redactPartProjections(parts []PartProjection, identifiers []string) {
	for i := range parts {
		part := &parts[i]
		part.Text = redactLegacyText(part.Text, identifiers)
		part.Name = redactLegacyText(part.Name, identifiers)
		part.Title = redactLegacyText(part.Title, identifiers)
		part.Description = redactLegacyText(part.Description, identifiers)
	}
}

func legacySourceIdentifiers(messages []session.Message, events []session.Event) []string {
	var ids []string
	decode := func(raw []byte) {
		var dto struct {
			ToolCalls []struct {
				ID     string
				ItemID string `json:"item_id"`
			} `json:"tool_calls"`
			ToolResult      *struct{ CallID string } `json:"tool_result"`
			ReasoningItemID string                   `json:"reasoning_item_id"`
			EventCall       *struct {
				ID     string
				ItemID string `json:"item_id"`
			} `json:"ToolCall"`
			EventResult *struct{ CallID string } `json:"ToolResult"`
		}
		if json.Unmarshal(raw, &dto) != nil {
			return
		}
		for _, c := range dto.ToolCalls {
			ids = append(ids, c.ID, c.ItemID)
		}
		if dto.ToolResult != nil {
			ids = append(ids, dto.ToolResult.CallID)
		}
		if dto.EventCall != nil {
			ids = append(ids, dto.EventCall.ID, dto.EventCall.ItemID)
		}
		if dto.EventResult != nil {
			ids = append(ids, dto.EventResult.CallID)
		}
		ids = append(ids, dto.ReasoningItemID)
	}
	for _, m := range messages {
		if p := m.Legacy; p != nil {
			for _, c := range p.Calls {
				ids = append(ids, c.ProviderID, c.ItemID)
			}
			ids = append(ids, p.ResultProviderID, p.ReasoningItemID)
			if len(p.Unsupported) > 0 {
				decode(p.Unsupported)
			}
		}
	}
	for _, e := range events {
		if p := e.LegacyIdentity(); p != nil {
			decode(p.Payload)
		}
	}
	return ids
}

const historicalIdentifierMarker = "[historical-identifier]"

// Replacer writes incrementally, so bound construction rather than allocating an
// expanded string and truncating it afterward. Do not embed Builder: its promoted
// WriteString would let Replacer bypass Write's limit.
type redactionWriter struct {
	out strings.Builder
}

func (w *redactionWriter) Write(p []byte) (int, error) {
	remaining := maxProjectionTextBytes - w.out.Len()
	if len(p) <= remaining {
		return w.out.Write(p)
	}
	if string(p) == historicalIdentifierMarker {
		return 0, io.ErrShortWrite
	}
	n, _ := w.out.Write(p[:remaining])
	return n, io.ErrShortWrite
}

func redactLegacyText(text string, identifiers []string) string {
	seen := map[string]bool{historicalIdentifierMarker: true}
	var keys []string
	for _, id := range identifiers {
		for _, key := range []string{id, safeProjectionText("", id)} {
			// An opaque marker must not shield a private identifier extending past
			// it (or equal to it). Omit the affected field rather than expose it.
			if key != "" && strings.Contains(text, historicalIdentifierMarker) && strings.Contains(text, key) &&
				(key == historicalIdentifierMarker || !strings.Contains(historicalIdentifierMarker, key)) {
				return ""
			}
			if key != "" && key != "[redacted-secret]" && !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	// Existing markers are opaque on later passes, including identifiers such as
	// "i" that also occur inside the marker. Replacement output is never rescanned.
	pairs := []string{historicalIdentifierMarker, historicalIdentifierMarker}
	for _, key := range keys {
		pairs = append(pairs, key, historicalIdentifierMarker)
	}
	var out redactionWriter
	_, _ = strings.NewReplacer(pairs...).WriteString(&out, text)
	return strings.ToValidUTF8(out.out.String(), "")
}
