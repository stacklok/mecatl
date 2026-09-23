// Package sessnap provides a stable, JSON-friendly serialization ("snapshot")
// of a session.Session that the store adapters (memstore, jsonlstore) share.
//
// The Session aggregate exposes its lifecycle data through exported fields
// (ID, State, Mode, Conversation, Limits, Counters, EnvironmentRef, CreatedAt) and
// through the PendingAsk, PendingAuthorization, and PendingWorkspaceEnrollment
// accessors. Four pieces of its state are unexported and not directly
// addressable from outside the session package:
//
//   - pending *PendingAsk — readable via Session.PendingAsk() (only while
//     StateAwaiting) and restorable via Session.PauseForApproval() (only from
//     StateRunning). The snapshot round-trips it for the awaiting case.
//   - pendingAuthorization *PendingAuthorization — readable through a deep-copy
//     accessor while StateAuthorizing and restored through PauseForAuthorization.
//     Its private DTO preserves effective tool-argument bytes exactly.
//   - pendingWorkspaceEnrollment *PendingWorkspaceEnrollment — safe pre-prompt
//     correlation restored through BeginWorkspaceEnrollment.
//   - stop StopReason — the recorded terminal stop reason. It is captured
//     faithfully via Session.RecordedStopReason() (which performs no limit
//     derivation) and restored via the matching terminal transition
//     Complete/Stop/Cancel/Fail. Capturing the recorded value directly means the
//     snapshot no longer has to infer the reason from the conflated
//     Session.StopReason(), so terminal round-trips are exact.
package sessnap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// Snapshot is the on-the-wire form of a session.Session. It is a plain data
// struct with JSON tags so it serializes deterministically regardless of the
// (untagged) layout of the domain types.
type Snapshot struct {
	ID              session.SessionID       `json:"id"`
	State           session.State           `json:"state"`
	Mode            session.PermissionMode  `json:"mode"`
	Limits          session.Limits          `json:"limits"`
	Counters        session.Counters        `json:"counters"`
	ExternalBinding session.ExternalBinding `json:"external_binding,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	Incarnation     session.IncarnationID   `json:"incarnation,omitempty"`
	Messages        []messageDTO            `json:"messages"`
	Pending         *session.PendingAsk     `json:"pending,omitempty"`
	// PendingAuthorization is present exactly while StateAuthorizing. Its private
	// DTO base64-encodes tool arguments so JSON normalization cannot change bytes.
	PendingAuthorization *pendingAuthorizationDTO `json:"pending_authorization,omitempty"`
	// PendingWorkspaceEnrollment contains only safe enrollment correlation; no
	// endpoint, credential, callback, or discovered-service state is persisted.
	PendingWorkspaceEnrollment *session.PendingWorkspaceEnrollment `json:"pending_workspace_enrollment,omitempty"`
	StopReason                 session.StopReason                  `json:"stop_reason,omitempty"`
	// Kind and Relationship are the validated producer taxonomy from ADR 0217.
	// A missing kind is legacy data and restores as unknown (fail-closed).
	Kind         session.SessionKind         `json:"kind,omitempty"`
	Relationship session.SessionRelationship `json:"relationship,omitzero"`
	// Profile is the session's opaque tool-surface profile label. omitempty keeps a
	// v1 snapshot with no "profile" key decoding to "" (the default profile) —
	// purely additive, no format-tag bump (the same precedent as ProviderPhase /
	// Parts).
	Profile string `json:"profile,omitempty"`
	// ProviderID and ModelID are the session's opaque neutral provider+model
	// selector pair. omitempty keeps a v1 snapshot with no key decoding to the empty
	// pair ("server default") — additive, no version bump. Persisting them lets a
	// restarted process re-derive the SAME per-session engine via the factory.
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	// ReasoningEffort is the session's opaque neutral reasoning-effort token (ADR
	// 0055). omitempty keeps a pre-0055 snapshot with no key decoding to "" (unset)
	// — additive, no version bump. Persisting it lets a restarted process re-mint the
	// SAME per-session engine (the same-effort adapter) via the factory.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// DebugMCPServers names only the configured server-global MCP servers selected
	// for a debug session. DebugMCPTools is the exact direct-tool ceiling captured
	// at creation; neither field contains URLs, headers, or connection details.
	DebugMCPServers []string `json:"debug_mcp_servers,omitempty"`
	DebugMCPTools   []string `json:"debug_mcp_tools,omitempty"`
	// DebugTargetFingerprint is the non-projectable target-incarnation binding.
	DebugTargetFingerprint string `json:"debug_target_fingerprint,omitempty"`
	// Title is the session's human-readable label seeded from the first genuine
	// user prompt. omitempty keeps a pre-Title snapshot with no "title" key
	// decoding to "" — additive, no format-tag bump (the same precedent as
	// Profile / ProviderID / Parts). Persisting it lets a restarted process show
	// the label without re-deriving it. It is an inert stored label (like
	// Profile), restored by direct assignment, NOT a state transition.
	Title           string                  `json:"title,omitempty"`
	TitleProvenance session.TitleProvenance `json:"title_provenance,omitempty"`
	// TitleRevision is the title-specific durable metadata revision. omitempty
	// preserves the zero value for legacy snapshots.
	TitleRevision uint64 `json:"title_revision,omitempty"`
	// TitleGeneration metadata is distinct from main Usage and conversation.
	TitleGeneration    session.TitleGenerationState `json:"title_generation,omitempty"`
	TitleSourcePrompts []string                     `json:"title_source_prompts,omitempty"`
	TitleAttempts      []session.TitleAttempt       `json:"title_attempts,omitempty"`
	// TokenUsage is the canonical durable usage ledger. The writer always emits it;
	// an omitted empty ledger decodes to the zero value.
	TokenUsage map[session.UsageKind]session.TokenUsage `json:"token_usage"`
	// RetryDisposition and StreamProgress are the typed terminal facts for a failed
	// model stream. Missing fields decode conservatively to unknown.
	RetryDisposition session.RetryDisposition `json:"retry_disposition,omitempty"`
	StreamProgress   session.StreamProgress   `json:"stream_progress,omitempty"`
	// RetryPending persists the consumed failed-step retry intent across the crash window
	// between preparation and terminal completion. Its metadata remains separate
	// from failed-state metadata because the prepared aggregate is idle/running.
	RetryPending            bool                     `json:"retry_pending,omitempty"`
	RetryPendingDisposition session.RetryDisposition `json:"retry_pending_disposition,omitempty"`
	RetryPendingProgress    session.StreamProgress   `json:"retry_pending_progress,omitempty"`
	// LastError records a StateFailed session's terminal failure CAUSE
	// (session.RecordLastError, the Permanent-analog for the failure detail).
	// omitempty keeps a pre-#332 snapshot with no "last_error" key decoding to ""
	// — purely additive, no format-tag bump (the same precedent as Permanent). It
	// is normalised at stamp time (one line, rune-clamped), mirroring the
	// event-side subagentCausePayload so the snapshot and the subagent.end event
	// carry the same persisted cause.
	LastError string `json:"last_error,omitempty"`
	// RunID is the opaque, host-minted identity of the run this session is
	// currently driving or most recently drove (ADR 0249). Persisting it is what
	// makes an awaiting-approval resume continue THE SAME run across a process
	// restart: the resume path reads it back and reuses it instead of minting a
	// new one.
	//
	// omitempty keeps a pre-0245 snapshot with no "run_id" key decoding to "" —
	// purely additive, no format-tag bump (the Profile/ProviderID/Usage
	// precedent). A legacy session restores with no run id and is stamped on its
	// next run; there is no migration sweep.
	RunID string `json:"run_id,omitempty"`
	// Owner is the verified caller the session is attributed to (ADR 0204). A
	// POINTER for true omitempty: an ownerless session emits no "owner" key, so a
	// pre-ship snapshot decodes to a nil owner and an ownerless snapshot stays
	// byte-identical to a pre-ship one — purely additive, no format-tag bump.
	// Restored via the write-once aggregate method Session.RestoreLabels, NOT a
	// RestoreState parameter (widening that signature would be a Changed/breaking
	// entry under engine/COMPATIBILITY.md; a direct-assignment field is
	// Added/minor).
	Owner *session.Principal `json:"owner,omitempty"`
	// Authority is the plain, derived capability payload. A nil pointer is a
	// genuinely pre-feature legacy record; a present payload must decode to the
	// one governance.CapabilitySet representation or restore fails closed.
	Authority *session.Authority `json:"authority,omitempty"`
	// WorkspaceEnrollmentBrokerKeys is the exact completed broker registration-key
	// ledger. Presence distinguishes known empty provenance from legacy absence.
	WorkspaceEnrollmentBrokerKeys        []string `json:"workspace_enrollment_broker_keys,omitempty"`
	WorkspaceEnrollmentBrokerKeysPresent bool     `json:"workspace_enrollment_broker_keys_present,omitempty"`
	// EnvironmentRef is the sole durable execution-environment identity. It is
	// required and must contain the exact provider revision used for reattachment.
	EnvironmentRef session.EnvironmentRef `json:"environment_ref"`
	// Placement is safe display-only metadata and is never used for reattachment.
	Placement session.PlacementMetadata `json:"placement,omitempty"`
}

// messageDTO mirrors session.Message with JSON tags. session.Message is
// untagged; encoding it directly would still work, but a DTO keeps the wire
// format explicit and stable against future field reordering.
type messageDTO struct {
	Role       session.Role        `json:"role"`
	Text       string              `json:"text,omitempty"`
	ToolCalls  []session.ToolCall  `json:"tool_calls,omitempty"`
	ToolResult *session.ToolResult `json:"tool_result,omitempty"`
	Reasoning  string              `json:"reasoning,omitempty"`
	// ProviderPhase carries the OpenAI Responses opaque phase marker on an assistant
	// message. omitempty keeps a v1 snapshot with no "phase" key decoding to the
	// empty string ("no phase") — purely additive, no version bump. The json tag
	// stays "phase" so the persisted wire format is unchanged across the rename.
	ProviderPhase string `json:"phase,omitempty"`
	// ReasoningItemID carries the OpenAI Responses opaque reasoning-item id on an
	// assistant message (the reasoning item's provider-assigned id, replayed
	// verbatim on subsequent stateless turns). omitempty keeps a v1 snapshot with
	// no "reasoning_item_id" key decoding to the empty string — purely additive, no
	// version bump (the same precedent as ProviderPhase / Parts).
	ReasoningItemID string `json:"reasoning_item_id,omitempty"`
	// Parts carries non-text media on a user message. It is omitempty so a v1
	// snapshot with no "parts" key decodes to nil Parts — a text-only message,
	// exactly correct; the field is purely additive and needs no version bump.
	Parts []contentDTO `json:"parts,omitempty"`
	// UserPromptProvenance is additive; absent legacy snapshots remain unknown.
	UserPromptProvenance session.UserPromptProvenance `json:"user_prompt_provenance,omitempty"`
}

// contentDTO mirrors session.Content with JSON tags. Data []byte marshals as
// base64 automatically. Exactly one of data/url is set on a well-formed part.
type contentDTO struct {
	Kind     session.MediaKind `json:"kind"`
	MIMEType string            `json:"mime_type,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	URL      string            `json:"url,omitempty"`
}

// pendingAuthorizationDTO keeps the private continuation's effective argument
// bytes out of json.RawMessage encoding, which may normalize JSON whitespace.
// []byte uses encoding/json's base64 representation and therefore round-trips
// the exact bytes.
type pendingAuthorizationDTO struct {
	Authorization externalAuthorizationDTO `json:"authorization"`
	Call          authorizationCallDTO     `json:"call"`
	Deferred      []authorizationCallDTO   `json:"deferred,omitempty"`
}

type externalAuthorizationDTO struct {
	ID          string                       `json:"id"`
	DisplayName string                       `json:"display_name,omitempty"`
	Binding     session.AuthorizationBinding `json:"binding"`
	ExpiresAt   time.Time                    `json:"expires_at"`
}

type authorizationCallDTO struct {
	ID     session.ToolCallID `json:"id"`
	Name   string             `json:"name"`
	Args   []byte             `json:"args,omitempty"`
	ItemID string             `json:"item_id,omitempty"`
}

func toPendingAuthorizationDTO(p session.PendingAuthorization) *pendingAuthorizationDTO {
	dto := &pendingAuthorizationDTO{
		Authorization: externalAuthorizationDTO{
			ID:          p.Authorization.ID,
			DisplayName: p.Authorization.DisplayName,
			Binding:     p.Authorization.Binding,
			ExpiresAt:   p.Authorization.ExpiresAt,
		},
		Call: toAuthorizationCallDTO(p.Call),
	}
	dto.Deferred = make([]authorizationCallDTO, len(p.Deferred))
	for i, call := range p.Deferred {
		dto.Deferred[i] = toAuthorizationCallDTO(call)
	}
	return dto
}

func toAuthorizationCallDTO(call session.ToolCall) authorizationCallDTO {
	return authorizationCallDTO{
		ID:     call.ID,
		Name:   call.Name,
		Args:   append([]byte(nil), call.Args...),
		ItemID: call.ItemID,
	}
}

func fromPendingAuthorizationDTO(dto *pendingAuthorizationDTO) *session.PendingAuthorization {
	if dto == nil {
		return nil
	}
	pending := &session.PendingAuthorization{
		Authorization: session.ExternalAuthorization{
			ID:          dto.Authorization.ID,
			DisplayName: dto.Authorization.DisplayName,
			Binding:     dto.Authorization.Binding,
			ExpiresAt:   dto.Authorization.ExpiresAt,
		},
		Call: fromAuthorizationCallDTO(dto.Call),
	}
	pending.Deferred = make([]session.ToolCall, len(dto.Deferred))
	for i, call := range dto.Deferred {
		pending.Deferred[i] = fromAuthorizationCallDTO(call)
	}
	return pending
}

func fromAuthorizationCallDTO(dto authorizationCallDTO) session.ToolCall {
	return session.ToolCall{
		ID:     dto.ID,
		Name:   dto.Name,
		Args:   append([]byte(nil), dto.Args...),
		ItemID: dto.ItemID,
	}
}

// ErrNilSession is returned by Of when given a nil session.
var ErrNilSession = errors.New("sessnap: nil session")

// Of builds a Snapshot from a live Session, reading everything reachable
// through the Session's public surface.
func Of(s *session.Session) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, ErrNilSession
	}
	if err := s.ValidateAuthorizationState(); err != nil {
		return Snapshot{}, fmt.Errorf("sessnap: validate authorization state: %w", err)
	}
	relationship := s.Relationship
	if relationship.BranchIndex != nil {
		branchIndex := *relationship.BranchIndex
		relationship.BranchIndex = &branchIndex
	}
	snap := Snapshot{
		ID:                     s.ID,
		State:                  s.State,
		Mode:                   s.Mode,
		Limits:                 s.Limits,
		Counters:               s.Counters,
		ExternalBinding:        s.ExternalBinding,
		EnvironmentRef:         s.EnvironmentRef,
		Placement:              s.Placement,
		Profile:                s.Profile,
		ProviderID:             s.ProviderID,
		ModelID:                s.ModelID,
		ReasoningEffort:        s.ReasoningEffort,
		DebugMCPServers:        append([]string(nil), s.DebugMCPServers...),
		DebugMCPTools:          append([]string(nil), s.DebugMCPTools...),
		DebugTargetFingerprint: s.DebugTargetFingerprint,
		Title:                  s.Title,
		TitleProvenance:        s.TitleProvenance,
		TitleRevision:          s.TitleRevision,
		TitleGeneration:        s.TitleGeneration,
		TitleSourcePrompts:     s.TitleSourcePrompts(),
		TitleAttempts:          s.TitleAttempts(),
		TokenUsage:             s.TokenUsageSnapshot(),
		Kind:                   s.Kind,
		Relationship:           relationship,
		CreatedAt:              s.CreatedAt,
		Incarnation:            s.Incarnation(),
		// Owner is a pointer for true omitempty; Clone so the snapshot cannot
		// alias (and later mutate) the aggregate's own principal.
		Owner: s.Owner.Clone(),
	}
	if authority, ok := s.BoundAuthority(); ok {
		snap.Authority = &authority
	}
	snap.WorkspaceEnrollmentBrokerKeys, snap.WorkspaceEnrollmentBrokerKeysPresent = s.WorkspaceEnrollmentBrokerKeys()
	if s.Conversation != nil {
		snap.Messages = make([]messageDTO, len(s.Conversation.Messages))
		for i, m := range s.Conversation.Messages {
			snap.Messages[i] = toDTO(m)
		}
	}
	if ask, ok := s.PendingAsk(); ok {
		a := ask
		snap.Pending = &a
	}
	if pending, ok := s.PendingAuthorization(); ok {
		snap.PendingAuthorization = toPendingAuthorizationDTO(pending)
	}
	if pending, ok := s.PendingWorkspaceEnrollment(); ok {
		p := pending
		snap.PendingWorkspaceEnrollment = &p
	}
	// Capture the recorded terminal reason faithfully (no limit derivation) so a
	// terminal session round-trips through the matching transition on restore.
	if r, ok := s.RecordedStopReason(); ok {
		snap.StopReason = r
	}
	failure := s.FailureMetadata()
	snap.RetryDisposition, snap.StreamProgress = failure.Disposition, failure.Progress
	pendingRetry, pending := s.FailedStepRetryPending()
	snap.RetryPendingDisposition, snap.RetryPendingProgress, snap.RetryPending = pendingRetry.Disposition, pendingRetry.Progress, pending
	snap.LastError = s.LastError()
	snap.RunID = s.RunID()
	return snap, nil
}

// Restore reconstructs a Session from a Snapshot by driving the session state
// machine through its public constructors and transitions, so all invariants
// hold on the rebuilt aggregate.
func (s Snapshot) Restore() (*session.Session, error) {
	if !s.EnvironmentRef.Valid() {
		return nil, errors.New("sessnap: missing or invalid environment_ref")
	}
	restored := session.New(s.ID, s.Mode, s.EnvironmentRef, s.Limits, s.CreatedAt)
	if err := restored.RestoreSessionMetadata(s.Kind, s.Relationship); err != nil {
		return nil, fmt.Errorf("sessnap: restore session metadata: %w", err)
	}

	if err := restoreAuthority(restored, s.Authority); err != nil {
		return nil, err
	}
	if err := restored.RestoreWorkspaceEnrollmentBrokerKeys(s.WorkspaceEnrollmentBrokerKeys, s.WorkspaceEnrollmentBrokerKeysPresent); err != nil {
		return nil, fmt.Errorf("sessnap: restore workspace enrollment broker keys: %w", err)
	}

	// Rebuild the conversation history verbatim.
	for _, dto := range s.Messages {
		restored.Conversation.Append(fromDTO(dto))
	}
	// Restore opaque creation labels by direct assignment. Title-specific metadata
	// restores atomically through RestoreTitleMetadata below.
	restored.Profile = s.Profile
	restored.ProviderID = s.ProviderID
	restored.ModelID = s.ModelID
	restored.ReasoningEffort = s.ReasoningEffort
	restored.Placement = s.Placement
	restored.DebugMCPServers = append([]string(nil), s.DebugMCPServers...)
	restored.DebugMCPTools = append([]string(nil), s.DebugMCPTools...)
	restored.DebugTargetFingerprint = s.DebugTargetFingerprint
	restored.ExternalBinding = s.ExternalBinding
	// RunID restores by direct assignment, like Profile/Title above: it is an
	// inert stored label, not lifecycle state, so it does not belong in
	// RestoreState's state-machine parameter list.
	restored.BeginRun(s.RunID)
	restored.RestoreTitleMetadata(s.Title, s.TitleProvenance, s.TitleRevision, s.TitleGeneration, s.TitleSourcePrompts, s.TitleAttempts)
	// The identity labels go through the WRITE-ONCE aggregate method rather than a
	// field poke (Session is an aggregate) and rather than a RestoreState
	// parameter (that widening is Changed/breaking; this stays Added/minor).
	if err := restored.RestoreLabels(s.Owner, session.Authority{}); err != nil {
		return nil, fmt.Errorf("sessnap: restore labels: %w", err)
	}
	if err := restored.RestoreIncarnation(s.Incarnation); err != nil {
		return nil, fmt.Errorf("sessnap: restore incarnation: %w", err)
	}

	pendingAuthorization := fromPendingAuthorizationDTO(s.PendingAuthorization)
	data := RestoreData{
		State:                s.State,
		Stop:                 s.StopReason,
		Pending:              s.Pending,
		PendingAuthorization: pendingAuthorization,
		Counters:             s.Counters,
		TokenUsage:           s.TokenUsage,
		Failure:              session.RetryMetadata{Disposition: s.RetryDisposition, Progress: s.StreamProgress},
		RetryPending:         s.RetryPending,
		Retry:                session.RetryMetadata{Disposition: s.RetryPendingDisposition, Progress: s.RetryPendingProgress},
		LastError:            s.LastError,
	}
	if err := RestoreState(restored, data); err != nil {
		return nil, err
	}
	if s.PendingWorkspaceEnrollment != nil {
		if err := restored.BeginWorkspaceEnrollment(*s.PendingWorkspaceEnrollment); err != nil {
			return nil, fmt.Errorf("sessnap: restore workspace enrollment: %w", err)
		}
	}
	return restored, nil
}

func restoreAuthority(s *session.Session, authority *session.Authority) error {
	if authority == nil {
		return nil // Pre-feature record: legacy, deliberately unbound.
	}
	if err := ValidatePersistedAuthority(authority); err != nil {
		return fmt.Errorf("sessnap: restore authority: %w", err)
	}
	if err := s.BindAuthority(*authority); err != nil {
		return fmt.Errorf("sessnap: restore authority: %w", err)
	}
	return nil
}

// ValidatePersistedAuthority rejects an authority claim that cannot represent a
// usable derived capability set. Callers must distinguish a nil authority
// pointer caused by a genuinely absent legacy field before calling it.
func ValidatePersistedAuthority(authority *session.Authority) error {
	if authority == nil {
		return errors.New("missing authority claim")
	}
	set := authority.CapabilitySet
	if len(set.Tools) == 0 && set.RemainingDelegationDepth == 0 && !set.FileSystem && !set.DirectWrite {
		return errors.New("missing or empty capability set")
	}
	if !authority.Valid() {
		return errors.New("invalid authority payload")
	}
	return nil
}

// RestoreData is the adapter-owned complete lifecycle state used to rehydrate a
// freshly constructed session without positional compatibility parameters.
type RestoreData struct {
	State                session.State
	Stop                 session.StopReason
	Pending              *session.PendingAsk
	PendingAuthorization *session.PendingAuthorization
	Counters             session.Counters
	TokenUsage           map[session.UsageKind]session.TokenUsage
	Failure              session.RetryMetadata
	RetryPending         bool
	Retry                session.RetryMetadata
	LastError            string
}

// RestoreState drives a fresh idle Session to the supplied current state.
//
//nolint:gocyclo // The switch mirrors the complete session lifecycle state machine.
func RestoreState(s *session.Session, data RestoreData) error {
	if err := validateRestorePendingState(s, data.State, data.Pending, data.PendingAuthorization); err != nil {
		return err
	}
	s.Counters = data.Counters
	s.RestoreTokenUsage(data.TokenUsage)

	switch data.State {
	case session.StateIdle:
	case session.StateRunning:
		if err := beginTurnPreservingCounters(s, data.Counters); err != nil {
			return err
		}
	case session.StateAwaiting:
		if err := beginTurnPreservingCounters(s, data.Counters); err != nil {
			return err
		}
		if err := s.PauseForApproval(*data.Pending); err != nil {
			return fmt.Errorf("sessnap: restore awaiting: %w", err)
		}
	case session.StateAuthorizing:
		if err := beginTurnPreservingCounters(s, data.Counters); err != nil {
			return err
		}
		if err := s.PauseForAuthorization(*data.PendingAuthorization); err != nil {
			return fmt.Errorf("sessnap: restore authorizing: %w", err)
		}
	case session.StateCompleted:
		if data.Stop == session.StopNone || data.Stop == session.StopEndTurn {
			if err := s.Complete(); err != nil {
				return fmt.Errorf("sessnap: restore completed: %w", err)
			}
		} else if err := s.Stop(data.Stop); err != nil {
			return fmt.Errorf("sessnap: restore completed: %w", err)
		}
	case session.StateCancelled:
		if err := s.Cancel(); err != nil {
			return fmt.Errorf("sessnap: restore cancelled: %w", err)
		}
	case session.StateFailed:
		if err := s.Fail(); err != nil {
			return fmt.Errorf("sessnap: restore failed: %w", err)
		}
		if err := s.RecordFailureMetadata(data.Failure); err != nil {
			return fmt.Errorf("sessnap: restore failure metadata: %w", err)
		}
		if data.LastError != "" {
			if err := s.RecordLastError(data.LastError); err != nil {
				return fmt.Errorf("sessnap: record last error: %w", err)
			}
		}
	default:
		return fmt.Errorf("sessnap: unknown state %q", data.State)
	}
	if data.RetryPending {
		if err := s.RestoreFailedStepRetryPending(data.Retry); err != nil {
			return fmt.Errorf("sessnap: restore failed-step retry intent: %w", err)
		}
	}
	return nil
}

func validateRestorePendingState(s *session.Session, state session.State, pending *session.PendingAsk, pendingAuthorization *session.PendingAuthorization) error {
	switch state {
	case session.StateAwaiting:
		if pending == nil || pendingAuthorization != nil {
			return fmt.Errorf("sessnap: awaiting state/pending mismatch")
		}
	case session.StateAuthorizing:
		if pending != nil || pendingAuthorization == nil {
			return fmt.Errorf("sessnap: authorizing state/pending mismatch")
		}
		// Validate against the exact restored history without mutating s.
		probe := session.New(s.ID, s.Mode, s.EnvironmentRef, s.Limits, s.CreatedAt)
		probe.Conversation = s.Conversation
		if err := probe.BeginTurn(); err != nil {
			return fmt.Errorf("sessnap: validate authorizing: %w", err)
		}
		if err := probe.PauseForAuthorization(*pendingAuthorization); err != nil {
			return fmt.Errorf("sessnap: authorizing state/pending mismatch: %w", err)
		}
	default:
		if pending != nil || pendingAuthorization != nil {
			return fmt.Errorf("sessnap: pending value outside matching state")
		}
	}
	return nil
}

// beginTurnPreservingCounters enters StateRunning without letting BeginTurn's
// turn increment clobber the restored counters.
func beginTurnPreservingCounters(s *session.Session, want session.Counters) error {
	if err := s.BeginTurn(); err != nil {
		return fmt.Errorf("sessnap: restore running: %w", err)
	}
	s.Counters = want
	return nil
}

// Marshal encodes the snapshot of s as a single JSON line (no trailing newline).
func Marshal(s *session.Session) ([]byte, error) {
	snap, err := Of(s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(snap)
}

// Unmarshal decodes a JSON snapshot line and restores it into a Session.
func Unmarshal(line []byte) (*session.Session, error) {
	var wire struct {
		Authority json.RawMessage `json:"authority"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	if err := validateAuthorityWireClaim(wire.Authority); err != nil {
		return nil, fmt.Errorf("sessnap: decode authority: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	if err := validateWorkspaceEnrollmentBrokerKeysWire(fields); err != nil {
		return nil, fmt.Errorf("sessnap: decode workspace enrollment broker keys: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(line, &snap); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	return snap.Restore()
}

func validWorkspaceEnrollmentKeyEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return false
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return false
		}
		code, ok := jsonEscapeCode(raw[i+2 : i+6])
		if !ok {
			return false
		}
		i += 5
		if code < 0xd800 || code > 0xdfff {
			continue
		}
		if code >= 0xdc00 || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, ok := jsonEscapeCode(raw[i+3 : i+7])
		if !ok || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func jsonEscapeCode(raw []byte) (rune, bool) {
	code, err := strconv.ParseUint(string(raw), 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(code), true
}

func validateWorkspaceEnrollmentBrokerKeysWire(fields map[string]json.RawMessage) error {
	keysRaw, keysSupplied := fields["workspace_enrollment_broker_keys"]
	presentRaw, presentSupplied := fields["workspace_enrollment_broker_keys_present"]
	present := false
	if presentSupplied {
		if bytes.Equal(bytes.TrimSpace(presentRaw), []byte("null")) {
			return errors.New("null presence")
		}
		if err := json.Unmarshal(presentRaw, &present); err != nil {
			return errors.New("presence must be a boolean")
		}
	}
	var keys []string
	if keysSupplied && !bytes.Equal(bytes.TrimSpace(keysRaw), []byte("null")) {
		if !utf8.Valid(keysRaw) || !validWorkspaceEnrollmentKeyEscapes(keysRaw) {
			return errors.New("keys contain invalid UTF-8")
		}
		if err := json.Unmarshal(keysRaw, &keys); err != nil {
			return errors.New("keys must be an array of strings")
		}
	}
	if (!present && len(keys) != 0) || !session.ValidWorkspaceEnrollmentToolNames(keys) {
		return errors.New("invalid broker-key ledger")
	}
	return nil
}

func validateAuthorityWireClaim(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil // Genuinely pre-feature record: no authority field.
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null authority claim")
	}
	var claim map[string]json.RawMessage
	if err := json.Unmarshal(raw, &claim); err != nil {
		return err
	}
	set, ok := claim["capability_set"]
	if !ok || bytes.Equal(bytes.TrimSpace(set), []byte("null")) || bytes.Equal(bytes.TrimSpace(set), []byte("{}")) {
		return errors.New("missing or empty capability set")
	}
	return nil
}

func toDTO(m session.Message) messageDTO {
	return messageDTO{
		Role:                 m.Role,
		Text:                 m.Text,
		ToolCalls:            m.ToolCalls,
		ToolResult:           m.ToolResult,
		Reasoning:            m.Reasoning,
		ProviderPhase:        m.ProviderPhase,
		ReasoningItemID:      m.ReasoningItemID,
		Parts:                contentToDTO(m.Parts),
		UserPromptProvenance: m.UserPromptProvenance,
	}
}

func fromDTO(dto messageDTO) session.Message {
	return session.Message{
		Role:                 dto.Role,
		Text:                 dto.Text,
		ToolCalls:            dto.ToolCalls,
		ToolResult:           dto.ToolResult,
		Reasoning:            dto.Reasoning,
		ProviderPhase:        dto.ProviderPhase,
		ReasoningItemID:      dto.ReasoningItemID,
		Parts:                contentFromDTO(dto.Parts),
		UserPromptProvenance: dto.UserPromptProvenance,
	}
}

func contentToDTO(parts []session.Content) []contentDTO {
	if len(parts) == 0 {
		return nil
	}
	out := make([]contentDTO, len(parts))
	for i, p := range parts {
		out[i] = contentDTO{Kind: p.Kind, MIMEType: p.MIMEType, Data: p.Data, URL: p.URL}
	}
	return out
}

func contentFromDTO(parts []contentDTO) []session.Content {
	if len(parts) == 0 {
		return nil
	}
	out := make([]session.Content, len(parts))
	for i, p := range parts {
		out[i] = session.Content{Kind: p.Kind, MIMEType: p.MIMEType, Data: p.Data, URL: p.URL}
	}
	return out
}
