package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
)

// SessionID uniquely identifies a session. Outside code holds a SessionID and
// reaches inner entities only through the Session aggregate root.
type SessionID string

// ExternalBinding is an opaque composition-issued identity for process-external
// session state. The session aggregate persists it without interpretation; the
// empty value means no external runtime is bound.
type ExternalBinding string

// State is the session lifecycle state. The state machine is:
//
//	idle → running → completed
//	          ↕ awaiting
//	          ↕ authorizing
//
// Awaiting is resumed only through ResumeWith. Authorizing can leave only
// through ClaimAuthorization, AbortAuthorization, or InterruptAuthorization;
// generic terminal transitions reject it.
//
// Cancel and Fail are otherwise permitted from any non-terminal state. completed, failed, and cancelled are terminal. Each terminal
// state has its own recovery seam back to idle: Reopen (from completed), Interrupt
// (from cancelled, also repairing the interrupted turn's history), Recover
// (from failed, same history repair — issue #51), and Abandon (from running,
// same history repair, for a session left running by a process that exited
// mid-turn — issue #475).
type State string

const (
	// StateIdle is the initial state: created, no turn running.
	StateIdle State = "idle"
	// StateRunning means a turn is in flight.
	StateRunning State = "running"
	// StateAwaiting means the loop is paused on a permission "ask".
	StateAwaiting State = "awaiting"
	// StateAuthorizing means the loop is paused on an external authorization.
	StateAuthorizing State = "authorizing"
	// StateCompleted is a terminal success state.
	StateCompleted State = "completed"
	// StateFailed is a terminal failure state.
	StateFailed State = "failed"
	// StateCancelled is a terminal cancellation state.
	StateCancelled State = "cancelled"
)

// IsTerminal reports whether no further transitions are possible from s.
func (s State) IsTerminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// PermissionMode is the session-wide permission posture, which drives plan-mode
// tool filtering and acceptEdits behaviour.
type PermissionMode string

const (
	// ModeDefault is the standard deny→ask→allow posture.
	ModeDefault PermissionMode = "default"
	// ModePlan enforces a read-only toolset (plan mode).
	ModePlan PermissionMode = "plan"
	// ModeAccept auto-accepts edits (acceptEdits).
	ModeAccept PermissionMode = "acceptEdits"
)

// ApprovalVerdict is the client's resolution of a permission.ask. It widens the
// historical allow/deny boolean into three outcomes so a client can ask the
// harness to LEARN an allow for the matching tool+pattern (allow_always) versus
// permitting only the current call (allow_once).
//
// VerdictDeny is the ZERO VALUE deliberately: a verdict that is never set, or a
// resolution path that abandons the ask (ctx cancel, transport error), fails
// SAFE to deny. A learned allow (VerdictAllowAlways) NEVER overrides a deny and
// NEVER bypasses plan-mode mutation denial — it only adds a narrow,
// session-scoped allow rule the Evaluator consults at the lowest precedence.
type ApprovalVerdict int

const (
	// VerdictDeny denies the call. It is the zero value (fail-safe default).
	VerdictDeny ApprovalVerdict = iota
	// VerdictAllowOnce allows the current call only; nothing is learned.
	VerdictAllowOnce
	// VerdictAllowAlways allows the current call AND asks the harness to learn a
	// per-session allow rule for the same tool + exact canonical pattern.
	VerdictAllowAlways
)

// StopReason explains why a run stopped. It is carried on terminal events and
// in the LLM provider's terminal chunk.
type StopReason string

const (
	// StopNone is the zero value: the run has not stopped.
	StopNone StopReason = ""
	// StopEndTurn means the model finished without requesting more tools.
	StopEndTurn StopReason = "end_turn"
	// StopMaxTurns means the MaxTurns limit was reached.
	StopMaxTurns StopReason = "max_turns"
	// StopMaxToolCalls means the MaxToolCalls limit was reached.
	StopMaxToolCalls StopReason = "max_tool_calls"
	// StopMaxConsecutiveFailures means too many tool calls failed in a row.
	StopMaxConsecutiveFailures StopReason = "max_consecutive_failures"
	// StopCancelled means the run was cancelled by the client or ctx.
	StopCancelled StopReason = "cancelled"
	// StopError means the run failed with an unrecoverable error.
	StopError StopReason = "error"
	// StopNoProgress means the model produced NEITHER tool calls NOR meaningful text
	// across the bounded continuation-nudge budget; the run ended without a
	// deliverable. It is a CLEAN terminal (not StopError — nothing failed), routed
	// through the same completed path as StopEndTurn, so the session ends COMPLETED
	// and stays Reopen-recoverable. It is distinguishable from StopEndTurn so a
	// client/team can tell "the model went silent" from "the model finished". It maps
	// to the proto stop string verbatim (no proto enum; the wire stop field is a
	// string passthrough).
	StopNoProgress StopReason = "no_progress"
	// StopBudget means the run's cumulative token usage crossed the configured
	// loop-level ceiling (Deps.MaxRunTokens), checked at a turn boundary. It is the
	// shared runaway brake serving every delegation path (main + Subagent + Team + Fork):
	// each engine inherits the ceiling and a per-call override may TIGHTEN it. Like
	// StopNoProgress it is a CLEAN terminal (not StopError — nothing failed), routed
	// through the same completed path as StopEndTurn, so the session ends COMPLETED and
	// stays Reopen-recoverable. The check is at the turn boundary (never mid-stream), so
	// the in-flight turn always completes and the no-replay-after-first-chunk invariant
	// holds. It maps to the proto stop string verbatim (no proto enum; the wire stop
	// field is a string passthrough, exactly like StopNoProgress).
	StopBudget StopReason = "budget"
	// StopTimeout means a per-fire wall-clock deadline expired (issue #386, the
	// in-flight scheduled-fire state). Like StopBudget it is a CLEAN terminal
	// (not StopError — nothing failed, the fire ran out of its allotted time), a
	// budget exhaustion rather than a fault or cancel, routed through the same
	// completed path as StopEndTurn, so the session ends COMPLETED and stays
	// Reopen-recoverable. It is distinct from a per-call Subagent time-budget
	// timeout (which lands StopCancelled on the child and renders as a tool
	// error); StopTimeout is the scheduled-fire wall-clock deadline, checked at a
	// turn boundary exactly like StopBudget. It maps to the proto stop string
	// verbatim (no proto enum; the wire stop field is a string passthrough, exactly
	// like StopNoProgress / StopBudget).
	StopTimeout StopReason = "timeout"
	// StopStructuredOutput means a structured-output (output_schema) child run
	// exhausted its bounded SubmitResult validation-retry budget without ever
	// producing a schema-valid payload. Like StopNoProgress / StopBudget it is a
	// CLEAN terminal (not StopError — the run did not crash, it just failed to
	// satisfy the requested schema), routed through the completed path so the session
	// ends COMPLETED and stays Reopen-recoverable. The Subagent tool renders it as a
	// model-visible tool error carrying the last validation failure, so the failure
	// reaches the model (never only a log line). It maps to the proto stop string
	// verbatim (no proto enum; the wire stop field is a string passthrough, exactly
	// like StopNoProgress / StopBudget).
	StopStructuredOutput StopReason = "structured_output"
	// StopPlanApproved means the operator approved a presented plan (issue #206:
	// the plan-approval gate). Like StopNoProgress / StopBudget / StopStructuredOutput
	// it is a CLEAN terminal (not StopError — nothing failed; the approval is a
	// success outcome), routed through the completed path so the session ends COMPLETED
	// and stays Reopen-recoverable. It is emitted when an operator approves a plan the
	// model presented via the PresentPlan tool; the session is typically flipped out of
	// plan mode and the next run continues the now-approved work. It maps to the proto
	// stop string verbatim (no proto enum; the wire stop field is a string passthrough,
	// exactly like StopNoProgress / StopBudget / StopStructuredOutput).
	StopPlanApproved StopReason = "plan_approved"
	// StopPlanIterate means the operator chose to iterate on a presented plan
	// (issue #206: the plan-approval gate). It is the iterate/deny sibling of
	// StopPlanApproved: a deny of a plan ask TERMINATES the plan run CLEANLY instead
	// of continuing in-turn, so the operator's NEXT typed prompt drives the
	// revision (the old behaviour kept the model working with no operator input).
	// Like StopPlanApproved it is a CLEAN non-error terminal (not StopError — the
	// operator asked for edits, nothing failed), routed through the completed path
	// so the session ends COMPLETED and stays Reopen-recoverable. It maps to the
	// proto stop string verbatim (no proto enum; the wire stop field is a string
	// passthrough, exactly like StopPlanApproved).
	StopPlanIterate StopReason = "plan_iterate"
)

// Limits are the configured stop conditions for a session. A zero value in any
// field disables that particular limit.
type Limits struct {
	// MaxTurns caps the number of model calls; 0 disables.
	MaxTurns int
	// MaxToolCalls caps the total tool invocations; 0 disables.
	MaxToolCalls int
	// MaxConsecutiveFailures caps back-to-back tool failures; 0 disables.
	MaxConsecutiveFailures int
}

// WithDefaults returns l with each zero field filled from d. A caller that pins
// only some caps (say MaxTurns) keeps the rest from d rather than disabling them:
// a zero field means "unset", not "unlimited", once a default is supplied. An
// all-zero l yields d unchanged; a fully-set l is returned verbatim. This mirrors
// the per-field merge the subagent and team layers already use (defLimits,
// mergeLimits), so a partial Limits behaves the same wherever defaults apply.
func (l Limits) WithDefaults(d Limits) Limits {
	if l.MaxTurns == 0 {
		l.MaxTurns = d.MaxTurns
	}
	if l.MaxToolCalls == 0 {
		l.MaxToolCalls = d.MaxToolCalls
	}
	if l.MaxConsecutiveFailures == 0 {
		l.MaxConsecutiveFailures = d.MaxConsecutiveFailures
	}
	return l
}

// Counters track running totals used to evaluate stop conditions.
type Counters struct {
	// Turns is the number of model calls begun.
	Turns int
	// ToolCalls is the total number of tool invocations recorded.
	ToolCalls int
	// ConsecutiveFailures is the current run of back-to-back tool failures; it
	// resets to zero on any successful tool result.
	ConsecutiveFailures int
}

// GuardrailApprovalKind distinguishes approval of an action from release of an
// already-produced result.
type GuardrailApprovalKind string

// Guardrail approval kinds.
const (
	GuardrailApprovalAction        GuardrailApprovalKind = "action"
	GuardrailApprovalResultRelease GuardrailApprovalKind = "result_release"
)

// GuardrailPendingScope is the machine-only scope attached to a guardrail ask.
type GuardrailPendingScope struct {
	ReviewID        string
	Kind            GuardrailApprovalKind
	GrantDigest     string
	SessionOnly     bool
	RepeatAvailable bool
}

// ApprovalOrigin is the explicit provenance of an approval.
type ApprovalOrigin string

// Approval origins.
const (
	ApprovalOriginUnknown       ApprovalOrigin = ""
	ApprovalOriginPermission    ApprovalOrigin = "permission"
	ApprovalOriginHookGuardrail ApprovalOrigin = "hook_guardrail"
	ApprovalOriginPlan          ApprovalOrigin = "plan"
)

// PendingAsk describes a permission prompt the loop is blocked on while in
// StateAwaiting. It is surfaced to the client via a permission.ask Event and
// resolved by ResumeWith.
type PendingAsk struct {
	// AskID correlates the ask with the client's resolution.
	AskID string
	// Tool is the name of the tool awaiting approval.
	Tool string
	// Args is the proposed tool-call argument payload.
	Args json.RawMessage
	// Reason explains why approval is required.
	Reason string
	// Call is the id of the gated ToolCall this ask pauses on.
	Call ToolCallID `json:"call,omitempty"`
	// Guardrail carries the exact guardrail approval class and repeat scope.
	Guardrail *GuardrailPendingScope `json:"guardrail,omitempty"`
	// Origin is serialized approval provenance. Unknown is fail-closed.
	Origin ApprovalOrigin `json:"origin,omitempty"`
	// AskProvenance is the run-local deterministic policy provenance.
	AskProvenance governance.AskProvenance `json:"-"`
}

// Errors returned by the Session state machine.
var (
	// ErrIllegalTransition is returned when a method is called in a state that
	// does not permit it.
	ErrIllegalTransition = errors.New("session: illegal state transition")
	// ErrNoPendingAsk is returned by ResumeWith when the session is not awaiting
	// approval.
	ErrNoPendingAsk = errors.New("session: no pending ask to resume")
	// ErrNoPendingAuthorization is returned when authorization resolution is
	// requested outside StateAuthorizing.
	ErrNoPendingAuthorization = errors.New("session: no pending external authorization")
)

// Session is the aggregate root of the Agent Session context. All mutation of
// the conversation, counters, and lifecycle flows through its intention-revealing
// methods so the state machine and stop conditions always hold. Outside code
// holds a SessionID and reaches inner entities only through these methods, never
// through public setters.
type Session struct {
	// ID identifies this session.
	ID SessionID
	// State is the current lifecycle state.
	State State
	// Mode is the permission posture.
	Mode PermissionMode
	// Conversation is the model-visible history.
	Conversation *Conversation
	// Limits are the configured stop conditions.
	Limits Limits
	// Counters are the running totals for stop-condition evaluation.
	Counters Counters
	// tokenUsage is the aggregate-owned canonical durable accounting ledger.
	// TokenUsageSnapshot returns an owned external view.
	tokenUsage map[UsageKind]TokenUsage
	// usageAttribution is the normalized run-scoped provider/model attribution
	// selected by composition. Empty means a restored or inert session has not
	// yet lazily derived it from its durable labels.
	usageAttribution string
	// EnvironmentRef is the sole durable identity of the execution environment.
	// It is minted by the placement provider and must be valid before persistence
	// or execution. Resolution to live capabilities belongs to composition.
	EnvironmentRef EnvironmentRef
	// Placement is safe display-only metadata minted by the placement provider.
	// It is persisted for public inventory projection but never used to bind or
	// reattach an environment.
	Placement PlacementMetadata
	// ExternalBinding is an opaque composition-issued binding to process-external
	// session state. The aggregate stores and persists it without interpretation.
	// An empty value means no external runtime is bound.
	ExternalBinding ExternalBinding
	// Profile is an opaque tool-surface profile label (e.g. "" for the default
	// filesystem profile, "no-fs" for the no-filesystem one). The aggregate STORES
	// it but never interprets it: the meaning lives entirely in composition, like
	// ProviderID and ModelID. It is a write-once creation label set by the
	// composition root after New (no mutator); persisting it lets a restarted
	// process rebuild the same tool surface. The exact EnvironmentRef remains the
	// sole placement identity.
	Profile string
	// ProviderID and ModelID are the opaque neutral provider+model selector pair
	// this session was bound to. The aggregate STORES them but never interprets
	// them — the ProviderSelector type and all resolution stay in composition; only
	// these two opaque strings cross into the domain. Persisting them lets a
	// restarted process re-derive the same per-session engine via the factory
	// instead of falling to the default-provider floor. They are write-once
	// creation labels set by composition after New (no mutator). The empty pair
	// means "server default".
	ProviderID string
	ModelID    string
	// ReasoningEffort is the opaque neutral reasoning-effort token (ADR 0055) this
	// session was bound to ("" = unset, the provider default). The aggregate STORES
	// it but never interprets it — the neutral vocabulary, normalisation, per-provider
	// clamp, and adapter re-mint all live in composition; only this opaque string
	// crosses into the domain (the same inert-label posture as ProviderID/ModelID).
	// Persisting it lets a restarted process re-mint the SAME per-session engine via
	// the factory instead of falling to the operator default. Write-once creation
	// label set by the composition root after New (no mutator).
	ReasoningEffort string
	// DebugMCPServers and DebugMCPTools are inert, durable labels for a debug
	// session's explicitly selected server-global MCP mount. DebugMCPTools is the
	// exact creation-time tool-name ceiling; rehydration requires exact equality
	// with the selected servers' current direct tool set.
	DebugMCPServers []string
	DebugMCPTools   []string
	// DebugTargetFingerprint binds a debug session to the target incarnation that
	// was authorized at creation. It is internal evidence only and is never
	// projected to clients or the model.
	DebugTargetFingerprint string
	// Title is a human-readable session label seeded ONCE from the first genuine
	// user prompt (via SetTitle, called from the loop's recordPrompt), clamped to
	// maxTitleRunes (120) runes. Subsequent prompts do NOT overwrite it (set-once).
	// It is persisted in the snapshot (an inert stored label, like Profile), and
	// read-time consumers (the lazy ListSessions/GetSession fallback, the
	// event-sourced Fold) use session.IsGenuineUserPrompt to derive it when empty.
	// It is "" for a session with no genuine prompt yet (lazy display-time
	// fallback applies). The aggregate never interprets it.
	Title string
	// TitleProvenance records whether Title came from the first genuine prompt or
	// an explicit operator rename. The zero value means legacy/unknown.
	TitleProvenance TitleProvenance
	// TitleGeneration is automatic title generation's durable lifecycle. New
	// sessions default to disabled until composition explicitly enables it.
	TitleGeneration TitleGenerationState
	// TitleRevision advances once for each effective durable title metadata
	// mutation. It is independent of event sequences, environment revisions, and
	// event-log cursors; zero is the legacy value.
	TitleRevision uint64
	// titleSourcePrompts captures only the first three genuine non-empty principal
	// text prompts at prompt ingress; it never derives candidates from history.
	titleSourcePrompts []string
	// titleAttempts records durable title-generation lifecycle attempts.
	titleAttempts []TitleAttempt
	// Owner is the verified caller this session is attributed to, or nil when the
	// session is ownerless (a pre-ship snapshot, or a deployment with no identity
	// verifier wired). It is a WRITE-ONCE label stamped through RestoreLabels —
	// never a public setter, and never fabricated when identity is absent
	// (ADR 0204 decision 4). The aggregate STORES it and never interprets it: no
	// enforcement, no filtering, display + audit only.
	Owner *Principal
	// Authority is the derived authority payload stamped through BindAuthority
	// before the first runnable state. The private marker distinguishes a bound
	// empty capability set from a genuinely pre-feature legacy session.
	Authority Authority
	// Kind classifies the trusted producer and continuation posture. New creates
	// main sessions; delegated/scheduled producers use the validated constructors.
	Kind SessionKind
	// Relationship carries kind-specific durable lineage. It is empty for main
	// and legacy unknown sessions.
	Relationship SessionRelationship
	// CreatedAt is the creation timestamp.
	CreatedAt time.Time

	// incarnation is minted once by New and replaced only while idle during
	// restoration of persisted creation metadata.
	incarnation IncarnationID

	// authorityBound records that Authority was stamped through BindAuthority.
	// A zero Authority with this marker is impossible: BindAuthority validates the
	// payload before setting either field.
	authorityBound bool
	// pending is set iff State == StateAwaiting.
	pending *PendingAsk
	// pendingAuthorization is set iff State == StateAuthorizing.
	pendingAuthorization *PendingAuthorization
	// pendingWorkspaceEnrollment is safe pre-prompt correlation state. It is
	// independent of the agent-loop lifecycle and other pending continuations.
	pendingWorkspaceEnrollment *PendingWorkspaceEnrollment
	// stop holds the terminal stop reason once the session has stopped.
	stop StopReason
	// failureMetadata retains the typed terminal facts needed to decide failed-step
	// retry eligibility after restart. It is meaningful only in StateFailed and is
	// cleared by resetToIdle.
	failureMetadata RetryMetadata
	// retryPending records a durably-prepared exact model-step retry. It is
	// meaningful while idle or running and prevents a new user prompt from
	// bypassing the failed step after a process crash.
	retryPending  bool
	retryMetadata RetryMetadata
	// lastError records the terminal failure CAUSE (the loop's
	// session.ResultPayload.Error) when this session is in StateFailed. It is the
	// Permanent-analog for the failure detail itself: persisted on the snapshot so a
	// delegation's cause survives on the CHILD snapshot independent of the parent's
	// subagent.end emit (issue #332 — a background child's end-emit can lose the race
	// with the run-end seal, so the snapshot is the single durable source). It is
	// meaningful ONLY when State==StateFailed; resetToIdle clears it so a recovered
	// session never keeps a stale cause.
	lastError string
	// runID is the opaque, host-minted identity of the run this session is
	// CURRENTLY driving, or most recently drove (ADR 0249). It is persisted on the
	// snapshot, which is what makes an awaiting-approval resume continue THE SAME
	// run across a process restart: the resume path reads this value back and
	// reuses it instead of minting a new one, discharging ADR 0044's "stable
	// across processes for the same attempt" obligation mechanically.
	//
	// It is an inert stored label, like Profile: the aggregate never interprets
	// it, never validates its shape beyond emptiness, and no transition depends on
	// it. Deliberately NOT cleared by resetToIdle — the id of the run that just
	// ended stays readable until the next run replaces it, which is what lets a
	// terminal-state session still answer "which run was that?".
	runID string
}

// maxSnapshotErrorRunes caps how many runes of a StateFailed session's terminal
// cause are persisted on the snapshot. It mirrors the event-side
// maxSubagentCausePreview (engine/agent) so the snapshot and the subagent.end
// event agree byte-for-byte on the persisted cause. The session package cannot
// import engine/agent, so the constant lives here as a deliberate local mirror
// (two call sites — premature to extract a shared abstraction).
const maxSnapshotErrorRunes = 400

// normaliseSnapshotError collapses a failure cause to ONE line and clamps it to
// maxSnapshotErrorRunes. It mirrors engine/agent's subagentCausePayload normaliser
// (whitespace collapsed to single spaces, then rune-clamped) so the snapshot field
// and the event field carry the same persisted value; the two helpers must agree
// byte-for-byte, hence the explicit mirror note here.
func normaliseSnapshotError(cause string) string {
	return clampSnapshotRunes(strings.Join(strings.Fields(cause), " "), maxSnapshotErrorRunes)
}

// clampSnapshotRunes truncates s to at most n runes, appending an ellipsis on
// truncation. It is the session-local mirror of engine/agent's clampRunes (the
// session package cannot import engine/agent).
func clampSnapshotRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// New constructs an idle Session with an empty conversation and an exact
// durable environment identity. Callers must provide a valid provider-minted
// reference; persistence and run entry enforce the same invariant.
func New(id SessionID, mode PermissionMode, ref EnvironmentRef, limits Limits, createdAt time.Time) *Session {
	if !ref.Valid() {
		panic("session: New requires a valid environment ref")
	}
	return &Session{
		ID:              id,
		State:           StateIdle,
		Mode:            mode,
		Conversation:    &Conversation{},
		Limits:          limits,
		tokenUsage:      make(map[UsageKind]TokenUsage),
		EnvironmentRef:  ref,
		Kind:            SessionKindMain,
		TitleGeneration: TitleGenerationDisabled,
		CreatedAt:       createdAt,
		incarnation:     NewIncarnationID(),
	}
}

// BeginTurn transitions the session into StateRunning at the start of a model
// call. It is legal from StateIdle (first turn) or StateRunning (a follow-up
// model call within the same active run). It increments the turn counter. If a
// turn limit is already reached it returns ErrIllegalTransition is NOT used;
// callers should consult StopReason before beginning a turn.
func (s *Session) BeginTurn() error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("BeginTurn"); err != nil {
		return err
	}
	if s.State != StateIdle && s.State != StateRunning {
		return fmt.Errorf("%w: BeginTurn from %q", ErrIllegalTransition, s.State)
	}
	s.State = StateRunning
	s.Counters.Turns++
	return nil
}

// RecordAssistant appends the assistant message produced by a model call to the
// conversation. It is legal only while running.
func (s *Session) RecordAssistant(m Message) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: RecordAssistant from %q", ErrIllegalTransition, s.State)
	}
	s.Conversation.Append(m)
	return nil
}

// RecordToolResults appends tool-result messages to the conversation and updates
// the tool-call and consecutive-failure counters. It is legal only while
// running. Any successful result resets the consecutive-failure run; each error
// result extends it.
func (s *Session) RecordToolResults(results []ToolResult) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: RecordToolResults from %q", ErrIllegalTransition, s.State)
	}
	for _, r := range results {
		s.Conversation.Append(NewToolMessage(r))
		s.Counters.ToolCalls++
		if r.IsError {
			s.Counters.ConsecutiveFailures++
		} else {
			s.Counters.ConsecutiveFailures = 0
		}
	}
	return nil
}

// SetUsageAttribution selects the normalized provider/model attribution for
// subsequent main usage.
func (s *Session) SetUsageAttribution(providerID, modelID string) {
	s.usageAttribution = modelAttribution(providerID, modelID)
}

// RecordUsage accumulates the token usage of a model call onto the aggregate's
// canonical main ledger. It is the intention-revealing seam the loop uses instead
// of mutating accounting state directly, mirroring RecordAssistant/RecordToolResults:
// it is legal ONLY while running (a usage record belongs to an in-flight turn).
// Unlike Counters, main usage is deliberately NOT reset by resetToIdle.
func (s *Session) RecordUsage(u Usage) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: RecordUsage from %q", ErrIllegalTransition, s.State)
	}
	attribution := s.usageAttribution
	if attribution == "" {
		attribution = modelAttribution(s.ProviderID, s.ModelID)
		s.usageAttribution = attribution
	}
	s.recordTokenUsage(UsageKindMain, attribution, u)
	return nil
}

// UsageFor returns the authoritative total for a canonical usage bucket.
func (s *Session) UsageFor(kind UsageKind) Usage {
	return s.tokenUsage[kind].Total
}

// RecordUserPrompt appends a user prompt to the conversation through the
// aggregate root, optionally preceded by discovered project-instruction messages
// (AGENTS.md / CLAUDE.md). It is the intention-revealing seam the loop uses
// instead of poking the Conversation directly, so all history mutation flows
// through the root. It is legal from any non-terminal state (a prompt may be the
// first message while idle, or a follow-up while running).
func (s *Session) RecordUserPrompt(text string, instructions []Message) error {
	return s.RecordUserPromptWithParts(text, nil, instructions)
}

// RecordUserPromptWithParts is the multimodal sibling of RecordUserPrompt: it
// records a user prompt carrying flattened text PLUS non-text media parts
// (image/audio), optionally preceded by discovered project-instruction messages.
// text may be empty when parts carries the content. It shares
// RecordUserPrompt's state guard and instruction-prepend behaviour; the only
// difference is the recorded user message carries Parts. It is legal from any
// non-terminal state.
func (s *Session) RecordUserPromptWithParts(text string, parts []Content, instructions []Message) error {
	return s.recordUserPromptWithParts(text, parts, instructions, UserPromptProvenanceUnknown)
}

// RecordPrincipalPromptWithParts records a prompt whose principal provenance was
// authenticated by the root agent ingress. It is intentionally narrower than
// RecordUserPromptWithParts: arbitrary callers and legacy history remain unknown.
func (s *Session) RecordPrincipalPromptWithParts(text string, parts []Content, instructions []Message) error {
	return s.recordUserPromptWithParts(text, parts, instructions, UserPromptProvenancePrincipal)
}

// RecordHarnessPrompt records a harness-authored user-role continuation.
func (s *Session) RecordHarnessPrompt(text string) error {
	return s.recordUserPromptWithParts(text, nil, nil, UserPromptProvenanceHarness)
}

func (s *Session) recordUserPromptWithParts(text string, parts []Content, instructions []Message, provenance UserPromptProvenance) error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("RecordUserPrompt"); err != nil {
		return err
	}
	if s.State.IsTerminal() || (s.retryPending && s.State == StateIdle) || s.State == StateAuthorizing {
		return fmt.Errorf("%w: RecordUserPrompt from %q", ErrIllegalTransition, s.State)
	}
	for _, m := range instructions {
		s.Conversation.Append(m)
	}
	message := NewUserMessageWithParts(text, parts)
	message.UserPromptProvenance = provenance
	s.Conversation.Append(message)
	return nil
}

// ReplaceHistory atomically replaces the conversation history with messages. It
// is the compaction seam: the loop hands it the compacted message slice so the
// replacement flows through the root rather than mutating Conversation.Messages
// directly. It is legal only while running, when compaction occurs.
//
// As the aggregate-level guard it REJECTS a slice that is not tool-pairing-valid
// (an orphaned tool result or a dangling tool call) via ValidateToolPairing: such
// a history draws a provider HTTP 400 on the next replay and would drive the run
// to StateFailed. Refusing it here keeps the invariant
// that the conversation is always provider-replayable, independent of which
// compactor produced the slice.
func (s *Session) ReplaceHistory(messages []Message) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: ReplaceHistory from %q", ErrIllegalTransition, s.State)
	}
	if err := ValidateToolPairing(messages); err != nil {
		return fmt.Errorf("ReplaceHistory: %w", err)
	}
	s.Conversation.Messages = messages
	return nil
}

// ReplaceHistoryAtBoundary atomically replaces conversation history while no
// turn is in flight. It is the manual-compaction seam: legal from idle and all
// terminal states, and rejected from running or awaiting so an out-of-band
// rewrite cannot race a model turn or invalidate a pending approval. It changes
// only Conversation.Messages; lifecycle state and all other aggregate metadata
// are preserved.
//
// The replacement must satisfy ValidateToolPairing so the resulting history is
// provider-replayable in both directions.
func (s *Session) ReplaceHistoryAtBoundary(messages []Message) error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("ReplaceHistoryAtBoundary"); err != nil {
		return err
	}
	if s.State == StateRunning || s.State == StateAwaiting || s.State == StateAuthorizing {
		return fmt.Errorf("%w: ReplaceHistoryAtBoundary from %q", ErrIllegalTransition, s.State)
	}
	if err := ValidateToolPairing(messages); err != nil {
		return fmt.Errorf("ReplaceHistoryAtBoundary: %w", err)
	}
	s.Conversation.Messages = messages
	return nil
}

// SeedHistory atomically seeds a FRESH (idle) session's conversation history with
// messages. It is the IDLE-state sibling of ReplaceHistory (which is running-only,
// the compaction seam): SeedHistory exists for the fork:true Subagent child, whose
// session is freshly constructed and must be primed with a deep copy of the parent
// conversation BEFORE the first turn begins. It is the aggregate-mutation seam the
// agent package uses instead of poking Conversation.Messages directly, so the
// history-mutation discipline (and the pairing guard) holds.
//
// It is legal ONLY from StateIdle — a fresh, not-yet-run child. Seeding a session
// that has already begun a turn (or a terminal one) is a programming error and
// returns ErrIllegalTransition. As the aggregate-level guard it REJECTS a slice
// that is not tool-pairing-valid via ValidateToolPairing (an orphaned tool result
// or a dangling tool call would draw a provider HTTP 400 on the first replay), so
// the conversation is always provider-replayable regardless of what the caller
// supplies.
func (s *Session) SeedHistory(messages []Message) error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("SeedHistory"); err != nil {
		return err
	}
	if s.State != StateIdle {
		return fmt.Errorf("%w: SeedHistory from %q", ErrIllegalTransition, s.State)
	}
	if err := ValidateToolPairing(messages); err != nil {
		return fmt.Errorf("SeedHistory: %w", err)
	}
	s.Conversation.Messages = messages
	return nil
}

// PauseForApproval suspends a running turn on a permission ask, transitioning to
// StateAwaiting. It is legal only while running.
func (s *Session) PauseForApproval(ask PendingAsk) error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: PauseForApproval from %q", ErrIllegalTransition, s.State)
	}
	a := ask
	s.pending = &a
	s.State = StateAwaiting
	return nil
}

// PendingAsk returns the ask the session is blocked on and true when in
// StateAwaiting; otherwise it returns the zero value and false.
func (s *Session) PendingAsk() (PendingAsk, bool) {
	if s.State != StateAwaiting || s.pending == nil {
		return PendingAsk{}, false
	}
	return *s.pending, true
}

// ResumeWith clears the pending ask and returns the session to StateRunning so
// the loop can continue. It is legal only while awaiting; it returns
// ErrNoPendingAsk otherwise. The resolved ask is returned for reference. The
// caller (the loop) owns the permission decision and acts on it (allow →
// execute, deny → feed the reason back to the model); the aggregate only
// reconciles its own lifecycle, so it deliberately takes no governance type and
// keeps session a clean domain leaf.
func (s *Session) ResumeWith() (PendingAsk, error) {
	if s.State != StateAwaiting || s.pending == nil {
		return PendingAsk{}, ErrNoPendingAsk
	}
	ask := *s.pending
	s.pending = nil
	s.State = StateRunning
	return ask, nil
}

// Complete marks a successful terminal end of the run. It is legal from any
// non-terminal state except StateAuthorizing and records StopEndTurn unless a
// stop reason is already set.
func (s *Session) Complete() error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("Complete"); err != nil {
		return err
	}
	if s.State.IsTerminal() || s.State == StateAuthorizing {
		return fmt.Errorf("%w: Complete from %q", ErrIllegalTransition, s.State)
	}
	if s.stop == StopNone {
		s.stop = StopEndTurn
	}
	s.State = StateCompleted
	s.pending = nil
	s.clearRetryIntent()
	return nil
}

// Stop marks a successful terminal end carrying an explicit stop reason (e.g. a
// limit was reached). It is legal from any non-terminal state except
// StateAuthorizing.
func (s *Session) Stop(reason StopReason) error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("Stop"); err != nil {
		return err
	}
	if s.State.IsTerminal() || s.State == StateAuthorizing {
		return fmt.Errorf("%w: Stop from %q", ErrIllegalTransition, s.State)
	}
	s.stop = reason
	s.State = StateCompleted
	s.pending = nil
	s.clearRetryIntent()
	return nil
}

// Cancel transitions the session to StateCancelled. It is legal from any
// non-terminal state except StateAuthorizing.
func (s *Session) Cancel() error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("Cancel"); err != nil {
		return err
	}
	if s.State.IsTerminal() || s.State == StateAuthorizing {
		return fmt.Errorf("%w: Cancel from %q", ErrIllegalTransition, s.State)
	}
	s.stop = StopCancelled
	s.State = StateCancelled
	s.pending = nil
	s.clearRetryIntent()
	return nil
}

// RecordFailureMetadata stamps typed provider retry facts on a failed session.
func (s *Session) RecordFailureMetadata(metadata RetryMetadata) error {
	if s.State != StateFailed {
		return fmt.Errorf("%w: RecordFailureMetadata from %q", ErrIllegalTransition, s.State)
	}
	if !metadata.Valid() {
		return fmt.Errorf("%w: invalid failure metadata disposition=%d progress=%d", ErrIllegalTransition, metadata.Disposition, metadata.Progress)
	}
	s.failureMetadata = metadata
	return nil
}

// FailureMetadata returns typed terminal facts only while the session is failed.
func (s *Session) FailureMetadata() RetryMetadata {
	if s.State != StateFailed {
		return RetryMetadata{}
	}
	return s.failureMetadata
}

// PrepareFailedStepRetry consumes an eligible failed attempt into a durable,
// prompt-free retry intent. It repairs any interrupted tool-call tail and resets
// per-run counters while preserving the failed attempt's typed retry facts.
// Calling it again on an already-prepared idle session is idempotent.
func (s *Session) PrepareFailedStepRetry() error {
	if s.retryPending && s.State == StateIdle {
		return nil
	}
	metadata := s.failureMetadata
	if s.State != StateFailed || !metadata.Valid() ||
		metadata.Disposition != RetryDispositionRetryable ||
		(metadata.Progress != StreamProgressPrecommit && metadata.Progress != StreamProgressVisible) {
		return fmt.Errorf("%w: PrepareFailedStepRetry from %q", ErrIllegalTransition, s.State)
	}
	s.closeOutInterruptedTurn(recoverCloseOutMessage)
	s.resetToIdle()
	s.retryPending = true
	s.retryMetadata = metadata
	return nil
}

// RestoreFailedStepRetryPending restores persisted retry intent. It is legal only
// on an idle or running aggregate.
func (s *Session) RestoreFailedStepRetryPending(metadata RetryMetadata) error {
	if (s.State != StateIdle && s.State != StateRunning) ||
		metadata.Disposition != RetryDispositionRetryable ||
		(metadata.Progress != StreamProgressPrecommit && metadata.Progress != StreamProgressVisible) {
		return fmt.Errorf("%w: RestoreFailedStepRetryPending from %q", ErrIllegalTransition, s.State)
	}
	s.retryPending = true
	s.retryMetadata = metadata
	return nil
}

// FailedStepRetryPending reports the durable retry intent and its original failure facts.
func (s *Session) FailedStepRetryPending() (RetryMetadata, bool) {
	return s.retryMetadata, s.retryPending
}

func (s *Session) clearRetryIntent() {
	s.retryPending = false
	s.retryMetadata = RetryMetadata{}
}

// RecordLastError stamps the terminal failure CAUSE (the loop's
// session.ResultPayload.Error) onto a StateFailed session so it persists on the
// snapshot independent of the parent's subagent.end emit (issue #332). It is legal
// ONLY when State==StateFailed (an idle or non-failed terminal returns
// ErrIllegalTransition). The cause is normalised to ONE line and
// clamped to maxSnapshotErrorRunes (mirroring the event-side
// subagentCausePayload normaliser so the snapshot and event fields agree). The
// field is cleared on any transition out of StateFailed (resetToIdle via
// Recover/Reopen/Interrupt), so a healed session never keeps a stale cause.
func (s *Session) RecordLastError(cause string) error {
	if s.State != StateFailed {
		return fmt.Errorf("%w: RecordLastError from %q", ErrIllegalTransition, s.State)
	}
	s.lastError = normaliseSnapshotError(cause)
	return nil
}

// LastError reports the terminal failure cause a StateFailed session was stamped
// with via RecordLastError. It is an unguarded read (returns "" for any state);
// callers that need the state guard should check State == StateFailed first. The
// cause is already normalised (one line, rune-clamped) at stamp time.
func (s *Session) LastError() string {
	return s.lastError
}

// BeginRun stamps the opaque, host-minted identity of the run this session is
// about to drive (ADR 0249).
//
// It is an UNGUARDED setter by design. Every other run-scoped mutator on this
// aggregate guards on State because it changes lifecycle meaning; this one
// changes only a label, and the run-entry seams that call it legitimately do so
// from several states (idle after a Reopen/Recover/Interrupt, or awaiting on the
// cross-process resume path). Guarding it would force each seam to re-derive a
// state check it has already done, for no invariant.
//
// An EMPTY id is accepted and clears the stamp: a host that mints no run id (an
// in-memory embedder, a test) is byte-identical to the behaviour before ADR 0249.
func (s *Session) BeginRun(runID string) {
	s.runID = runID
}

// RunID reports the run identity stamped by BeginRun, or "" if none was.
//
// The awaiting-resume path reads it to CONTINUE a run rather than start a new
// one; that reuse is the whole reason the value is persisted.
func (s *Session) RunID() string {
	return s.runID
}

// Fail transitions the session to StateFailed with StopError. It is legal from
// any non-terminal state except StateAuthorizing.
//
// A failed session recovers through Recover (failed→idle, history-repaired —
// issue #51), so a transient provider failure no longer bricks the session
// permanently. Historical note: the trigger that originally bricked sessions
// here was compaction emitting unpaired history (an orphaned tool result →
// provider HTTP 400 → Fail), which the compactors independently prevent by
// snapping the kept-tail boundary past leading tool results and self-validating
// via ValidateToolPairing.
func (s *Session) Fail() error {
	if err := s.rejectWhileWorkspaceEnrollmentPending("Fail"); err != nil {
		return err
	}
	if s.State.IsTerminal() || s.State == StateAuthorizing {
		return fmt.Errorf("%w: Fail from %q", ErrIllegalTransition, s.State)
	}
	s.stop = StopError
	s.State = StateFailed
	s.pending = nil
	// A new failure supersedes the prepared retry's facts. terminate stamps the
	// new attempt's typed metadata immediately after this transition.
	s.clearRetryIntent()
	s.failureMetadata = RetryMetadata{}
	s.lastError = ""
	return nil
}

// Reopen returns a successfully-completed session to StateIdle so it can accept a
// new user prompt and run another turn-loop, preserving the conversation history.
// It is the multi-turn / long-lived-teammate continuation seam: the agent loop
// always drives a session to a terminal state within a single Run, so a session
// that must receive another prompt later — a team teammate awaiting a message, an
// interactive multi-turn chat — needs an explicit, guarded re-open rather than a
// fresh session that would lose its history.
//
// It is legal ONLY from StateCompleted (a clean end-of-run). A FAILED run
// recovers through Recover and a CANCELLED run through Interrupt (both also
// repair the interrupted turn's history), NOT here; and a non-terminal session is already
// runnable — so every other state returns ErrIllegalTransition. Reopen clears the
// recorded stop reason
// and any pending ask, and RESETS the per-run Counters to zero so the configured
// Limits bound EACH prompt's work, matching their single-run meaning rather than
// silently becoming a session-lifetime cap. A caller that wants a lifetime budget
// (e.g. a team supervisor bounding total turns across a teammate's life) must
// enforce it separately. Conversation, Mode, Limits, EnvironmentRef, and Placement
// are preserved.
func (s *Session) Reopen() error {
	if s.State != StateCompleted {
		return fmt.Errorf("%w: Reopen from %q", ErrIllegalTransition, s.State)
	}
	s.resetToIdle()
	return nil
}

// resetToIdle returns the session to StateIdle and clears the per-run state:
// the recorded stop reason, any pending ask, and the Counters (so the
// configured Limits bound EACH prompt's work). It is the single shared reset
// body of the three terminal-recovery seams — Reopen, Interrupt, Recover — so
// the reset fields cannot drift across them; the state GUARDS (and the
// history-repair step) stay per-method, since each seam is legal only from its
// own terminal state.
func (s *Session) resetToIdle() {
	s.State = StateIdle
	s.stop = StopNone
	s.pending = nil
	s.failureMetadata = RetryMetadata{}
	s.lastError = ""
	s.clearRetryIntent()
	s.Counters = Counters{}
	// CRITICAL: canonical main usage is deliberately not cleared here. The
	// MaxRunTokens budget is evaluated against UsageFor(UsageKindMain), so spend
	// remains cumulative across reopen and restart.
}

// Synthetic close-out messages for closeOutInterruptedTurn. The text is DURABLE
// MODEL-FACING replayed history, so it must accurately attribute why the call
// never got a real result (the same accuracy discipline as the subagent
// childAutoDenyMessage: never claim a user action that didn't happen).
const (
	// interruptCloseOutMessage closes an orphan left by a user/driver CANCEL.
	interruptCloseOutMessage = "tool call interrupted by cancellation"
	// recoverCloseOutMessage closes an orphan left by a run FAILURE (e.g. a
	// provider stream error, or an internal record failure mid-dispatch) — no
	// user cancelled anything, and the replayed history must not say they did.
	recoverCloseOutMessage = "tool call aborted: the run failed before this call's result was recorded"
	// abandonCloseOutMessage closes an orphan left by a session found still
	// StateRunning with no process actually driving it (e.g. a crash-orphaned
	// snapshot, issue #475) — neither a cancellation nor a run failure was
	// observed, so the wording must not claim either.
	abandonCloseOutMessage = "tool call aborted: the process driving this run exited before this call's result was recorded"
)

// closeOutInterruptedTurn repairs an interrupted-mid-dispatch history so it is
// provider-valid: for the trailing assistant message's ToolCalls that have no
// following tool-result message, it appends one synthetic error tool result per
// orphaned ToolCall.ID, carrying the given message (model-facing: it states why
// the call never got a real result — cancellation vs failure differ, see the
// message constants above). Idempotent; a no-op when there is no trailing
// orphan. Mutates Conversation only via its append method. Shared by Interrupt
// (cancelled) and Recover (failed).
func (s *Session) closeOutInterruptedTurn(message string) {
	msgs := s.Conversation.Messages
	// Find the LAST assistant message.
	lastAssistant := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return
	}
	// Collect the set of CallIDs already answered by tool-role messages that
	// follow the trailing assistant message.
	answered := make(map[ToolCallID]struct{})
	for _, m := range msgs[lastAssistant+1:] {
		if m.Role == RoleTool && m.ToolResult != nil {
			answered[m.ToolResult.CallID] = struct{}{}
		}
	}
	// For each tool call on the trailing assistant message not yet answered,
	// append a synthetic error result in ToolCalls order. IsError here means the
	// call never ran to a result (interrupted/aborted), NOT a real tool failure.
	for _, call := range msgs[lastAssistant].ToolCalls {
		if _, ok := answered[call.ID]; ok {
			continue
		}
		s.Conversation.Append(NewToolMessage(NewToolError(call.ID, message)))
	}
}

// Interrupt recovers a CANCELLED session to StateIdle after repairing the
// interrupted turn's history. Legal ONLY from StateCancelled. Mirrors Reopen's
// reset (clears stop + pending, resets Counters) but is a SEPARATE method
// because its precondition and history-repair invariant differ.
//
// A turn cancelled mid-dispatch may leave the trailing assistant message with
// tool calls that never received a result; closeOutInterruptedTurn appends a
// synthetic error result per orphan so the replayed history stays
// provider-valid (no dangling tool_use / function_call) before the next prompt.
// Reopen stays completed-only; a FAILED session recovers through Recover.
func (s *Session) Interrupt() error {
	if s.State != StateCancelled {
		return fmt.Errorf("%w: Interrupt from %q", ErrIllegalTransition, s.State)
	}
	s.closeOutInterruptedTurn(interruptCloseOutMessage)
	s.resetToIdle()
	return nil
}

// Recover returns a FAILED session to StateIdle after repairing the failed
// turn's history, so a transient provider failure (an upstream 5xx that
// exhausted the resilience layer's retries) degrades to "retryable" instead of
// permanently bricking the session (issue #51). Legal ONLY from StateFailed.
//
// A turn that failed mid-stream / mid-dispatch may leave the trailing assistant
// message with tool calls that never received a result; closeOutInterruptedTurn
// appends a synthetic error result per orphan (with the failure-accurate
// recoverCloseOutMessage — never the cancellation wording, which would falsely
// attribute a user action) so the replayed history stays provider-valid (no
// dangling tool_use / function_call). Recovery makes RETRY POSSIBLE, not
// guaranteed — if the underlying cause persists (a permanent auth/config
// failure, or a history poisoned in a way the pairing repair cannot fix), the
// retried run fails again, which is acceptable: the user keeps the conversation
// context and can retry or clear.
//
// It is the sibling of Interrupt (cancelled→idle) and Reopen (completed→idle):
// same reset (resetToIdle), separate method because its precondition differs.
// Reopen stays completed-only; Interrupt stays cancelled-only.
func (s *Session) Recover() error {
	if s.State != StateFailed {
		return fmt.Errorf("%w: Recover from %q", ErrIllegalTransition, s.State)
	}
	s.closeOutInterruptedTurn(recoverCloseOutMessage)
	s.resetToIdle()
	return nil
}

// Abandon returns a session STUCK in StateRunning to StateIdle after repairing
// the abandoned turn's history — the case where the process that was driving
// the run exited (crashed, was killed, lost its host) without ever reaching a
// terminal state, so the last persisted snapshot reads "running" forever
// (issue #475). Legal ONLY from StateRunning: a session actually paused on an
// ask belongs to Awaiting's own resume path, not this seam (abandoning it
// would discard a still-resolvable PendingAsk via resetToIdle), and every
// other state is already idle or has its own recovery seam.
//
// A turn abandoned mid-dispatch may leave the trailing assistant message with
// tool calls that never received a result; closeOutInterruptedTurn appends a
// synthetic error result per orphan (with the abandonment-accurate
// abandonCloseOutMessage — never the cancellation or failure wording, neither
// of which was observed here) so the replayed history stays provider-valid (no
// dangling tool_use / function_call) before the next prompt.
//
// It is the sibling of Interrupt (cancelled→idle) and Recover (failed→idle):
// same reset (resetToIdle), separate method because its precondition and
// message differ. Callers are expected to have already established that no
// process is actually still driving the session (e.g. an age-horizon-gated
// staleness sweep) — Abandon itself performs no liveness check.
func (s *Session) Abandon() error {
	if s.State != StateRunning {
		return fmt.Errorf("%w: Abandon from %q", ErrIllegalTransition, s.State)
	}
	pending, metadata := s.retryPending, s.retryMetadata
	s.closeOutInterruptedTurn(abandonCloseOutMessage)
	s.resetToIdle()
	// Exact retry is the one Abandon carve-out: a crash after durable preparation
	// must return to idle-but-pending, not become an ordinary promptable session.
	if pending {
		s.retryPending = true
		s.retryMetadata = metadata
	}
	return nil
}

// Rehome replaces the exact environment identity of an idle delegated session
// after a fresh child environment has been minted. The live environment used by
// the caller must carry the same ref.
func (s *Session) Rehome(ref EnvironmentRef) error {
	if s.State != StateIdle {
		return fmt.Errorf("%w: Rehome from %q", ErrIllegalTransition, s.State)
	}
	if !ref.Valid() {
		return fmt.Errorf("session: Rehome requires a valid environment ref")
	}
	s.EnvironmentRef = ref
	return nil
}

// SetMode changes the session's permission posture. It is the intention-revealing
// seam an out-of-band control surface (e.g. ACP session/set_mode) uses to switch
// between default/plan/acceptEdits, so the change flows through the aggregate
// rather than poking the public Mode field.
//
// It is legal ONLY while the session is NOT actively progressing a turn — i.e.
// from StateIdle or any terminal state, but NOT from StateRunning or
// StateAwaiting. Changing the permission posture mid-turn would race the loop's
// own permission evaluation (plan mode hard-denies mutations; acceptEdits
// auto-allows them) against tool dispatch already in flight, so a mid-run switch
// is rejected with ErrIllegalTransition. A control surface that receives a
// set_mode while running must defer it (apply on the next prompt). Setting the
// mode it already has is a no-op success.
func (s *Session) SetMode(mode PermissionMode) error {
	if s.State == StateRunning || s.State == StateAwaiting || s.State == StateAuthorizing {
		return fmt.Errorf("%w: SetMode from %q", ErrIllegalTransition, s.State)
	}
	s.Mode = mode
	return nil
}

// maxTitleRunes bounds Session.Title (and ClampTitle) — a human-readable label,
// not a paragraph. 120 runes is generous for a one-line session summary.
const maxTitleRunes = 120

// SetTitle seeds Session.Title from a user prompt, ONCE: a non-empty text on a
// session whose Title is still "" sets it (clamped via ClampTitle); any later
// call is a no-op (subsequent prompts do NOT overwrite the first). The caller —
// the loop's recordPrompt (the GENUINE prompt site) — MUST ensure it passes only
// a genuine user prompt: the aggregate enforces set-once, the caller enforces
// genuineness. An empty/whitespace-only text leaves Title=="" (a multimodal-only
// prompt with no text, or an empty prompt, does not seed). It is NOT a state
// transition (legal from any state) — Title is an inert stored label.
func (s *Session) SetTitle(text string) {
	if s.Title == "" && strings.TrimSpace(text) != "" {
		s.Title = ClampTitle(text)
		s.TitleProvenance = TitleProvenanceFirstPrompt
		s.bumpTitleRevision()
	}
}

// RenameTitle replaces the title at an operator's explicit request. Unlike
// SetTitle it is intentionally not set-once. Blank titles are rejected and the
// shared title helper applies the same whitespace and length rules as prompt titles.
func (s *Session) RenameTitle(text string) error {
	title := ClampTitle(text)
	if title == "" {
		return fmt.Errorf("session: title must not be blank")
	}
	if s.Title == title && s.TitleProvenance == TitleProvenanceOperator {
		return nil
	}
	s.Title = title
	s.TitleProvenance = TitleProvenanceOperator
	s.bumpTitleRevision()
	return nil
}

// StopReason reports why the run should stop. It is a DERIVED predicate: it
// returns the recorded terminal reason if one is set, otherwise it computes a
// limit-tripped reason from the configured Limits and current Counters. It does
// not mutate state. The loop consults it as a pre-turn guard.
//
// Because it conflates the recorded reason with a limit derivation, it is NOT a
// faithful witness of terminal state. Persistence and any caller that needs the
// exact reason explicitly recorded via Stop/Cancel/Fail/Complete must use
// RecordedStopReason instead.
func (s *Session) StopReason() (StopReason, bool) {
	if r, ok := s.RecordedStopReason(); ok {
		return r, true
	}
	if r, ok := s.LimitTripped(); ok {
		return r, true
	}
	return StopNone, false
}

// RecordedStopReason returns the terminal stop reason explicitly recorded on the
// session (via Stop/Cancel/Fail/Complete) and true when one is set. It performs
// NO limit derivation, so it is a faithful witness of the recorded terminal
// state — use it for persistence and round-tripping rather than StopReason.
func (s *Session) RecordedStopReason() (StopReason, bool) {
	if s.stop == StopNone {
		return StopNone, false
	}
	return s.stop, true
}

// LimitTripped reports the stop reason implied by the configured Limits and
// current Counters, and true when a limit is reached. It is a pure predicate
// that ignores any recorded terminal reason; it never mutates state.
func (s *Session) LimitTripped() (StopReason, bool) {
	if s.Limits.MaxTurns > 0 && s.Counters.Turns >= s.Limits.MaxTurns {
		return StopMaxTurns, true
	}
	if s.Limits.MaxToolCalls > 0 && s.Counters.ToolCalls >= s.Limits.MaxToolCalls {
		return StopMaxToolCalls, true
	}
	if s.Limits.MaxConsecutiveFailures > 0 &&
		s.Counters.ConsecutiveFailures >= s.Limits.MaxConsecutiveFailures {
		return StopMaxConsecutiveFailures, true
	}
	return StopNone, false
}
