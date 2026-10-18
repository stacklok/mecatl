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
type legacyPendingAskDTO struct {
	AskID     string
	Tool      string
	Args      json.RawMessage
	Reason    string
	Call      string                         `json:"call,omitempty"`
	Guardrail *session.GuardrailPendingScope `json:"guardrail,omitempty"`
	Origin    session.ApprovalOrigin         `json:"origin,omitempty"`
}
type legacyAuthorizationCallDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   []byte `json:"args,omitempty"`
	ItemID string `json:"item_id,omitempty"`
}
type legacyPendingAuthorizationDTO struct {
	Authorization externalAuthorizationDTO     `json:"authorization"`
	Call          legacyAuthorizationCallDTO   `json:"call"`
	Deferred      []legacyAuthorizationCallDTO `json:"deferred,omitempty"`
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
	}
	var decoded Snapshot
	wire.plain = (*plain)(&decoded)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	decoded.IdentitySchema = schema
	switch decoded.IdentitySchema {
	case 0:
		if decoded.IdentityHold != nil {
			return errors.New("sessnap: legacy snapshot has new identity hold")
		}
		if len(wire.Relationship) != 0 {
			var old legacyRelationshipDTO
			if err := json.Unmarshal(wire.Relationship, &old); err != nil {
				return err
			}
			if len(old.Legacy) != 0 {
				return errors.New("sessnap: legacy relationship has new carrier")
			}
			decoded.Relationship = old.SessionRelationship
			if old.CallID != "" {
				decoded.Relationship.Legacy = &session.LegacyRelationshipIdentity{ProviderCallID: old.CallID}
			}
		}
		if err := decoded.decodeLegacy(wire.Messages, wire.Pending, wire.Authorization); err != nil {
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

// MarshalJSON also preserves a decoded old snapshot before Restore is called.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type plain Snapshot
	if s.IdentitySchema != 0 || s.legacyMessages == nil && s.legacyPendingBytes == nil && s.legacyAuthorizationBytes == nil && s.Relationship.Legacy == nil {
		return json.Marshal(plain(s))
	}
	rel := legacyRelationshipDTO{SessionRelationship: s.Relationship}
	if s.Relationship.Legacy != nil {
		rel.CallID = s.Relationship.Legacy.ProviderCallID
	}
	return json.Marshal(struct {
		plain
		Relationship  legacyRelationshipDTO `json:"relationship,omitzero"`
		Messages      []legacyMessageDTO    `json:"messages"`
		Pending       json.RawMessage       `json:"pending,omitempty"`
		Authorization json.RawMessage       `json:"pending_authorization,omitempty"`
	}{plain(s), rel, s.legacyMessages, s.legacyPendingBytes, s.legacyAuthorizationBytes})
}

func (s *Snapshot) decodeLegacy(messages, pending, authorization []byte) error {
	if len(messages) != 0 {
		if err := json.Unmarshal(messages, &s.legacyMessages); err != nil {
			return err
		}
	}
	if len(pending) != 0 {
		if err := json.Unmarshal(pending, &s.legacyPending); err != nil {
			return err
		}
		s.legacyPendingBytes = append([]byte(nil), pending...)
	}
	if len(authorization) != 0 {
		if err := json.Unmarshal(authorization, &s.legacyAuthorization); err != nil {
			return err
		}
		s.legacyAuthorizationBytes = append([]byte(nil), authorization...)
	}
	return nil
}

const legacySnapshotNamespace = "sessnap/legacy-v1"

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
	ordinal  int
	slot     int
	call     legacyCallDTO
	id       session.ToolCallID
	unsafe   bool
	answered bool
}

type legacyHoldPayload struct {
	Version                 int                      `json:"version"`
	State                   session.State            `json:"state"`
	Pending                 []byte                   `json:"pending,omitempty"`
	Authorization           []byte                   `json:"pending_authorization,omitempty"`
	Enrollment              []byte                   `json:"pending_workspace_enrollment,omitempty"`
	Stop                    session.StopReason       `json:"stop_reason,omitempty"`
	RetryDisposition        session.RetryDisposition `json:"retry_disposition,omitempty"`
	StreamProgress          session.StreamProgress   `json:"stream_progress,omitempty"`
	RetryPending            bool                     `json:"retry_pending,omitempty"`
	RetryPendingDisposition session.RetryDisposition `json:"retry_pending_disposition,omitempty"`
	RetryPendingProgress    session.StreamProgress   `json:"retry_pending_progress,omitempty"`
	LastError               string                   `json:"last_error,omitempty"`
}

func validateHoldPayload(state session.State, data []byte) error {
	var hold legacyHoldPayload
	if err := json.Unmarshal(data, &hold); err != nil {
		return fmt.Errorf("sessnap: invalid hold: %w", err)
	}
	if hold.Version != 1 || hold.State != state {
		return errors.New("sessnap: invalid hold lifecycle/version")
	}
	var ask *legacyPendingAskDTO
	var auth *legacyPendingAuthorizationDTO
	if len(hold.Pending) != 0 {
		if err := json.Unmarshal(hold.Pending, &ask); err != nil {
			return err
		}
	}
	if len(hold.Authorization) != 0 {
		if err := json.Unmarshal(hold.Authorization, &auth); err != nil {
			return err
		}
	}
	// Mismatched or missing original continuation is itself a reason for holding;
	// decoding validates storage, not execution authority.
	return nil
}

func validateHeldHistory(messages []messageDTO) error {
	seen := make(map[session.ToolCallID]bool)
	open := make(map[session.ToolCallID]bool)
	for _, dto := range messages {
		if err := session.ValidateMessageReplay(fromDTO(dto)); err != nil {
			return err
		}
		for _, call := range dto.ToolCalls {
			if dto.Role != session.RoleAssistant || call.ID == "" || seen[call.ID] {
				return errors.New("sessnap: unsafe held call identity")
			}
			seen[call.ID], open[call.ID] = true, true
		}
		if result := dto.ToolResult; result != nil {
			if dto.Role != session.RoleTool || result.CallID == "" || !open[result.CallID] {
				return errors.New("sessnap: orphan held canonical result")
			}
			delete(open, result.CallID)
		} else if dto.Role == session.RoleTool {
			return errors.New("sessnap: held tool message without result")
		}
	}
	return nil
}

func (s Snapshot) migrateLegacy(inc session.IncarnationID) (Snapshot, error) {
	return s.migrateLegacySource(inc, legacySnapshotNamespace)
}

func (s Snapshot) migrateLegacySource(inc session.IncarnationID, namespace string) (Snapshot, error) {
	if err := s.decodeLegacyLiteral(); err != nil {
		return s, err
	}
	source, err := indexLegacySource(s.legacyMessages, inc, namespace)
	if err != nil {
		return s, err
	}
	if err := source.migrateMessages(&s, inc, namespace); err != nil {
		return s, err
	}
	s.Pending, s.PendingAuthorization = nil, nil
	held := false
	switch s.State {
	case session.StateAwaiting:
		held = !source.migratePendingAsk(&s)
	case session.StateAuthorizing:
		held, err = source.migratePendingAuthorization(&s)
		if err != nil {
			return s, err
		}
	default:
		if s.legacyPending != nil || s.legacyAuthorization != nil {
			return s, errors.New("sessnap: pending value outside matching state")
		}
	}
	if held {
		if err := s.holdLegacyContinuation(); err != nil {
			return s, err
		}
	}
	s.IdentitySchema = 2
	return s, nil
}

func (s *Snapshot) decodeLegacyLiteral() error {
	// Direct Snapshot literals predate schema discrimination. Decode their old
	// provider-domain fields through the same private DTOs as the JSON path.
	if s.legacyMessages == nil && (s.Messages != nil || s.Pending != nil || s.PendingAuthorization != nil) {
		messages, err := json.Marshal(s.Messages)
		if err != nil {
			return err
		}
		pending, err := json.Marshal(s.Pending)
		if err != nil {
			return err
		}
		auth, err := json.Marshal(s.PendingAuthorization)
		if err != nil {
			return err
		}
		if err := s.decodeLegacy(messages, pending, auth); err != nil {
			return err
		}
	}
	return nil
}

type legacySource struct {
	old           []legacyMessageDTO
	calls         [][]*legacyOccurrence
	results       []*legacyOccurrence
	open          map[string][]*legacyOccurrence
	historyProven bool
	lastAssistant int
}

func indexLegacySource(old []legacyMessageDTO, inc session.IncarnationID, namespace string) (*legacySource, error) {
	x := &legacySource{
		old: old, calls: make([][]*legacyOccurrence, len(old)), results: make([]*legacyOccurrence, len(old)),
		open: make(map[string][]*legacyOccurrence), historyProven: true, lastAssistant: -1,
	}
	for i, m := range old {
		if len(m.Replay) != 0 {
			return nil, errors.New("sessnap: replay carrier without identity schema")
		}
		batch := make(map[string][]*legacyOccurrence)
		for j, call := range m.ToolCalls {
			occ := &legacyOccurrence{ordinal: i, slot: j, call: call, id: legacySourceInvocationID(inc, namespace, i, j), unsafe: call.ID == "" || m.Role != session.RoleAssistant}
			x.calls[i] = append(x.calls[i], occ)
			batch[call.ID] = append(batch[call.ID], occ)
			if len(x.open[call.ID]) != 0 {
				occ.unsafe = true
				for _, prior := range x.open[call.ID] {
					prior.unsafe = true
				}
			}
			x.open[call.ID] = append(x.open[call.ID], occ)
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
			if occurrences := x.open[key]; key != "" && len(occurrences) == 1 && !occurrences[0].unsafe && m.Role == session.RoleTool {
				x.results[i] = occurrences[0]
				occurrences[0].answered = true
				delete(x.open, key)
			}
		}
	}
	return x, nil
}

func (x *legacySource) migrateMessages(s *Snapshot, inc session.IncarnationID, namespace string) error {
	s.Messages = make([]messageDTO, len(x.old))
	for i, m := range x.old {
		if m.Role == session.RoleAssistant {
			x.lastAssistant = i
		}
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
			if dto.Text == "" {
				dto.Text = m.ToolResult.Content
			}
		}
	} else if m.Role == session.RoleTool {
		unsupported = true
		dto.Role = session.RoleAssistant
	}
	if unsupported {
		x.historyProven = false
		var err error
		dto.Legacy.Unsupported, err = json.Marshal(m)
		if err != nil {
			return messageDTO{}, err
		}
	}
	if dto.Role == session.RoleAssistant && (replay.Provider != nil || len(replay.Bindings) != 0) {
		dto.Replay = replay
	}
	return dto, nil
}

func (x *legacySource) resolve(key string) *legacyOccurrence {
	occurrences := x.open[key]
	if key == "" || len(occurrences) != 1 || occurrences[0].unsafe {
		return nil
	}
	return occurrences[0]
}

func (x *legacySource) migratePendingAsk(s *Snapshot) bool {
	p := s.legacyPending
	if p == nil || s.legacyAuthorization != nil {
		return false
	}
	occ := x.resolve(p.Call)
	if occ == nil || occ.call.Name != p.Tool || !x.historyProven || occ.ordinal != x.lastAssistant {
		return false
	}
	proven := true
	for i, occurrences := range x.calls {
		for _, other := range occurrences {
			shouldBeAnswered := i < occ.ordinal || i == occ.ordinal && other.slot < occ.slot
			if other.answered != shouldBeAnswered {
				proven = false
			}
		}
	}
	for _, m := range x.old[occ.ordinal+1:] {
		if m.Role != session.RoleTool || m.ToolResult == nil {
			proven = false
		}
	}
	if !proven {
		return false
	}
	s.Pending = &session.PendingAsk{AskID: p.AskID, Tool: p.Tool, Args: append([]byte(nil), p.Args...), Reason: p.Reason, Call: occ.id, Guardrail: p.Guardrail, Origin: p.Origin}
	return true
}

func (x *legacySource) convertAuthorizationCall(call legacyAuthorizationCallDTO) (authorizationCallDTO, bool) {
	occ := x.resolve(call.ID)
	if occ == nil || occ.call.ItemID != call.ItemID {
		return authorizationCallDTO{}, false
	}
	return authorizationCallDTO{ID: occ.id, Name: call.Name, Args: append([]byte(nil), call.Args...)}, true
}

func (x *legacySource) migratePendingAuthorization(s *Snapshot) (bool, error) {
	p := s.legacyAuthorization
	if p == nil || s.legacyPending != nil || !x.historyProven {
		return true, nil
	}
	call, ok := x.convertAuthorizationCall(p.Call)
	if !ok {
		return true, nil
	}
	s.PendingAuthorization = &pendingAuthorizationDTO{Authorization: p.Authorization, Call: call}
	for _, deferred := range p.Deferred {
		call, ok := x.convertAuthorizationCall(deferred)
		if !ok {
			return true, nil
		}
		s.PendingAuthorization.Deferred = append(s.PendingAuthorization.Deferred, call)
	}
	probe := session.New(s.ID, s.Mode, s.EnvironmentRef, s.Limits, s.CreatedAt)
	for _, m := range s.Messages {
		probe.Conversation.Append(fromDTO(m))
	}
	if err := probe.BeginTurn(); err != nil {
		return false, err
	}
	if err := probe.PauseForAuthorization(*fromPendingAuthorizationDTO(s.PendingAuthorization)); err != nil {
		return true, nil
	}
	return false, nil
}

func (s *Snapshot) holdLegacyContinuation() error {
	payload := legacyHoldPayload{Version: 1, State: s.State, Pending: s.legacyPendingBytes, Authorization: s.legacyAuthorizationBytes,
		Stop: s.StopReason, RetryDisposition: s.RetryDisposition, StreamProgress: s.StreamProgress,
		RetryPending: s.RetryPending, RetryPendingDisposition: s.RetryPendingDisposition, RetryPendingProgress: s.RetryPendingProgress, LastError: s.LastError}
	if s.PendingWorkspaceEnrollment != nil {
		var err error
		payload.Enrollment, err = json.Marshal(s.PendingWorkspaceEnrollment)
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.IdentityHold = &session.IdentityMigrationHold{Reason: "legacy-continuation-unproven", Payload: data}
	s.Pending, s.PendingAuthorization, s.PendingWorkspaceEnrollment = nil, nil, nil
	return nil
}
