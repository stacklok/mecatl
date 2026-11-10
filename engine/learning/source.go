package learning

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
)

// ManifestProtocolSupported accepts only canonical evidence in the source coordinate domain.
func ManifestProtocolSupported(protocol EvidenceProtocol, domain string) bool {
	return protocol == ReflectionEvidenceV2 && domain == ReflectionEvidenceSourceV1
}

// EvidenceCitation is a public-safe verified canonical citation.
type EvidenceCitation struct {
	Protocol   EvidenceProtocol   `json:"protocol"`
	ToolCallID session.ToolCallID `json:"tool_call_id,omitempty"`
	Preview    string             `json:"preview"`
}

// RematerializeEvidence verifies a stored canonical manifest against eligible retained
// original sources. Sparse readers must supply original event-only iterator ordinals.
func RematerializeEvidence(trajectory Trajectory, events []session.Event, manifest MaterializationManifest, eventOrdinals ...[]int) (Input, error) {
	if !trajectory.LearningEligible() || !ManifestProtocolSupported(manifest.Protocol, manifest.Source.Domain) || manifest.Source.Value != trajectory.SessionID || len(manifest.Entries) > MaxInputMessages+MaxInputEvents {
		return Input{}, ErrInvalidEvidence
	}
	var messages []session.Message
	var selectedEvents []session.Event
	for _, entry := range manifest.Entries {
		switch entry.Locator {
		case EvidenceMessage:
			if len(messages) >= MaxInputMessages || entry.OriginalMessage == nil {
				return Input{}, ErrInvalidEvidence
			}
			index := *entry.OriginalMessage
			if index < 0 || index >= len(trajectory.Messages) {
				return Input{}, ErrInvalidEvidence
			}
			messages = append(messages, trajectory.Messages[index])
		case EvidenceEvent:
			if len(selectedEvents) >= MaxInputEvents || entry.EventSequence == nil {
				return Input{}, ErrInvalidEvidence
			}
			index, err := sourceEventIndex(events, *entry.EventSequence, eventOrdinals...)
			if err != nil {
				return Input{}, err
			}
			selectedEvents = append(selectedEvents, events[index])
		default:
			return Input{}, ErrInvalidEvidence
		}
	}
	trajectory.Messages = messages
	in := NewInput(trajectory, selectedEvents, nil, nil)
	// Materialization excludes event text from its canonical digest.
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
	in.Manifest = &manifest
	if err := ValidateInput(in); err != nil {
		return Input{}, err
	}
	return in, nil
}

func sourceEventIndex(events []session.Event, coordinate int64, eventOrdinals ...[]int) (int, error) {
	if len(eventOrdinals) > 1 || len(eventOrdinals) == 1 && len(eventOrdinals[0]) != len(events) {
		return -1, ErrInvalidEvidence
	}
	found := -1
	for i := range events {
		ordinal := i
		if len(eventOrdinals) == 1 {
			ordinal = eventOrdinals[0][i]
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

// VerifyEvidenceSource verifies a canonical citation against eligible retained sources.
// Manifest-backed approval must use RematerializeEvidence to verify the aggregate.
func VerifyEvidenceSource(trajectory Trajectory, events []session.Event, ref EvidenceRef, eventOrdinals ...[]int) (EvidenceCitation, error) {
	if !trajectory.LearningEligible() || ref.Protocol != ReflectionEvidenceV2 || ref.SessionID != trajectory.SessionID {
		return EvidenceCitation{}, ErrInvalidEvidence
	}
	var selectedEvents []session.Event
	switch ref.Locator {
	case EvidenceMessage:
		coordinate := ref.Ordinal
		if ref.OriginalMessage != nil {
			coordinate = *ref.OriginalMessage
		}
		if coordinate < 0 || coordinate >= len(trajectory.Messages) {
			return EvidenceCitation{}, ErrInvalidEvidence
		}
		trajectory.Messages = trajectory.Messages[coordinate : coordinate+1]
	case EvidenceEvent:
		coordinate := int64(ref.Ordinal)
		if ref.AggregateDigest != "" {
			if ref.EventSeq == nil {
				return EvidenceCitation{}, ErrInvalidEvidence
			}
			coordinate = *ref.EventSeq
		}
		index, err := sourceEventIndex(events, coordinate, eventOrdinals...)
		if err != nil {
			return EvidenceCitation{}, err
		}
		trajectory.Messages = nil
		selectedEvents = []session.Event{events[index]}
	default:
		return EvidenceCitation{}, ErrInvalidEvidence
	}
	in := NewInput(trajectory, selectedEvents, nil, nil)
	local := ref
	local.Ordinal, local.ManifestIndex = 0, 0
	local.OriginalMessage, local.AggregateDigest = nil, ""
	if ref.AggregateDigest != "" && ref.Locator == EvidenceEvent {
		in.Events[0].Text = ""
		seq := selectedEvents[0].Seq
		local.EventSeq = &seq
	}
	return CanonicalEvidenceCitation(in, local)
}

// CanonicalEvidenceCitation returns only digest-verified canonical identities.
func CanonicalEvidenceCitation(in Input, ref EvidenceRef) (EvidenceCitation, error) {
	preview, err := EvidencePreview(in, ref)
	if err != nil {
		return EvidenceCitation{}, err
	}
	return EvidenceCitation{Protocol: ref.Protocol, Preview: preview, ToolCallID: ref.ToolCallID}, nil
}

func publicEvidenceJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return truncateUTF8(string(raw), maxEvidencePreviewBytes), nil
}
