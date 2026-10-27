package sessnap

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

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

// legacySnapshotDTO is the /1 storage schema, separate from the current Snapshot.
// Stable metadata is copied explicitly; provider-era references stay in legacy DTOs.
type legacySnapshotDTO struct {
	ID                                   session.SessionID                        `json:"id"`
	State                                session.State                            `json:"state"`
	Mode                                 session.PermissionMode                   `json:"mode"`
	Limits                               session.Limits                           `json:"limits"`
	Counters                             session.Counters                         `json:"counters"`
	ExternalBinding                      session.ExternalBinding                  `json:"external_binding,omitempty"`
	CreatedAt                            time.Time                                `json:"created_at"`
	Incarnation                          session.IncarnationID                    `json:"incarnation,omitempty"`
	Messages                             []legacyMessageDTO                       `json:"messages"`
	Pending                              json.RawMessage                          `json:"pending"`
	PendingAuthorization                 json.RawMessage                          `json:"pending_authorization"`
	PendingWorkspaceEnrollment           *session.PendingWorkspaceEnrollment      `json:"pending_workspace_enrollment,omitempty"`
	StopReason                           session.StopReason                       `json:"stop_reason,omitempty"`
	Kind                                 session.SessionKind                      `json:"kind,omitempty"`
	Relationship                         legacyRelationshipDTO                    `json:"relationship"`
	Profile                              string                                   `json:"profile,omitempty"`
	ExecutionTemplateID                  string                                   `json:"execution_template_id,omitempty"`
	ExecutionTemplateRevision            string                                   `json:"execution_template_revision,omitempty"`
	AgentDefinitionName                  string                                   `json:"agent_definition_name,omitempty"`
	AgentCreateIntent                    string                                   `json:"agent_create_intent,omitempty"`
	ProviderID                           string                                   `json:"provider_id,omitempty"`
	ModelID                              string                                   `json:"model_id,omitempty"`
	ReasoningEffort                      string                                   `json:"reasoning_effort,omitempty"`
	DebugMCPServers                      []string                                 `json:"debug_mcp_servers,omitempty"`
	DebugMCPTools                        []string                                 `json:"debug_mcp_tools,omitempty"`
	DebugTargetFingerprint               string                                   `json:"debug_target_fingerprint,omitempty"`
	Title                                string                                   `json:"title,omitempty"`
	TitleProvenance                      session.TitleProvenance                  `json:"title_provenance,omitempty"`
	TitleRevision                        uint64                                   `json:"title_revision,omitempty"`
	TitleGeneration                      session.TitleGenerationState             `json:"title_generation,omitempty"`
	TitleSourcePrompts                   []string                                 `json:"title_source_prompts,omitempty"`
	TitleAttempts                        []session.TitleAttempt                   `json:"title_attempts,omitempty"`
	TokenUsage                           map[session.UsageKind]session.TokenUsage `json:"token_usage"`
	LatestContextOccupancy               *session.ContextOccupancy                `json:"latest_context_occupancy,omitempty"`
	RetryDisposition                     session.RetryDisposition                 `json:"retry_disposition,omitempty"`
	StreamProgress                       session.StreamProgress                   `json:"stream_progress,omitempty"`
	RetryPending                         bool                                     `json:"retry_pending,omitempty"`
	RetryPendingDisposition              session.RetryDisposition                 `json:"retry_pending_disposition,omitempty"`
	RetryPendingProgress                 session.StreamProgress                   `json:"retry_pending_progress,omitempty"`
	LastError                            string                                   `json:"last_error,omitempty"`
	RunID                                string                                   `json:"run_id,omitempty"`
	Owner                                *session.Principal                       `json:"owner,omitempty"`
	Authority                            *session.Authority                       `json:"authority,omitempty"`
	ExecutionCapabilities                *session.ExecutionCapabilities           `json:"execution_capabilities,omitempty"`
	WorkspaceEnrollmentBrokerKeys        []string                                 `json:"workspace_enrollment_broker_keys,omitempty"`
	WorkspaceEnrollmentBrokerKeysPresent bool                                     `json:"workspace_enrollment_broker_keys_present,omitempty"`
	EnvironmentRef                       session.EnvironmentRef                   `json:"environment_ref"`
	Placement                            session.PlacementMetadata                `json:"placement,omitempty"`
}

type legacyRelationshipDTO struct {
	ScheduleName           string                `json:"schedule_name,omitempty"`
	OriginSessionID        session.SessionID     `json:"origin_session_id,omitempty"`
	OriginIncarnation      session.IncarnationID `json:"origin_incarnation,omitempty"`
	ParentSessionID        session.SessionID     `json:"parent_session_id,omitempty"`
	ParentIncarnation      session.IncarnationID `json:"parent_incarnation,omitempty"`
	CallID                 string                `json:"call_id,omitempty"`
	Legacy                 json.RawMessage       `json:"legacy_identity,omitempty"`
	BranchIndex            *int                  `json:"branch_index,omitempty"`
	TeamID                 string                `json:"team_id,omitempty"`
	MemberName             string                `json:"member_name,omitempty"`
	DebugTargetID          session.SessionID     `json:"debug_target_id,omitempty"`
	DebugTargetIncarnation session.IncarnationID `json:"debug_target_incarnation,omitempty"`
}

func decodeLegacySnapshot(data []byte) (Snapshot, error) {
	var old legacySnapshotDTO
	if err := json.Unmarshal(data, &old); err != nil {
		return Snapshot{}, err
	}
	if old.State == session.StateAwaiting || old.State == session.StateAuthorizing ||
		len(old.Pending) != 0 && string(old.Pending) != "null" ||
		len(old.PendingAuthorization) != 0 && string(old.PendingAuthorization) != "null" {
		return Snapshot{}, errors.New("sessnap: legacy pending continuation is unsupported")
	}
	if len(old.Relationship.Legacy) != 0 {
		return Snapshot{}, errors.New("sessnap: legacy relationship contains schema-2 legacy_identity field")
	}
	inc, err := snapshotIncarnation(old.Incarnation, old.ID, old.CreatedAt, old.Owner)
	if err != nil {
		return Snapshot{}, err
	}
	messages, err := migrateLegacyMessages(old.Messages, inc, legacySnapshotNamespace)
	if err != nil {
		return Snapshot{}, err
	}
	rel := old.Relationship
	relationship := session.SessionRelationship{
		ScheduleName: rel.ScheduleName, OriginSessionID: rel.OriginSessionID, OriginIncarnation: rel.OriginIncarnation,
		ParentSessionID: rel.ParentSessionID, ParentIncarnation: rel.ParentIncarnation,
		BranchIndex: rel.BranchIndex, TeamID: rel.TeamID, MemberName: rel.MemberName,
		DebugTargetID: rel.DebugTargetID, DebugTargetIncarnation: rel.DebugTargetIncarnation,
	}
	if rel.CallID != "" {
		relationship.Legacy = &session.LegacyRelationshipIdentity{ProviderCallID: rel.CallID}
	}
	return Snapshot{
		IdentitySchema: 2, Incarnation: inc, Messages: messages, Relationship: relationship,
		ID: old.ID, State: old.State, Mode: old.Mode, Limits: old.Limits, Counters: old.Counters,
		ExternalBinding: old.ExternalBinding, CreatedAt: old.CreatedAt,
		PendingWorkspaceEnrollment: old.PendingWorkspaceEnrollment, StopReason: old.StopReason, Kind: old.Kind,
		Profile: old.Profile, ExecutionTemplateID: old.ExecutionTemplateID, ExecutionTemplateRevision: old.ExecutionTemplateRevision,
		AgentDefinitionName: old.AgentDefinitionName, AgentCreateIntent: old.AgentCreateIntent,
		ProviderID: old.ProviderID, ModelID: old.ModelID, ReasoningEffort: old.ReasoningEffort,
		DebugMCPServers: old.DebugMCPServers, DebugMCPTools: old.DebugMCPTools, DebugTargetFingerprint: old.DebugTargetFingerprint,
		Title: old.Title, TitleProvenance: old.TitleProvenance, TitleRevision: old.TitleRevision,
		TitleGeneration: old.TitleGeneration, TitleSourcePrompts: old.TitleSourcePrompts, TitleAttempts: old.TitleAttempts,
		TokenUsage: old.TokenUsage, LatestContextOccupancy: old.LatestContextOccupancy,
		RetryDisposition: old.RetryDisposition, StreamProgress: old.StreamProgress,
		RetryPending: old.RetryPending, RetryPendingDisposition: old.RetryPendingDisposition, RetryPendingProgress: old.RetryPendingProgress,
		LastError: old.LastError, RunID: old.RunID, Owner: old.Owner, Authority: old.Authority, ExecutionCapabilities: old.ExecutionCapabilities,
		WorkspaceEnrollmentBrokerKeys: old.WorkspaceEnrollmentBrokerKeys, WorkspaceEnrollmentBrokerKeysPresent: old.WorkspaceEnrollmentBrokerKeysPresent,
		EnvironmentRef: old.EnvironmentRef, Placement: old.Placement,
	}, nil
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

type legacySource struct {
	calls   [][]*legacyOccurrence
	results []*legacyOccurrence
}

// indexLegacySource proves call/result pairings within one complete legacy source.
// It marks duplicate or concurrently outstanding provider keys unsafe, while allowing
// a key to be reused after its earlier occurrence has been answered.
func indexLegacySource(old []legacyMessageDTO, inc session.IncarnationID, namespace string) (*legacySource, error) {
	x := &legacySource{
		calls: make([][]*legacyOccurrence, len(old)), results: make([]*legacyOccurrence, len(old)),
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

func migrateLegacyMessages(old []legacyMessageDTO, inc session.IncarnationID, namespace string) ([]messageDTO, error) {
	source, err := indexLegacySource(old, inc, namespace)
	if err != nil {
		return nil, err
	}
	messages := make([]messageDTO, len(old))
	for i, m := range old {
		dto, err := source.migrateMessage(i, m, inc, namespace)
		if err != nil {
			return nil, err
		}
		messages[i] = dto
	}
	return messages, nil
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
