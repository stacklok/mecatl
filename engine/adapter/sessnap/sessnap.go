// Package sessnap provides a stable, JSON-friendly serialization ("snapshot")
// of a session.Session that the store adapters (memstore, jsonlstore) share.
//
// The Session aggregate exposes its lifecycle data through exported fields
// (ID, State, Mode, Conversation, Limits, Counters, EnvironmentRef, CreatedAt) and
// through the PendingAsk accessor. Two pieces of its state are unexported and
// not directly addressable from outside the session package:
//
//   - pending *PendingAsk — readable via Session.PendingAsk() (only while
//     StateAwaiting) and restorable via Session.PauseForApproval() (only from
//     StateRunning). The snapshot round-trips it for the awaiting case.
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
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// Snapshot is the on-the-wire form of a session.Session. It is a plain data
// struct with JSON tags so it serializes deterministically regardless of the
// (untagged) layout of the domain types.
type Snapshot struct {
	ID          session.SessionID      `json:"id"`
	State       session.State          `json:"state"`
	Mode        session.PermissionMode `json:"mode"`
	Limits      session.Limits         `json:"limits"`
	Counters    session.Counters       `json:"counters"`
	CreatedAt   time.Time              `json:"created_at"`
	Incarnation session.IncarnationID  `json:"incarnation,omitempty"`
	Messages    []messageDTO           `json:"messages"`
	Pending     *session.PendingAsk    `json:"pending,omitempty"`
	StopReason  session.StopReason     `json:"stop_reason,omitempty"`
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
	// TitleGeneration metadata is distinct from main Usage and conversation.
	TitleGeneration    session.TitleGenerationState `json:"title_generation,omitempty"`
	TitleSourcePrompts []string                     `json:"title_source_prompts,omitempty"`
	TitleAttempts      []session.TitleAttempt       `json:"title_attempts,omitempty"`
	// TokenUsage is the canonical durable usage ledger. A missing map is legacy;
	// restore derives honest unknown attribution from deprecated projections.
	TokenUsage map[session.UsageKind]session.TokenUsage `json:"token_usage,omitempty"`
	// Usage is the deprecated cumulative run-token accounting, a POINTER for true omitempty
	// (matching the Pending precedent): a zero Usage marshals nothing and a v1
	// snapshot with no "usage" key decodes to a nil pointer => the zero Usage on
	// restore. It is what the MaxRunTokens budget brake is evaluated against, so
	// persisting it lets the budget survive restart.
	Usage *session.Usage `json:"usage,omitempty"`
	// Permanent records whether a StateFailed session's failure was flagged as
	// permanent (session.RecordFailurePermanence). omitempty keeps a pre-flag
	// snapshot with no "permanent" key decoding to false — purely additive, no
	// format-tag bump.
	Permanent bool `json:"permanent,omitempty"`
	// RetryDisposition and StreamProgress are the typed terminal facts for a failed
	// model stream. Missing legacy fields decode conservatively to unknown.
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
}

// contentDTO mirrors session.Content with JSON tags. Data []byte marshals as
// base64 automatically. Exactly one of data/url is set on a well-formed part.
type contentDTO struct {
	Kind     session.MediaKind `json:"kind"`
	MIMEType string            `json:"mime_type,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	URL      string            `json:"url,omitempty"`
}

// ErrNilSession is returned by Of when given a nil session.
var ErrNilSession = errors.New("sessnap: nil session")

// Of builds a Snapshot from a live Session, reading everything reachable
// through the Session's public surface.
func Of(s *session.Session) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, ErrNilSession
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
		TitleGeneration:        s.TitleGeneration,
		TitleSourcePrompts:     s.TitleSourcePrompts(),
		TitleAttempts:          s.TitleAttempts(),
		TokenUsage:             cloneTokenUsage(s.TokenUsage),
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
	// Usage is a pointer for true omitempty: only emit the key when there is spend
	// to persist, so a zero-usage snapshot stays byte-identical to a pre-Usage one.
	if s.Usage != (session.Usage{}) {
		u := s.Usage
		snap.Usage = &u
	}
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
	// Capture the recorded terminal reason faithfully (no limit derivation) so a
	// terminal session round-trips through the matching transition on restore.
	if r, ok := s.RecordedStopReason(); ok {
		snap.StopReason = r
	}
	snap.Permanent = s.FailurePermanence()
	snap.RetryDisposition, snap.StreamProgress = s.FailureMetadata()
	snap.RetryPendingDisposition, snap.RetryPendingProgress, snap.RetryPending = s.FailedStepRetryPending()
	snap.LastError = s.LastError()
	snap.RunID = s.RunID()
	return snap, nil
}

// Restore reconstructs a Session from a Snapshot by driving the session state
// machine through its public constructors and transitions, so all invariants
// hold on the rebuilt aggregate.
func (snap Snapshot) Restore() (*session.Session, error) {
	if !snap.EnvironmentRef.Valid() {
		return nil, errors.New("sessnap: missing or invalid environment_ref")
	}
	s := session.New(snap.ID, snap.Mode, snap.EnvironmentRef, snap.Limits, snap.CreatedAt)
	if err := s.RestoreSessionMetadata(snap.Kind, snap.Relationship); err != nil {
		return nil, fmt.Errorf("sessnap: restore session metadata: %w", err)
	}

	if err := restoreAuthority(s, snap.Authority); err != nil {
		return nil, err
	}

	// Rebuild the conversation history verbatim.
	for _, dto := range snap.Messages {
		s.Conversation.Append(fromDTO(dto))
	}
	// Restore the inert creation labels by direct assignment — exported authoritative
	// values, with no state transition. Profile / ProviderID / ModelID /
	// ReasoningEffort / EnvironmentRef are opaque to the domain.
	s.Profile = snap.Profile
	s.ProviderID = snap.ProviderID
	s.ModelID = snap.ModelID
	s.ReasoningEffort = snap.ReasoningEffort
	s.Placement = snap.Placement
	s.DebugMCPServers = append([]string(nil), snap.DebugMCPServers...)
	s.DebugMCPTools = append([]string(nil), snap.DebugMCPTools...)
	s.DebugTargetFingerprint = snap.DebugTargetFingerprint
	// RunID restores by direct assignment, like Profile/Title above: it is an
	// inert stored label, not lifecycle state, so it does not belong in
	// RestoreState's state-machine parameter list.
	s.BeginRun(snap.RunID)
	s.Title = snap.Title
	s.TitleProvenance = snap.TitleProvenance
	s.RestoreTitleMetadata(snap.TitleGeneration, snap.TitleSourcePrompts, snap.TitleAttempts)
	if snap.TokenUsage != nil {
		s.RestoreTokenUsage(snap.TokenUsage)
	}
	// The identity labels go through the WRITE-ONCE aggregate method rather than a
	// field poke (Session is an aggregate) and rather than a RestoreState
	// parameter (that widening is Changed/breaking; this stays Added/minor).
	if err := s.RestoreLabels(snap.Owner, session.Authority{}); err != nil {
		return nil, fmt.Errorf("sessnap: restore labels: %w", err)
	}
	if err := s.RestoreIncarnation(snap.Incarnation); err != nil {
		return nil, fmt.Errorf("sessnap: restore incarnation: %w", err)
	}

	// The cumulative usage to seed (a nil pointer => the zero Usage, the pre-Usage
	// default), passed to RestoreState alongside the counters so it seeds the budget
	// AFTER the state machine advances (Usage must survive the BeginTurn the
	// running/awaiting restore performs).
	var usage session.Usage
	if snap.Usage != nil {
		usage = *snap.Usage
	}

	// Drive the state machine to the recorded lifecycle state, seed the running
	// totals + cumulative usage. New() lands in StateIdle; RestoreState advances.
	if err := RestoreState(s, snap.State, snap.StopReason, snap.Pending, snap.Counters, usage, snap.Permanent, snap.LastError); err != nil {
		return nil, err
	}
	if snap.TokenUsage == nil && usage != (session.Usage{}) {
		s.RestoreTokenUsage(map[session.UsageKind]session.TokenUsage{
			session.UsageKindMain: {Total: usage, Models: map[string]session.Usage{"unknown": usage}},
		})
	}
	if snap.State == session.StateFailed {
		disposition := snap.RetryDisposition
		if disposition == session.RetryDispositionUnknown && snap.Permanent {
			disposition = session.RetryDispositionPermanent
		}
		if err := s.RecordFailureMetadata(disposition, snap.StreamProgress); err != nil {
			return nil, fmt.Errorf("sessnap: restore failure metadata: %w", err)
		}
	}
	if snap.RetryPending {
		if err := s.RestoreFailedStepRetryPending(snap.RetryPendingDisposition, snap.RetryPendingProgress); err != nil {
			return nil, fmt.Errorf("sessnap: restore failed-step retry intent: %w", err)
		}
	}
	return s, nil
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

// RestoreState drives a freshly-constructed (StateIdle) Session through the state
// machine to the target lifecycle state, seeding the running totals (counters) and
// cumulative usage. It is the SINGLE place the terminal/awaiting transition
// vocabulary lives, shared by Snapshot.Restore (snapshot rehydration) and the
// event-sourced fold (engine/adapter/eventsource) so the state-driving logic is
// never copy-pasted.
//
// s MUST be a fresh StateIdle session (e.g. straight from session.New) with its
// conversation already seeded; RestoreState only advances the lifecycle. stop is the
// recorded terminal stop reason (used for the completed-vs-stop distinction); pending
// is the parked ask (used only for StateAwaiting). counters seed the running totals
// (preserved across the BeginTurn that running/awaiting restore performs); usage
// seeds the cumulative budget figure; permanent records a permanence flag on
// StateFailed (meaningful only when state==StateFailed and permanent==true);
// lastError records the terminal failure cause on StateFailed (the Permanent-analog
// for the failure detail, issue #332 — meaningful only when state==StateFailed and
// lastError!=""). It returns an error on an unknown state or a transition the
// aggregate rejects.
func RestoreState(
	s *session.Session,
	state session.State,
	stop session.StopReason,
	pending *session.PendingAsk,
	counters session.Counters,
	usage session.Usage,
	permanent bool,
	lastError string,
) error {
	// Restore running totals directly; these are exported and authoritative.
	s.Counters = counters
	// Usage seeds the budget so it survives restart.
	s.Usage = usage

	switch state {
	case session.StateIdle:
		// already idle
	case session.StateRunning:
		if err := beginTurnPreservingCounters(s, counters); err != nil {
			return err
		}
	case session.StateAwaiting:
		if err := beginTurnPreservingCounters(s, counters); err != nil {
			return err
		}
		ask := session.PendingAsk{}
		if pending != nil {
			ask = *pending
		}
		if err := s.PauseForApproval(ask); err != nil {
			return fmt.Errorf("sessnap: restore awaiting: %w", err)
		}
	case session.StateCompleted:
		// Stop(reason) records the exact captured reason; Complete is the special
		// case for a plain end-of-turn (StopEndTurn, or StopNone for an older
		// snapshot that predates the recorded reason). Because RecordedStopReason
		// captured the value faithfully, there is no inference here.
		if stop == session.StopNone || stop == session.StopEndTurn {
			if err := s.Complete(); err != nil {
				return fmt.Errorf("sessnap: restore completed: %w", err)
			}
		} else if err := s.Stop(stop); err != nil {
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
		if err := recordFailedStateFlags(s, permanent, lastError); err != nil {
			return err
		}
	default:
		return fmt.Errorf("sessnap: unknown state %q", state)
	}
	return nil
}

// recordFailedStateFlags stamps the optional StateFailed metadata (permanence +
// terminal cause) after the Fail() transition. Extracted from RestoreState so the
// state-machine switch stays under the gocyclo budget; each flag is independently
// guarded (meaningful only when non-zero/non-empty).
func recordFailedStateFlags(s *session.Session, permanent bool, lastError string) error {
	if permanent {
		if err := s.RecordFailurePermanence(true); err != nil {
			return fmt.Errorf("sessnap: record failure permanence: %w", err)
		}
	}
	if lastError != "" {
		if err := s.RecordLastError(lastError); err != nil {
			return fmt.Errorf("sessnap: record last error: %w", err)
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

func cloneTokenUsage(in map[session.UsageKind]session.TokenUsage) map[session.UsageKind]session.TokenUsage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[session.UsageKind]session.TokenUsage, len(in))
	for kind, bucket := range in {
		models := make(map[string]session.Usage, len(bucket.Models))
		for model, usage := range bucket.Models {
			models[model] = usage
		}
		out[kind] = session.TokenUsage{Total: bucket.Total, Models: models}
	}
	return out
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	for _, legacy := range []string{"workspace", "adoption_source_id", "adoption_request_digest"} {
		if _, ok := fields[legacy]; ok {
			return nil, fmt.Errorf("sessnap: unsupported legacy duplicate placement field %q", legacy)
		}
	}
	var wire struct {
		Authority json.RawMessage `json:"authority"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	if err := validateAuthorityWireClaim(wire.Authority); err != nil {
		return nil, fmt.Errorf("sessnap: decode authority: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(line, &snap); err != nil {
		return nil, fmt.Errorf("sessnap: decode snapshot: %w", err)
	}
	return snap.Restore()
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
		Role:            m.Role,
		Text:            m.Text,
		ToolCalls:       m.ToolCalls,
		ToolResult:      m.ToolResult,
		Reasoning:       m.Reasoning,
		ProviderPhase:   m.ProviderPhase,
		ReasoningItemID: m.ReasoningItemID,
		Parts:           contentToDTO(m.Parts),
	}
}

func fromDTO(dto messageDTO) session.Message {
	return session.Message{
		Role:            dto.Role,
		Text:            dto.Text,
		ToolCalls:       dto.ToolCalls,
		ToolResult:      dto.ToolResult,
		Reasoning:       dto.Reasoning,
		ProviderPhase:   dto.ProviderPhase,
		ReasoningItemID: dto.ReasoningItemID,
		Parts:           contentFromDTO(dto.Parts),
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
