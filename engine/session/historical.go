package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// EvHistorical carries readable history whose invocation association is unavailable.
const EvHistorical EventType = "historical"

// HistoricalRecord is a display-only, source-local record. It contains no replay,
// invocation reference, continuation token, or inferred child relationship.
type HistoricalRecord struct {
	RecordID              string
	Kind                  string
	Text                  string
	Parts                 []Content
	AttachmentUnavailable bool
}

// LegacyRelationshipIdentity retains a provider-era parent reference privately.
// It is never used to infer an invocation from the child's own history.
type LegacyRelationshipIdentity struct{ ProviderCallID string }

func validRelationshipCall(rel SessionRelationship) bool {
	return rel.CallID != "" && rel.Legacy == nil || rel.CallID == "" && rel.Legacy != nil && rel.Legacy.ProviderCallID != ""
}

// HistoricalRelationship returns public-safe relationship metadata. Unproven
// legacy delegation edges are unavailable, not normal edges with a blank call ID.
func HistoricalRelationship(rel SessionRelationship) SessionRelationship {
	if rel.Legacy != nil {
		return SessionRelationship{}
	}
	return CloneSessionRelationship(rel)
}

// LegacyEventIdentity is a private persistence/verification view. Ordinal is the
// original event-only iterator ordinal, NOT Event.Seq or a cursor position.
// Payload must never enter a public projection or executable identity lookup.
type LegacyEventIdentity struct {
	Incarnation IncarnationID
	Namespace   string
	Coordinate  string
	Ordinal     int
	Payload     []byte
}

// LegacyIdentity returns an owned private verification/persistence view.
func (e Event) LegacyIdentity() *LegacyEventIdentity {
	if e.legacy == nil {
		return nil
	}
	p := *e.legacy
	p.Payload = slices.Clone(p.Payload)
	return &p
}

// WithLegacyIdentity attaches an independently owned private carrier.
func (e Event) WithLegacyIdentity(p LegacyEventIdentity) Event {
	p.Payload = slices.Clone(p.Payload)
	e.legacy = &p
	return e
}

// WithLegacySource supplies an actual durable source position without overwriting
// provenance already carried through a decoding driver or a migrated log.
func (e Event) WithLegacySource(namespace, coordinate string) Event {
	if e.legacy == nil || e.legacy.Coordinate != "" {
		return e
	}
	p := *e.legacy
	p.Namespace, p.Coordinate = namespace, coordinate
	return e.WithLegacyIdentity(p)
}

// HistoricalEvent returns an owned safe display projection for a single record,
// including cursor pages that cannot prove a complete call/result source. Supply
// the original event-only ordinal when known, or -1; it never guesses a call ID.
func HistoricalEvent(e Event, inc IncarnationID, ordinal int) (*HistoricalRecord, error) {
	if e.Historical == nil {
		return nil, nil
	}
	h := *e.Historical
	h.Parts = cloneContents(h.Parts)
	if h.RecordID != "" {
		return &h, nil
	}
	p := e.legacy
	if p == nil {
		return nil, fmt.Errorf("session: historical record lacks source identity")
	}
	namespace, coordinate := p.Namespace, p.Coordinate
	if namespace == "" {
		namespace = "eventlog/legacy-v1"
	}
	if p.Incarnation != "" {
		inc = p.Incarnation
	}
	if p.Ordinal >= 0 {
		ordinal = p.Ordinal
	}
	if coordinate == "" {
		if e.RunID != "" {
			coordinate = fmt.Sprintf("run:%s/seq:%d", e.RunID, e.Seq)
		} else if ordinal >= 0 {
			coordinate = fmt.Sprintf("event:%d", ordinal)
		} else {
			return nil, fmt.Errorf("session: historical record lacks source position")
		}
	}
	if !inc.Valid() {
		return nil, fmt.Errorf("session: historical record lacks source incarnation")
	}
	h.RecordID = HistoricalRecordID(inc, namespace, coordinate)
	return &h, nil
}

// HistoricalRecordID identifies a source position, not the content at that position.
func HistoricalRecordID(inc IncarnationID, namespace, coordinate string) string {
	b, _ := json.Marshal([]string{string(inc), namespace, coordinate})
	sum := sha256.Sum256(b)
	return "history_" + hex.EncodeToString(sum[:])
}

// HistoricalInvocationID names a proven occurrence, disjoint from snapshot IDs.
func HistoricalInvocationID(inc IncarnationID, namespace string, ordinal int) ToolCallID {
	return ToolCallID("call_" + HistoricalRecordID(inc, namespace, fmt.Sprint(ordinal)))
}

// HistoricalMessage projects unsupported snapshot history without exposing its
// original provider keys or replay. It also preserves typed result content that
// could not be placed on a normal ToolResult.
func HistoricalMessage(m Message) *HistoricalRecord {
	if m.Historical != nil {
		h := *m.Historical
		h.Parts = cloneContents(h.Parts)
		return &h
	}
	if m.Legacy == nil || len(m.Legacy.Unsupported) == 0 {
		return nil
	}
	var old struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			Name string
			Args json.RawMessage
		} `json:"tool_calls"`
		ToolResult *struct {
			Content string
			Parts   []Content
		} `json:"tool_result"`
	}
	// The DTO was validated by the storage codec; malformed private data still
	// leaves the already-safe readable text available.
	_ = json.Unmarshal(m.Legacy.Unsupported, &old)
	h := &HistoricalRecord{RecordID: HistoricalRecordID(m.Legacy.Incarnation, m.Legacy.Namespace, fmt.Sprint(m.Legacy.Ordinal)), Kind: old.Role, Text: m.Text, Parts: cloneHistoricalParts(m.Parts), AttachmentUnavailable: true}
	for _, call := range old.ToolCalls {
		if h.Text != "" {
			h.Text += "\n"
		}
		h.Text += call.Name
		if len(call.Args) != 0 {
			h.Text += "\n" + string(call.Args)
		}
	}
	if old.ToolResult != nil {
		h.Kind = string(EvToolResult)
		if h.Text == "" {
			h.Text = old.ToolResult.Content
		}
		h.Parts = cloneHistoricalParts(old.ToolResult.Parts)
	}
	return h
}

func cloneHistoricalParts(parts []Content) []Content {
	return cloneContents(parts)
}

// CloneEventIdentity copies the private carrier and historical display content.
// Other event payloads retain the existing immutable value-object contract.
func CloneEventIdentity(e Event) Event {
	if p := e.LegacyIdentity(); p != nil {
		e = e.WithLegacyIdentity(*p)
	}
	if e.Historical != nil {
		h := *e.Historical
		h.Parts = cloneHistoricalParts(h.Parts)
		e.Historical = &h
	}
	return e
}
