package learning

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
)

// ManifestProtocolSupported distinguishes frozen provider-era v1 from canonical v2.
// Both versions retain the original source coordinate domain.
func ManifestProtocolSupported(protocol EvidenceProtocol, domain string) bool {
	return (protocol == ReflectionEvidenceV1 || protocol == ReflectionEvidenceV2) && domain == ReflectionEvidenceSourceV1
}

// EvidenceCitation is a public-safe verified citation. ToolCallID is optional:
// availability does not imply that an invocation attachment could be proven.
// Stored provider-era EvidenceRef identities are NOT public invocation references.
type EvidenceCitation struct {
	Protocol   EvidenceProtocol   `json:"protocol"`
	ToolCallID session.ToolCallID `json:"tool_call_id,omitempty"`
	Preview    string             `json:"preview"`
}

// SourceMessageIndex resolves frozen v0/v1 coordinates from private original
// provenance. V2 coordinates address the current canonical source trajectory;
// the canonical invocation IDs in its digest prevent provider-era aliasing.
func SourceMessageIndex(messages []session.Message, incarnation session.IncarnationID, ordinal int, protocol EvidenceProtocol) (int, error) {
	found := -1
	if ordinal < 0 {
		return -1, ErrInvalidEvidence
	}
	if protocol == ReflectionEvidenceV2 {
		if ordinal >= len(messages) {
			return -1, ErrInvalidEvidence
		}
		return ordinal, nil
	}
	if protocol != ReflectionEvidenceLegacyV0 && protocol != ReflectionEvidenceV1 {
		return -1, ErrInvalidEvidence
	}
	for i, m := range messages {
		if m.Legacy == nil || m.Legacy.Incarnation != incarnation || m.Legacy.Namespace != "sessnap/legacy-v1" || m.Legacy.Ordinal != ordinal {
			continue
		}
		if found >= 0 {
			return -1, fmt.Errorf("%w: ambiguous source coordinate", ErrInvalidEvidence)
		}
		found = i
	}
	if found >= 0 {
		return found, nil
	}
	return -1, fmt.Errorf("%w: source coordinate unavailable", ErrInvalidEvidence)
}

// SourceEventOrdinal uses the original event-only iterator ordinal, never Seq or
// a physical cursor. A carried origin must belong to this source incarnation;
// an unknown legacy ordinal is unaddressable (-1, nil), never a retained index.
func SourceEventOrdinal(event session.Event, incarnation session.IncarnationID, ordinal int) (int, error) {
	if p := event.LegacyIdentity(); p != nil {
		if p.Incarnation != "" && p.Incarnation != incarnation {
			return -1, ErrInvalidEvidence
		}
		if p.Ordinal < -1 {
			return -1, ErrInvalidEvidence
		}
		return p.Ordinal, nil
	}
	return ordinal, nil
}

func legacyMessage(message session.Message) (session.Message, []string, error) {
	p := message.Legacy
	if p == nil {
		return message, nil, nil
	}
	ids := []string{p.ReasoningItemID}
	if len(p.Unsupported) != 0 {
		// Only the frozen digest projection fields are decoded. Replay never becomes
		// verification content or a preview, even for an unsafe historical component.
		var dto struct {
			Role      session.Role `json:"role"`
			Text      string       `json:"text"`
			ToolCalls []struct {
				ID     string
				Name   string
				ItemID string `json:"item_id"`
			} `json:"tool_calls"`
			ReasoningItemID string `json:"reasoning_item_id"`
			ToolResult      *struct {
				CallID  string
				Content string
				IsError bool
				Parts   []session.Content
			} `json:"tool_result"`
			Parts []struct {
				Kind        session.MediaKind `json:"kind"`
				MIMEType    string            `json:"mime_type"`
				Text        string            `json:"text"`
				Name        string            `json:"name"`
				Title       string            `json:"title"`
				Description string            `json:"description"`
				Data        []byte            `json:"data"`
			} `json:"parts"`
		}
		if err := json.Unmarshal(p.Unsupported, &dto); err != nil {
			return session.Message{}, nil, ErrInvalidEvidence
		}
		ids = append(ids, dto.ReasoningItemID)
		message = session.Message{Role: dto.Role, Text: dto.Text}
		for _, c := range dto.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, session.ToolCall{ID: session.ToolCallID(c.ID), Name: c.Name})
			ids = append(ids, c.ID, c.ItemID)
		}
		if r := dto.ToolResult; r != nil {
			message.ToolResult = &session.ToolResult{CallID: session.ToolCallID(r.CallID), Content: r.Content, IsError: r.IsError, Parts: r.Parts}
			ids = append(ids, r.CallID)
		}
		for _, part := range dto.Parts {
			message.Parts = append(message.Parts, session.Content{Kind: part.Kind, MIMEType: part.MIMEType, Text: part.Text, Name: part.Name, Title: part.Title, Description: part.Description, Data: part.Data})
		}
		return message, ids, nil
	}
	message = session.CloneMessages([]session.Message{message})[0]
	message.Legacy, message.Replay = nil, nil
	for i, c := range message.ToolCalls {
		message.ToolCalls[i].Args = nil
		found := false
		for _, old := range p.Calls {
			if old.CallID == c.ID {
				message.ToolCalls[i].ID = session.ToolCallID(old.ProviderID)
				ids = append(ids, old.ProviderID, old.ItemID)
				found = true
				break
			}
		}
		if !found {
			return session.Message{}, nil, ErrInvalidEvidence
		}
	}
	if message.ToolResult != nil {
		message.ToolResult.CallID = session.ToolCallID(p.ResultProviderID)
		ids = append(ids, p.ResultProviderID)
	}
	return message, ids, nil
}

func legacyEvent(event session.Event) (session.Event, []string, error) {
	p := event.LegacyIdentity()
	if p == nil {
		return event, nil, nil
	}
	var dto struct {
		Type     session.EventType
		Seq      int64
		Turn     int
		Text     string
		ToolCall *struct {
			ID     string
			Name   string
			ItemID string `json:"item_id"`
		}
		ToolResult *struct {
			CallID  string
			Content string
			IsError bool
			Parts   []session.Content
		}
		Result *session.ResultPayload
	}
	if err := json.Unmarshal(p.Payload, &dto); err != nil {
		return session.Event{}, nil, ErrInvalidEvidence
	}
	out := session.Event{Type: dto.Type, Seq: dto.Seq, Turn: dto.Turn, Text: dto.Text, Result: dto.Result}
	var ids []string
	if c := dto.ToolCall; c != nil {
		out.ToolCall = &session.ToolCall{ID: session.ToolCallID(c.ID), Name: c.Name}
		ids = append(ids, c.ID, c.ItemID)
	}
	if r := dto.ToolResult; r != nil {
		out.ToolResult = &session.ToolResult{CallID: session.ToolCallID(r.CallID), Content: r.Content, IsError: r.IsError, Parts: r.Parts}
		ids = append(ids, r.CallID)
	}
	return out, ids, nil
}

// RematerializeEvidence verifies a stored manifest against retained ORIGINAL
// sources. It neither rewrites the manifest nor recovers missing/pruned content.
// For a sparse event read, eventOrdinals supplies the actual original event-only
// iterator ordinals. Omit it only for a complete source sequence.
func RematerializeEvidence(trajectory Trajectory, events []session.Event, incarnation session.IncarnationID, manifest MaterializationManifest, eventOrdinals ...[]int) (Input, error) {
	if !ManifestProtocolSupported(manifest.Protocol, manifest.Source.Domain) || manifest.Source.Value != trajectory.SessionID || len(manifest.Entries) > MaxInputMessages+MaxInputEvents {
		return Input{}, ErrInvalidEvidence
	}
	var messages []session.Message
	var selectedEvents []session.Event
	var safeMessages []session.Message
	var safeEvents []session.Event
	var ids []string
	for _, entry := range manifest.Entries {
		switch entry.Locator {
		case EvidenceMessage:
			if len(messages) >= MaxInputMessages {
				return Input{}, ErrInvalidEvidence
			}
			m, safe, more, err := manifestSourceMessage(trajectory.Messages, incarnation, entry, manifest.Protocol)
			if err != nil {
				return Input{}, err
			}
			safeMessages = append(safeMessages, safe)
			ids = append(ids, more...)
			messages = append(messages, m)
		case EvidenceEvent:
			if len(selectedEvents) >= MaxInputEvents {
				return Input{}, ErrInvalidEvidence
			}
			if entry.EventSequence == nil {
				return Input{}, ErrInvalidEvidence
			}
			found, err := sourceEventIndex(events, incarnation, *entry.EventSequence, eventOrdinals...)
			if err != nil {
				return Input{}, err
			}
			e := events[found]
			safeEvents = append(safeEvents, e)
			if manifest.Protocol == ReflectionEvidenceV1 {
				if e.LegacyIdentity() == nil {
					return Input{}, ErrInvalidEvidence
				}
				var more []string
				var err error
				e, more, err = legacyEvent(e)
				if err != nil {
					return Input{}, err
				}
				ids = append(ids, more...)
			}
			selectedEvents = append(selectedEvents, e)
		default:
			return Input{}, ErrInvalidEvidence
		}
	}
	in := NewInput(NewTrajectory(trajectory.SessionID, trajectory.Workspace, trajectory.Stop, trajectory.Usage, messages), selectedEvents, nil, nil)
	// v1 materialization deliberately omitted event text. Preserve that frozen rule.
	for i := range in.Events {
		in.Events[i].Text = ""
	}
	manifest.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		entry.ToolCallIDs = append([]session.ToolCallID(nil), entry.ToolCallIDs...)
		if entry.OriginalMessage != nil {
			coordinate := *entry.OriginalMessage
			entry.OriginalMessage = &coordinate
		}
		if entry.EventSequence != nil {
			coordinate := *entry.EventSequence
			entry.EventSequence = &coordinate
		}
	}
	in.Protocol = manifest.Protocol
	in.Manifest = &manifest
	in.previewMessages = session.CloneMessages(safeMessages)
	in.previewEvents = safeEvents
	in.legacyIdentifiers = append(ids, legacySourceIdentifiers(safeMessages, safeEvents)...)
	if err := ValidateInput(in); err != nil {
		return Input{}, err
	}
	return in, nil
}

func manifestSourceMessage(messages []session.Message, incarnation session.IncarnationID, entry ManifestEntry, protocol EvidenceProtocol) (session.Message, session.Message, []string, error) {
	if entry.OriginalMessage == nil {
		return session.Message{}, session.Message{}, nil, ErrInvalidEvidence
	}
	index, err := SourceMessageIndex(messages, incarnation, *entry.OriginalMessage, protocol)
	if err != nil {
		return session.Message{}, session.Message{}, nil, err
	}
	safe := messages[index]
	if protocol == ReflectionEvidenceV1 {
		message, ids, err := legacyMessage(safe)
		return message, safe, ids, err
	}
	return safe, safe, nil, nil
}

func sourceEventIndex(events []session.Event, incarnation session.IncarnationID, coordinate int64, eventOrdinals ...[]int) (int, error) {
	found := -1
	for i, event := range events {
		sourceIndex := i
		if len(eventOrdinals) > 0 {
			if len(eventOrdinals[0]) != len(events) {
				return -1, ErrInvalidEvidence
			}
			sourceIndex = eventOrdinals[0][i]
		}
		ordinal, err := SourceEventOrdinal(event, incarnation, sourceIndex)
		if err != nil {
			return -1, err
		}
		if ordinal >= 0 && int64(ordinal) == coordinate {
			if found >= 0 {
				return -1, ErrInvalidEvidence
			}
			found = i
		}
	}
	if found < 0 {
		return -1, ErrInvalidEvidence
	}
	return found, nil
}

// VerifyEvidenceSource verifies a single citation when no aggregate manifest is
// available (legacy-v0). Manifest-backed approval must use RematerializeEvidence.
func VerifyEvidenceSource(trajectory Trajectory, events []session.Event, incarnation session.IncarnationID, ref EvidenceRef, eventOrdinals ...[]int) (EvidenceCitation, error) {
	protocol := ref.ResolvedProtocol()
	if protocol != ReflectionEvidenceLegacyV0 && protocol != ReflectionEvidenceV1 && protocol != ReflectionEvidenceV2 {
		return EvidenceCitation{}, ErrInvalidEvidence
	}
	if ref.SessionID != trajectory.SessionID {
		return EvidenceCitation{}, ErrInvalidEvidence
	}
	var m session.Message
	var e session.Event
	var ids []string
	var err error
	switch ref.Locator {
	case EvidenceMessage:
		m, err = evidenceSourceMessage(trajectory.Messages, incarnation, ref)
	case EvidenceEvent:
		e, err = evidenceSourceEvent(events, incarnation, ref, eventOrdinals...)
	default:
		return EvidenceCitation{}, ErrInvalidEvidence
	}
	if err != nil {
		return EvidenceCitation{}, err
	}
	safeM, safeE := m, e
	if protocol != ReflectionEvidenceV2 {
		if ref.Locator == EvidenceMessage {
			m, ids, err = legacyMessage(m)
		} else {
			if e.LegacyIdentity() == nil {
				return EvidenceCitation{}, ErrInvalidEvidence
			}
			e, ids, err = legacyEvent(e)
		}
		if err != nil {
			return EvidenceCitation{}, err
		}
	}
	in := NewInput(NewTrajectory(trajectory.SessionID, "", trajectory.Stop, trajectory.Usage, []session.Message{m}), []session.Event{e}, nil, nil)
	if protocol == ReflectionEvidenceLegacyV0 {
		restoreDirectLegacyIdentities(&in, m, e, ref.ToolCallID)
	}
	in.Protocol = ReflectionEvidenceLegacyV0
	local := ref
	local.Protocol = ReflectionEvidenceLegacyV0
	local.Ordinal = 0
	local.ManifestIndex = 0
	local.OriginalMessage = nil
	local.AggregateDigest = ""
	if (protocol == ReflectionEvidenceV1 || ref.AggregateDigest != "") && local.Locator == EvidenceEvent {
		in.Events[0].Text = ""
		seq := e.Seq
		local.EventSeq = &seq
	}
	if protocol == ReflectionEvidenceV2 {
		in.Protocol = ReflectionEvidenceV2
		local.Protocol = ReflectionEvidenceV2
	}
	in.previewMessages = evidencePreviewMessages(trajectory.Messages, incarnation, safeM)
	in.previewEvents = []session.Event{safeE}
	in.legacyIdentifiers = append(ids, legacySourceIdentifiers(in.previewMessages, in.previewEvents)...)
	citation, err := CanonicalEvidenceCitation(in, local)
	citation.Protocol = protocol
	return citation, err
}

func restoreDirectLegacyIdentities(in *Input, message session.Message, event session.Event, callID session.ToolCallID) {
	// Direct v0 Inputs were also accepted: compare IDs before the frozen
	// projection's credential repair, without converting them to lookup IDs.
	if callID == "" || messageHasCall(message, callID) {
		in.Trajectory.Messages = []session.Message{message}
	}
	if event.ToolCall != nil && event.ToolCall.ID == callID {
		in.Events[0].ToolCall.ID = event.ToolCall.ID
	}
	if event.ToolResult != nil && event.ToolResult.CallID == callID {
		in.Events[0].ToolResult.CallID = event.ToolResult.CallID
	}
}

func evidenceSourceMessage(messages []session.Message, incarnation session.IncarnationID, ref EvidenceRef) (session.Message, error) {
	coordinate := ref.Ordinal
	if ref.ResolvedProtocol() == ReflectionEvidenceV1 || ref.OriginalMessage != nil {
		if ref.OriginalMessage == nil {
			return session.Message{}, ErrInvalidEvidence
		}
		coordinate = *ref.OriginalMessage
	}
	index, err := SourceMessageIndex(messages, incarnation, coordinate, ref.ResolvedProtocol())
	if err != nil {
		return session.Message{}, err
	}
	return messages[index], nil
}

func evidenceSourceEvent(events []session.Event, incarnation session.IncarnationID, ref EvidenceRef, eventOrdinals ...[]int) (session.Event, error) {
	coordinate := ref.Ordinal
	if ref.ResolvedProtocol() == ReflectionEvidenceV1 || ref.AggregateDigest != "" {
		if ref.EventSeq == nil {
			return session.Event{}, ErrInvalidEvidence
		}
		coordinate = int(*ref.EventSeq)
	}
	index, err := sourceEventIndex(events, incarnation, int64(coordinate), eventOrdinals...)
	if err != nil {
		return session.Event{}, err
	}
	return events[index], nil
}

func evidencePreviewMessages(messages []session.Message, incarnation session.IncarnationID, message session.Message) []session.Message {
	preview := session.CloneMessages([]session.Message{message})
	if p := message.Legacy; p != nil && message.ToolResult != nil && p.ResultOrdinal >= 0 {
		if witness, err := SourceMessageIndex(messages, incarnation, p.ResultOrdinal, ReflectionEvidenceLegacyV0); err == nil {
			preview = append(preview, session.CloneMessages(messages[witness : witness+1])[0])
		}
	}
	return preview
}

// CanonicalEvidenceCitation exposes a link only for the exact verified occurrence.
// Callers must not copy a stored EvidenceRef.ToolCallID into a public DTO.
func CanonicalEvidenceCitation(in Input, ref EvidenceRef) (EvidenceCitation, error) {
	preview, err := EvidencePreview(in, ref)
	if err != nil {
		return EvidenceCitation{}, err
	}
	out := EvidenceCitation{Protocol: ref.ResolvedProtocol(), Preview: preview}
	if ref.ToolCallID == "" {
		return out, nil
	}
	if ref.ResolvedProtocol() == ReflectionEvidenceV2 {
		out.ToolCallID = ref.ToolCallID
		return out, nil
	}
	if ref.Locator == EvidenceMessage && ref.Ordinal < len(in.previewMessages) {
		out.ToolCallID = legacyMessageAttachment(in.previewMessages[ref.Ordinal], in.previewMessages, ref.ToolCallID)
	}
	if ref.Locator == EvidenceEvent && ref.Ordinal < len(in.previewEvents) {
		out.ToolCallID = legacyEventAttachment(in.previewEvents[ref.Ordinal])
	}
	return out, nil
}

func legacyMessageAttachment(message session.Message, witnesses []session.Message, providerID session.ToolCallID) session.ToolCallID {
	p := message.Legacy
	if p == nil || len(p.Unsupported) != 0 {
		return ""
	}
	var attached session.ToolCallID
	for _, call := range p.Calls {
		if call.ProviderID == string(providerID) && messageHasCall(message, call.CallID) {
			if attached != "" {
				return ""
			}
			attached = call.CallID
		}
	}
	if message.ToolResult != nil && p.ResultProviderID == string(providerID) && p.ResultOrdinal >= 0 && p.ResultSlot >= 0 {
		return legacyResultAttachment(message, witnesses, attached)
	}
	return attached
}

func legacyResultAttachment(message session.Message, witnesses []session.Message, attached session.ToolCallID) session.ToolCallID {
	p := message.Legacy
	for _, witness := range witnesses {
		origin := witness.Legacy
		if origin == nil || len(origin.Unsupported) != 0 || origin.Incarnation != p.Incarnation || origin.Namespace != p.Namespace || origin.Ordinal != p.ResultOrdinal {
			continue
		}
		for _, call := range origin.Calls {
			if call.Slot == p.ResultSlot && call.ProviderID == p.ResultProviderID && call.CallID == message.ToolResult.CallID && messageHasCall(witness, call.CallID) {
				attached = call.CallID
			}
		}
	}
	return attached
}

func legacyEventAttachment(event session.Event) session.ToolCallID {
	// A normal domain payload in a projected complete source is the proof. An
	// unprojected historical record never receives a guessed attachment.
	p := event.LegacyIdentity()
	if p != nil && p.Incarnation.Valid() && p.Namespace != "" && event.Type == session.EvToolCall && event.Historical == nil && event.ToolCall != nil {
		// Project already proved this normal payload from a complete source, using
		// its durable coordinate or persisted canonical binding. The evidence
		// ordinal may be unknown and must not re-derive or rename that binding.
		return event.ToolCall.ID
	}
	// A result's provenance identifies the result, not the answered call. Keep
	// that citation available but unlinked.
	return ""
}

func validateLegacyEvidencePairing(messages []session.Message) error {
	open := make(map[session.ToolCallID]bool)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if message.Role != session.RoleAssistant || call.ID == "" || open[call.ID] {
				return ErrInvalidEvidence
			}
			open[call.ID] = true
		}
		if result := message.ToolResult; result != nil {
			if message.Role != session.RoleTool || !open[result.CallID] {
				return ErrInvalidEvidence
			}
			delete(open, result.CallID)
		} else if message.Role == session.RoleTool {
			return ErrInvalidEvidence
		}
	}
	if len(open) != 0 {
		return ErrInvalidEvidence
	}
	return nil
}

func publicEvidenceJSON(value any, identifiers []string) (string, error) {
	switch item := value.(type) {
	case MessageProjection:
		redactMessageProjection(&item, identifiers)
		value = item
	case EventProjection:
		redactEventProjection(&item, identifiers)
		value = item
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return truncateUTF8(string(raw), maxEvidencePreviewBytes), nil
}
