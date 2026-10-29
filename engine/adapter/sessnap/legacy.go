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
		return Snapshot{}, errors.New("sessnap: legacy delegation call relationship is unsupported")
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

// legacySourceToolCallID derives a stable canonical ID from immutable source
// coordinates, so repeated loads preserve references before a migrated snapshot saves.
func legacySourceToolCallID(inc session.IncarnationID, namespace string, ordinal, slot int) session.ToolCallID {
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

func migrateLegacyMessages(old []legacyMessageDTO, inc session.IncarnationID, namespace string) ([]messageDTO, error) {
	messages := make([]messageDTO, len(old))
	open := make(map[string]session.ToolCallID)
	for i, m := range old {
		if len(m.Replay) != 0 {
			return nil, errors.New("sessnap: replay carrier without identity schema")
		}
		dto := messageDTO{Role: m.Role, Text: m.Text, Parts: m.Parts, ProviderPhase: m.Phase, UserPromptProvenance: m.UserPromptProvenance}
		replay := &session.MessageReplay{Provider: legacyReplay(m)}
		if m.Role != session.RoleAssistant && (len(m.ToolCalls) != 0 || replay.Provider != nil) {
			return nil, errors.New("sessnap: legacy calls or replay on non-assistant message")
		}
		for j, call := range m.ToolCalls {
			if call.ID == "" {
				return nil, errors.New("sessnap: empty legacy call ID")
			}
			if _, exists := open[call.ID]; exists {
				return nil, errors.New("sessnap: ambiguous legacy call ID")
			}
			id := legacySourceToolCallID(inc, namespace, i, j)
			open[call.ID] = id
			dto.ToolCalls = append(dto.ToolCalls, session.ToolCall{ID: id, Name: call.Name, Args: call.Args})
			replay.Bindings = append(replay.Bindings, session.ToolCallBinding{CallID: id, PairingID: session.ProviderCallID(call.ID), ItemID: session.ProviderItemID(call.ItemID)})
		}
		if m.ToolResult != nil {
			occ, exists := open[m.ToolResult.CallID]
			if !exists || m.Role != session.RoleTool {
				return nil, errors.New("sessnap: orphaned or wrong-role legacy result")
			}
			dto.ToolResult = &session.ToolResult{CallID: occ, Content: m.ToolResult.Content, IsError: m.ToolResult.IsError, Parts: m.ToolResult.Parts}
			delete(open, m.ToolResult.CallID)
		} else if m.Role == session.RoleTool {
			return nil, errors.New("sessnap: legacy tool message without result")
		}
		if replay.Provider != nil || len(replay.Bindings) != 0 {
			dto.Replay = replay
		}
		messages[i] = dto
	}
	if len(open) != 0 {
		return nil, errors.New("sessnap: incomplete legacy tool pairing")
	}
	return messages, nil
}
