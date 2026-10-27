package sessnap

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/stacklok/mecatl/engine/session"
)

// Old IDs are provider strings. These DTOs intentionally never decode an old
// invocation reference into the domain's canonical ToolCallID type.
type legacyCallDTO struct {
	ID     string          `json:"ID"`
	Name   string          `json:"Name"`
	Args   json.RawMessage `json:"Args"`
	ItemID string          `json:"item_id,omitempty"`
}
type legacyResultDTO struct {
	CallID  string
	Content string
	IsError bool
	Parts   []session.Content `json:"Parts,omitempty"`
}
type legacyMessageDTO struct {
	Role                 session.Role                 `json:"role"`
	Text                 string                       `json:"text,omitempty"`
	ToolCalls            []legacyCallDTO              `json:"tool_calls,omitempty"`
	ToolResult           *legacyResultDTO             `json:"tool_result,omitempty"`
	Reasoning            string                       `json:"reasoning,omitempty"`
	ReasoningItemID      string                       `json:"reasoning_item_id,omitempty"`
	Phase                string                       `json:"phase,omitempty"`
	Parts                []contentDTO                 `json:"parts,omitempty"`
	UserPromptProvenance session.UserPromptProvenance `json:"user_prompt_provenance,omitempty"`
	Replay               json.RawMessage              `json:"replay,omitempty"`
}

type legacyRelationshipDTO struct {
	session.SessionRelationship
	CallID string          `json:"call_id,omitempty"`
	Legacy json.RawMessage `json:"legacy_identity,omitempty"`
}

// UnmarshalJSON discriminates before decoding any invocation-bearing field.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	schema, err := identitySchema(data)
	if err != nil {
		return err
	}
	type plain Snapshot
	var wire struct {
		*plain
		IdentitySchema json.RawMessage `json:"identity_schema"`
		Messages       json.RawMessage `json:"messages"`
		Pending        json.RawMessage `json:"pending"`
		Authorization  json.RawMessage `json:"pending_authorization"`
		Relationship   json.RawMessage `json:"relationship"`
		Hold           json.RawMessage `json:"identity_hold"`
	}
	var decoded Snapshot
	wire.plain = (*plain)(&decoded)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Hold) != 0 {
		return errors.New("sessnap: unsupported identity_hold")
	}
	decoded.IdentitySchema = schema
	switch decoded.IdentitySchema {
	case 0:
		if decoded.State == session.StateAwaiting || decoded.State == session.StateAuthorizing ||
			len(wire.Pending) != 0 && string(wire.Pending) != "null" ||
			len(wire.Authorization) != 0 && string(wire.Authorization) != "null" {
			return errors.New("sessnap: legacy pending continuation is unsupported")
		}
		if err := decoded.decodeLegacy(wire.Relationship, wire.Messages); err != nil {
			return err
		}
	case 2:
		if len(wire.Relationship) != 0 {
			if err := json.Unmarshal(wire.Relationship, &decoded.Relationship); err != nil {
				return err
			}
		}
		if err := json.Unmarshal(wire.Messages, &decoded.Messages); err != nil {
			return err
		}
		if len(wire.Pending) != 0 {
			if err := json.Unmarshal(wire.Pending, &decoded.Pending); err != nil {
				return err
			}
		}
		if len(wire.Authorization) != 0 {
			if err := json.Unmarshal(wire.Authorization, &decoded.PendingAuthorization); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("sessnap: unknown identity schema %d", decoded.IdentitySchema)
	}
	*s = decoded
	return nil
}

func (s *Snapshot) decodeLegacy(relationship, messages json.RawMessage) error {
	if len(relationship) != 0 {
		var old legacyRelationshipDTO
		if err := json.Unmarshal(relationship, &old); err != nil {
			return err
		}
		if len(old.Legacy) != 0 {
			return errors.New("sessnap: legacy relationship has new carrier")
		}
		s.Relationship = old.SessionRelationship
		if old.CallID != "" {
			s.Relationship.Legacy = &session.LegacyRelationshipIdentity{ProviderCallID: old.CallID}
		}
	}
	if len(messages) != 0 {
		if err := json.Unmarshal(messages, &s.legacyMessages); err != nil {
			return err
		}
	}
	// A non-nil slice distinguishes decoded /1 history from unsupported
	// schema-zero direct Snapshot construction, even for absent/null history.
	if s.legacyMessages == nil {
		s.legacyMessages = []legacyMessageDTO{}
	}
	return nil
}

// MarshalJSON also preserves a decoded old snapshot before Restore is called.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type plain Snapshot
	if s.IdentitySchema != 0 || s.legacyMessages == nil {
		return json.Marshal(plain(s))
	}
	rel := legacyRelationshipDTO{SessionRelationship: s.Relationship}
	if s.Relationship.Legacy != nil {
		rel.CallID = s.Relationship.Legacy.ProviderCallID
	}
	return json.Marshal(struct {
		plain
		Relationship legacyRelationshipDTO `json:"relationship,omitzero"`
		Messages     []legacyMessageDTO    `json:"messages"`
	}{plain(s), rel, s.legacyMessages})
}

const legacySnapshotNamespace = "sessnap/legacy-v1"

// legacySourceInvocationID derives a stable canonical ID from immutable source
// coordinates, so repeated loads preserve references before a migrated snapshot saves.
func legacySourceInvocationID(inc session.IncarnationID, namespace string, ordinal, slot int) session.ToolCallID {
	h := sha256.New()
	for _, part := range []string{"mecatl.tool-call/legacy-v1", string(inc), namespace, strconv.Itoa(ordinal), strconv.Itoa(slot)} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	return session.ToolCallID("call_legacy_" + hex.EncodeToString(h.Sum(nil)))
}

// This generic envelope preserves strings exactly, including signed/encrypted
// payloads. ProviderID is a configuration selector, not a trustworthy adapter
// namespace: adapters must interpret this legacy carrier during integration.
func legacyReplay(m legacyMessageDTO) *session.ProviderReplay {
	if m.Reasoning == "" && m.ReasoningItemID == "" && m.Phase == "" {
		return nil
	}
	data, _ := json.Marshal(struct {
		Reasoning       string `json:"reasoning"`
		ReasoningItemID string `json:"reasoning_item_id"`
		Phase           string `json:"phase"`
	}{m.Reasoning, m.ReasoningItemID, m.Phase})
	return &session.ProviderReplay{Format: "mecatl.legacy-message", Version: 1, Data: data}
}

type legacyOccurrence struct {
	ordinal int
	slot    int
	call    legacyCallDTO
	id      session.ToolCallID
	unsafe  bool
}

func (s Snapshot) migrateLegacy(inc session.IncarnationID) (Snapshot, error) {
	return s.migrateLegacySource(inc, legacySnapshotNamespace)
}

func (s Snapshot) migrateLegacySource(inc session.IncarnationID, namespace string) (Snapshot, error) {
	source, err := indexLegacySource(s.legacyMessages, inc, namespace)
	if err != nil {
		return s, err
	}
	if err := source.migrateMessages(&s, inc, namespace); err != nil {
		return s, err
	}
	s.IdentitySchema = 2
	return s, nil
}

type legacySource struct {
	old     []legacyMessageDTO
	calls   [][]*legacyOccurrence
	results []*legacyOccurrence
}

// indexLegacySource proves call/result pairings within one complete legacy source.
// It marks duplicate or concurrently outstanding provider keys unsafe, while allowing
// a key to be reused after its earlier occurrence has been answered.
func indexLegacySource(old []legacyMessageDTO, inc session.IncarnationID, namespace string) (*legacySource, error) {
	x := &legacySource{
		old: old, calls: make([][]*legacyOccurrence, len(old)), results: make([]*legacyOccurrence, len(old)),
	}
	open := make(map[string][]*legacyOccurrence)
	for i, m := range old {
		if len(m.Replay) != 0 {
			return nil, errors.New("sessnap: replay carrier without identity schema")
		}
		batch := make(map[string][]*legacyOccurrence)
		for j, call := range m.ToolCalls {
			occ := &legacyOccurrence{ordinal: i, slot: j, call: call, id: legacySourceInvocationID(inc, namespace, i, j), unsafe: call.ID == "" || m.Role != session.RoleAssistant}
			x.calls[i] = append(x.calls[i], occ)
			batch[call.ID] = append(batch[call.ID], occ)
			if len(open[call.ID]) != 0 {
				occ.unsafe = true
				for _, prior := range open[call.ID] {
					prior.unsafe = true
				}
			}
			open[call.ID] = append(open[call.ID], occ)
		}
		for _, occurrences := range batch {
			if len(occurrences) > 1 {
				for _, occ := range occurrences {
					occ.unsafe = true
				}
			}
		}
		if m.ToolResult != nil {
			key := m.ToolResult.CallID
			if occurrences := open[key]; key != "" && len(occurrences) == 1 && !occurrences[0].unsafe && m.Role == session.RoleTool {
				x.results[i] = occurrences[0]
				delete(open, key)
			}
		}
	}
	return x, nil
}

func (x *legacySource) migrateMessages(s *Snapshot, inc session.IncarnationID, namespace string) error {
	s.Messages = make([]messageDTO, len(x.old))
	for i, m := range x.old {
		dto, err := x.migrateMessage(i, m, inc, namespace)
		if err != nil {
			return err
		}
		s.Messages[i] = dto
	}
	return nil
}

func (x *legacySource) migrateMessage(i int, m legacyMessageDTO, inc session.IncarnationID, namespace string) (messageDTO, error) {
	dto := messageDTO{Role: m.Role, Text: m.Text, Parts: m.Parts, ProviderPhase: m.Phase, UserPromptProvenance: m.UserPromptProvenance,
		Legacy: &session.LegacyMessageIdentity{Incarnation: inc, Namespace: namespace, Ordinal: i, ResultOrdinal: -1, ResultSlot: -1, ReasoningItemID: m.ReasoningItemID}}
	replay := &session.MessageReplay{Provider: legacyReplay(m)}
	unsupported := m.Role != session.RoleAssistant && replay.Provider != nil
	for _, occ := range x.calls[i] {
		if occ.unsafe {
			unsupported = true
			continue
		}
		dto.ToolCalls = append(dto.ToolCalls, session.ToolCall{ID: occ.id, Name: occ.call.Name, Args: occ.call.Args})
		replay.Bindings = append(replay.Bindings, session.ToolCallBinding{CallID: occ.id, PairingID: session.ProviderCallID(occ.call.ID), ItemID: session.ProviderItemID(occ.call.ItemID)})
		dto.Legacy.Calls = append(dto.Legacy.Calls, session.LegacyCallIdentity{CallID: occ.id, Slot: occ.slot, ProviderID: occ.call.ID, ItemID: occ.call.ItemID})
	}
	if m.ToolResult != nil {
		dto.Legacy.ResultProviderID = m.ToolResult.CallID
		if occ := x.results[i]; occ != nil && !occ.unsafe {
			dto.ToolResult = &session.ToolResult{CallID: occ.id, Content: m.ToolResult.Content, IsError: m.ToolResult.IsError, Parts: m.ToolResult.Parts}
			dto.Legacy.ResultOrdinal, dto.Legacy.ResultSlot = occ.ordinal, occ.slot
		} else {
			unsupported = true
			dto.Role = session.RoleAssistant
		}
	} else if m.Role == session.RoleTool {
		unsupported = true
		dto.Role = session.RoleAssistant
	}
	if unsupported {
		var err error
		dto.Legacy.Unsupported, err = json.Marshal(m)
		if err != nil {
			return messageDTO{}, err
		}
		if m.ToolResult != nil {
			preserveUnattachedResult(&dto, m.ToolResult)
		}
	}
	if dto.Role == session.RoleAssistant && (replay.Provider != nil || len(replay.Bindings) != 0) {
		dto.Replay = replay
	}
	return dto, nil
}

func preserveUnattachedResult(dto *messageDTO, result *legacyResultDTO) {
	if dto.Text == "" {
		dto.Text = result.Content
	} else if result.Content != "" && dto.Text != result.Content {
		dto.Text += "\n" + result.Content
	}
	dto.Historical = session.HistoricalMessage(fromDTO(*dto))
	dto.Historical.Parts = append(contentFromDTO(dto.Parts), dto.Historical.Parts...)
}
