package session

// EventType is the kind of a domain Event. This is the single event taxonomy
// shared by the agent loop and the API; the API serializes it to proto and it is
// never a provider-specific type.
type EventType string

const (
	// EvSessionInit is emitted once when a run starts.
	EvSessionInit EventType = "session.init"
	// EvTurnStart is emitted at the beginning of each turn. Its Turn field is the
	// 0-based turn index (turnIdx = Counters.Turns - 1); turn.end mirrors it. The
	// 0-based wire contract is load-bearing — clients that surface a human-facing
	// "turn N" must add 1 themselves; do not shift the wire value.
	EvTurnStart EventType = "turn.start"
	// EvTurnEnd closes a turn's model exchange, carrying the typed TurnEndPayload
	// (this turn's Usage + elapsed model-call time). Emitted once per successful
	// turn, before the assistant message is recorded; not emitted on error/cancel.
	EvTurnEnd EventType = "turn.end"
	// EvMessageDelta carries streamed assistant text.
	EvMessageDelta EventType = "message.delta"
	// EvReasoningDelta carries streamed, human-readable reasoning summary text.
	// It is display-only and distinct from the opaque Message.Reasoning replay
	// item: it is emitted in addition to (never in place of) the reasoning
	// accumulation that is replayed back to the provider as encrypted content.
	EvReasoningDelta EventType = "reasoning.delta"
	// EvToolCall is emitted when a tool is about to run.
	EvToolCall EventType = "tool.call"
	// EvToolResult carries the result of a tool execution.
	EvToolResult EventType = "tool.result"
	// EvToolProgress is transient progress for a long-running tool: it carries a
	// human-readable Text line (e.g. "scanned 128/512 files") emitted at a
	// tool's phase boundaries via the observability seam so a slow call does not
	// look dead. It is ADVISORY — NOT persisted to a SessionStore and NOT recorded
	// to the model's conversation history; clients render it as a transient status
	// line, cleared on the next tool.result (or turn end).
	EvToolProgress EventType = "tool.progress"
	// EvPermissionAsk is emitted when the loop pauses for client approval.
	EvPermissionAsk EventType = "permission.ask"
	// EvPermissionRetract is emitted when a previously surfaced permission.ask is
	// WITHDRAWN by the harness — the owning subagent child was cancelled by the
	// client (Run.CancelChild) while parked on the ask, so there is nothing left to
	// approve. The payload rides the existing Ask field carrying ONLY the AskID
	// (server-authored; no tool/args/reason — there is no spoofing surface). Clients
	// dismiss a pending approval modal iff its AskID matches; an unknown/stale id is
	// ignored (idempotent). It maps to the proto event-type string verbatim (the
	// wire type field is a string passthrough; PermissionAsk's fields are optional,
	// so an ask_id-only payload is wire-legal with no proto change).
	EvPermissionRetract EventType = "permission.retract"
	// EvApproval is emitted when a permission ask is RESOLVED by a verdict — the
	// chronological approval record (which tool, which verdict, when) that pairs
	// with the EvPermissionAsk that preceded it. It is emitted at BOTH verdict
	// sites: the live-loop path (authorize, after the verdict resolves) and the
	// resume-from-awaiting path (resolvePendingCall, at entry). It is NOT relayed
	// to clients in 3a (the durable EventLog at the server relay consumes it, not
	// the Converse/SSE proto wire); it carries the ApprovalPayload. Its wire
	// string ("approval") is a passthrough, like EvNoProgress/StopBudget — no
	// proto enum, no task generate. See ApprovalPayload for the no-leak contract.
	EvApproval EventType = "approval"
	// EvHook is emitted when a hook fires (e.g. PreToolUse blocked).
	EvHook EventType = "hook"
	// EvCompaction is emitted when a compaction boundary is crossed.
	EvCompaction EventType = "compaction"
	// EvCompactionArchive is emitted AFTER a successful compaction (right after the
	// EvCompaction notice) carrying the pre-compaction conversation that
	// ReplaceHistory just replaced — the durable, NON-DESTRUCTIVE archive of the
	// history compaction would otherwise drop forever (the next session snapshot
	// holds only the compacted tail). It pairs with EvCompaction the way EvApproval
	// pairs with EvPermissionAsk: the notice is the live event, this is the durable
	// record. It carries the CompactionArchivePayload; like EvApproval it is
	// consumed by the durable EventLog at the server relay and is NOT relayed to the
	// client wire (no proto enum; the wire `type` is a string passthrough, no task
	// generate). The loop only EMITS it (e.emit) — the relay persists it; the loop
	// never imports port.EventLog.
	//
	// NO-LEAK CONTRACT (gauntlet #7): the archived messages are the PARENT'S OWN
	// conversation history — the exact slice already present in the pre-compaction
	// session snapshot. A child's (Subagent/team/parallel) content NEVER enters the
	// parent conversation (only a child's summarised ToolResult does), so this event
	// can carry no child content and there is no leak surface here, unlike the
	// redacted delegation events.
	EvCompactionArchive EventType = "compaction.archive"
	// EvNoProgress is emitted when a completed turn produced NEITHER a tool call NOR
	// meaningful assistant text (a reasoning-only / empty turn) and the loop is
	// either injecting a bounded continuation nudge or, on the final attempt, giving
	// up. Text carries a short human-readable reason and Turn is the no-progress
	// turn index. It is ADVISORY — NOT recorded to the model's conversation history
	// and NOT a diagnostics line (the event taxonomy owns the no-progress signal,
	// mirroring how cancellation/tool-error are event-covered). Clients render it as
	// a transient status line, like EvCompaction. It maps to the proto event-type
	// string verbatim (no proto enum; the wire type field is a string passthrough).
	EvNoProgress EventType = "no_progress"
	// EvProviderRoute is emitted once per turn when the serving provider reports
	// which DOWNSTREAM inference provider actually routed the request (issue #480).
	// Text carries an opaque human-readable display label (e.g. "Google" or
	// "Amazon Bedrock"), not the lowercase routing slug used in configuration and
	// not a round-trippable identifier; Turn is the turn index. Today only the
	// openrouter registry entry produces it (via the X-OpenRouter-Metadata opt-in);
	// mecatl's "provider" stays the wire adapter — this is the downstream provider
	// OpenRouter selected. It is ADVISORY + metadata-only: NOT recorded to the
	// model's conversation history, NOT a diagnostics line (the event taxonomy owns
	// it, like EvNoProgress), and gauntlet-#7-clean (a bounded routing label is
	// provider metadata, never child content). It is CLIENT-VISIBLE — clients render
	// it as a transient status line and may also retain it for the current turn. It
	// degrades to ABSENT on a cache hit (OpenRouter strips openrouter_metadata from
	// cached responses): the loop emits nothing rather than fabricating a value. It
	// maps to the proto event-type string verbatim (no proto enum; the wire type
	// field is a string passthrough, like EvNoProgress — no task generate).
	EvProviderRoute EventType = "provider.route"
	// EvRecoverNotice is emitted at the run-entry funnel (in the Service layer,
	// NOT the agent loop) when a session that failed on a PERMANENT provider error is
	// recovered for re-entry. Text carries a short human-readable advisory (e.g.
	// "this session's last turn failed on a permanent provider error; retrying replays
	// the same request and will fail again. Start a new session, or change the
	// request."). It is CLIENT-VISIBLE (like EvNoProgress) — rendered as a
	// transient status/warning so the user sees it at run start (before the run's
	// first event overwrites it). It does NOT block the run — Recover stays honest
	// (retry POSSIBLE, not guaranteed). It is emitted ONCE per recovery (the
	// permanence flag is cleared when Recover resets the session to idle, so a
	// subsequent prompt on the same session emits no repeat notice). It maps to the
	// proto event-type string verbatim (no proto enum; the wire type field is a
	// string passthrough, like EvNoProgress).
	EvRecoverNotice EventType = "recover_notice"
	// EvResult is the terminal event: success / limit / error / cancelled.
	EvResult EventType = "result"
	// EvUserPrompt is emitted when a USER-ROLE message is recorded into the
	// conversation — the genuine client prompt (recordPrompt) AND the harness-authored
	// synthetic continuations the loop records as user messages (the no-progress nudge,
	// the background-pending nudge, the background-completion notice). It carries the
	// UserPromptPayload (the recorded user-message Content — Text + Parts), enough for an
	// event-sourced consumer to reconstruct the user Message faithfully.
	//
	// LOG-ONLY (the EvApproval / EvCompactionArchive precedent): the durable EventLog at
	// the server relay consumes it, and the relay SKIPS it on the live client wire — the
	// driving client already authored/holds the prompt, so re-sending it is redundant.
	// It maps to the proto event-type string verbatim (no proto enum; the wire `type`
	// field is a string passthrough, no task generate). The loop only EMITS it; the relay
	// persists it — the loop never imports port.EventLog.
	//
	// WHY IT EXISTS: without it the durable log could not show WHAT THE USER ASKED (the
	// relay never re-emits the user prompt it received), and an event-sourced fold of the
	// log (engine/adapter/eventsource) could not reconstruct user-role turns — closing
	// that gap is ADR 0038 / the ADR 0027 row-11 follow-up.
	//
	// NO-LEAK CONTRACT (gauntlet #7): a CHILD's user prompt (a Subagent goal, a team
	// member task, a structured-output correction) is recorded into the CHILD session and
	// emitted on the CHILD run's event stream, which is drained INSIDE the delegation tool
	// and never forwarded to the parent run's log (exactly like every other child event).
	// So this event only ever carries the top-level run's own user input — no child
	// content crosses to the parent, the same posture as EvCompactionArchive.
	EvUserPrompt EventType = "user_prompt"

	// EvSubagentStart is emitted when a Subagent tool run begins. It is a
	// REDACTED observability projection of a child loop — never the child's
	// content. It carries only the parent call id, the child session id, and a
	// short goal label so a client can attribute and title the subagent card.
	EvSubagentStart EventType = "subagent.start"
	// EvSubagentTool is emitted each time a Subagent tool's child tool call
	// resolves. It is a REDACTED observability projection: it forwards ONLY the
	// child tool's NAME and error bool plus a running count — never the child's
	// tool args or result content, and never the child's message text. This keeps
	// the context-isolation guarantee (gauntlet #7) intact: nothing the child
	// produces enters the parent's conversation.
	EvSubagentTool EventType = "subagent.tool"
	// EvSubagentEnd is emitted when a Subagent tool run terminates. It is a
	// REDACTED observability projection carrying only aggregate metadata — the
	// child's tool count, token usage, stop reason, and wall-clock duration —
	// never any child content. The child's terminal summary still folds back into
	// the parent conversation exclusively via the Subagent tool's ToolResult.
	EvSubagentEnd EventType = "subagent.end"

	// EvTeamStart is emitted when a Team tool run begins. It is a BOUNDED
	// observability projection of an in-process team — it carries the parent call
	// id, the team id, and the roster the model formed (member names/roles, never
	// member content). See TeamPayload for the redaction contract.
	EvTeamStart EventType = "team.start"
	// EvTeamMember is emitted for each forwarded member-session event during a Team
	// run. Unlike the metadata-only subagent.tool projection, it is deliberately
	// FULLER — a team is meant to be watched — so it carries the member's message
	// text and BOUNDED tool-call/result previews, tagged by member name. It ALWAYS
	// sets Member (the member whose activity it projects) and InnerKind (that
	// member's underlying session event type). It is STILL bounded and redacted:
	// every preview is capped, and a member's permission.ask is DROPPED entirely
	// (never forwarded). See TeamPayload.
	EvTeamMember EventType = "team.member"
	// EvTeamTasks is emitted when the team's SHARED TASK LIST changes during a Team
	// run (and as a terminal snapshot on EvTeamEnd's payload). It is a team-WIDE
	// projection — NOT per-member — so it carries no Member; only TeamPayload.Tasks
	// (the id/state/assignee/deps snapshot in creation order). It is the discriminant
	// the client routes to the ctrl+a agents task sub-view. Snapshots are emitted
	// only on change (de-duped) to bound wire volume.
	EvTeamTasks EventType = "team.tasks"
	// EvTeamFindings is emitted when the team's SHARED FINDINGS LEDGER changes during
	// a Team run (and as a terminal snapshot on EvTeamEnd's payload). Like EvTeamTasks
	// it is a team-WIDE projection — NOT per-member — so it carries no Member; only
	// TeamPayload.Findings (each member-authored finding's recording member + a BOUNDED
	// body preview, in append order). The findings ledger is the PRIMARY channel the
	// lead's synthesis consolidates; surfacing it on the stream lets a watching client
	// see findings accrue. Snapshots are emitted only on change (de-duped) to bound
	// wire volume, exactly like EvTeamTasks.
	EvTeamFindings EventType = "team.findings"
	// EvTeamEnd is emitted when a Team run terminates. It is a BOUNDED projection
	// carrying aggregate metadata — the number of rounds, the stop reason, the team's
	// cumulative usage — plus the terminal Tasks/Findings snapshots and a per-member
	// terminal disposition snapshot (TeamPayload.Dispositions). The disposition carries
	// ONLY closed-enum supervisor verdicts (done/stopped × error/cancelled/budget),
	// NOT member-authored content, so the redaction discipline is untouched. The team's
	// joined summary folds back into the parent conversation exclusively via the Team
	// tool's ToolResult.
	EvTeamEnd EventType = "team.end"

	// EvParallelStart is emitted when a Parallel (fork-join fan-out) tool run begins.
	// It is a REDACTED, RUN-LEVEL projection: it carries the parent call id, the join
	// strategy, and the branch count so a client can frame the group from the first
	// event — never any branch content. See ParallelPayload for the redaction contract.
	EvParallelStart EventType = "parallel.start"
	// EvParallelBranch is emitted for each per-branch lifecycle transition of a Parallel
	// run, discriminated by ParallelPayload.Kind (branch_start / branch_tool / branch_end).
	// Like EvSubagentTool it is METADATA ONLY: a branch_tool carries only the child tool's
	// NAME + error bool + running count (forwarded via the SAME drainChildObserved
	// chokepoint Subagent uses), and a branch_end carries only the branch's stop / usage /
	// duration / failed flag / fork-root path — never branch args, result bodies, or
	// message text. This keeps gauntlet #7 intact.
	EvParallelBranch EventType = "parallel.branch"
	// EvParallelEnd is emitted when a Parallel run terminates. It is a REDACTED, RUN-LEVEL
	// projection carrying the join strategy, the WINNER branch index (-1 for join=all /
	// none-succeeded), the PRESERVED winner fork root, the run-total usage, the branch
	// count, and the run-level stop — never any branch content. The branches' summaries
	// fold back into the parent conversation exclusively via the Parallel tool's
	// ToolResult.
	EvParallelEnd EventType = "parallel.end"

	// EvScheduleFired is emitted when a schedule fires — a Claim→run→RecordFire
	// cycle started for a named schedule. It is emitted from COMPOSITION (the
	// scheduler) at fire time, NOT the agent loop (engine/agent never imports
	// port.ScheduleStore). For v1 it is delivered to the fire session's durable
	// EventLog ONLY (pull-only via GetFire/ListFires); a live broadcast stream is
	// a future phase. It carries the SchedulePayload; kind/stop/err are STRING
	// passthroughs (the EvNoProgress/StopBudget discipline — no proto enum).
	// Maps to the proto event-type string verbatim.
	EvScheduleFired EventType = "schedule.fired"
	// EvScheduleSkipped is emitted when a schedule's fire was SKIPPED — the
	// singleton overlap check found a prior fire still running, or the misfire
	// policy was MisfireSkip for a missed slot. Emitted from composition, not the
	// loop; carries SchedulePayload (kind="skipped"). A skipped fire has no
	// session id, so it is dropped from the durable log (session-keyed) and
	// surfaces only via the operator diagnostic for v1.
	EvScheduleSkipped EventType = "schedule.skipped"
	// EvScheduleFailed is emitted when a schedule's fire FAILED — the fire's run
	// ended with StopError, or the fire could not be claimed/driven at all.
	// Emitted from composition, not the loop; for v1 delivered to the fire
	// session's durable EventLog ONLY (pull-only via GetFire/ListFires); carries
	// SchedulePayload (kind="failed", stop/err populated).
	EvScheduleFailed EventType = "schedule.failed"
)

// ParallelEventKind discriminates which lifecycle transition an EvParallelBranch event
// projects (mirroring the way the client switches on a kind, like client.SubagentKind).
// The run-level EvParallelStart / EvParallelEnd events carry their own implicit kind via
// their event type; ParallelEventKind is set only on EvParallelBranch.
type ParallelEventKind string

const (
	// ParallelBranchStart marks a branch beginning its child run. Sets BranchIndex,
	// BranchLabel, Goal.
	ParallelBranchStart ParallelEventKind = "branch_start"
	// ParallelBranchTool marks a branch's child tool call resolving. Sets BranchIndex,
	// ToolName, IsError, ToolCount — metadata only (the drainChildObserved projection).
	ParallelBranchTool ParallelEventKind = "branch_tool"
	// ParallelBranchEnd marks a branch's child run terminating. Sets BranchIndex,
	// ToolCount, Stop, Usage, DurationMs, Failed, Workspace.
	ParallelBranchEnd ParallelEventKind = "branch_end"
)

// HookDecision is the outcome a hook fire produced, so a client can colour and
// rank a hook notice without parsing its prose. It is provider-neutral and maps
// 1:1 to a proto enum.
type HookDecision string

const (
	// HookInfo is a benign, informational hook notice (the default): the hook
	// fired and allowed the action, or reported something non-blocking.
	HookInfo HookDecision = "info"
	// HookBlocked means the hook vetoed the action (a PreToolUse/UserPromptSubmit
	// block, or a fail-safe hook error). These can abort a run and must read as
	// the most severe hook notice.
	HookBlocked HookDecision = "blocked"
	// HookModified means the hook rewrote the action's payload (prompt rewrite,
	// tool-arg or tool-result mutation) without blocking it.
	HookModified HookDecision = "modified"
	// HookAdvisory means the hook flagged the content as a finding but did NOT
	// alter the call/result (advisory-mode guardrail). It is client-visible
	// (rendered as a warning notice on the tool card) and model-invisible: the
	// tool result is byte-unchanged. Distinct from HookInfo (a generic benign
	// notice) and HookBlocked/HookModified (which change the outcome).
	HookAdvisory HookDecision = "advisory"
)

// HookPayload is the structured detail carried by an EvHook Event, in addition
// to the human-readable Event.Text. It lets a client render a hook notice
// distinctly from a compaction notice — labelling the lifecycle Phase and
// colouring the Decision (e.g. a blocked hook in an error colour) — instead of
// string-parsing the free text. All fields are optional; a zero value renders as
// a generic informational hook.
type HookPayload struct {
	// Phase is the lifecycle point the hook fired at (e.g. "PreToolUse"); empty
	// when not applicable.
	Phase string
	// Tool is the tool the hook relates to for the per-tool phases; empty
	// otherwise.
	Tool string
	// Decision is the outcome (info / blocked / modified).
	Decision HookDecision
	// CallID is the id of the tool call this hook fired against, for the per-tool
	// phases (PreToolUse / PostToolUse); empty for non-tool phases (e.g. Stop,
	// SessionStart) and for tool phases where no call is in scope. It lets a client
	// address the hook notice to the originating tool card (e.g. mark that exact
	// tool_call as failed) instead of falling back to a free-standing note.
	CallID ToolCallID
}

// ApprovalPayload is the structured detail carried by an EvApproval Event: the
// resolution of a permission ask. It is the verdict half of the chronological
// approval record (the EvPermissionAsk it follows is the request half).
//
// NO-LEAK CONTRACT (gauntlet #7): it carries the tool NAME, the verdict string,
// the askID, the gated tool-call id, and the allow-always flag — and NOTHING ELSE.
// It NEVER carries the raw tool args (those can quote secrets) nor the deny-reason
// body (which can quote a sensitive command preview). A consumer that needs to
// correlate a verdict back to a tool call uses Call (the opaque tool-call id, also
// implicitly inside AskID) against the conversation history, never an arg payload
// on this event.
type ApprovalPayload struct {
	// AskID is the id of the resolved permission ask (the same id carried on the
	// EvPermissionAsk that preceded this verdict and on the wire ResumeApproval).
	AskID string
	// Verdict is the resolution as a STRING passthrough: VerdictStringAllowOnce,
	// VerdictStringAllowAlways, or VerdictStringDeny (the EvNoProgress/StopBudget
	// precedent — no proto enum). It is the human/policy decision, never tool
	// content.
	Verdict string
	// Tool is the NAME of the tool the ask gated (e.g. "Bash"). It is the tool
	// name ALONE — never the call's args.
	Tool string
	// Call is the id of the gated ToolCall. It is an OPAQUE identifier, NOT secret
	// content — it is already implicitly encoded inside AskID (see agent.newAskID) —
	// so surfacing it directly opens no new leak surface. It is the durable,
	// grammar-free correlation handle a 3b permstore-replay consumer uses to find the
	// gated ToolCall in the loaded conversation and re-derive its rule from the real
	// args (which stay in the session history, never on this event).
	Call ToolCallID
	// AllowAlways mirrors (Verdict == VerdictStringAllowAlways): the verdict ASKED
	// the harness to learn a per-session allow rule. It is deliberately NOT named
	// "Learned": Policy.Learn no-ops on an unlearnable call (compound/substituted
	// Bash with no targetable pattern), so an allow-always verdict can set this
	// true even when NO rule was actually recorded. It honestly reflects the
	// VERDICT, not the policy outcome. A 3b permstore-replay consumer filtering on
	// this must re-derive the real rule from the conversation (the metadata-only
	// event never carries enough to reconstruct a pattern), so AllowAlways is a
	// filter hint, never a durable rule record.
	AllowAlways bool
}

// The EvApproval verdict-string passthrough labels. They are the ONE source of
// truth for the wire-neutral verdict strings (the EvNoProgress/StopBudget
// precedent: a string passthrough, no proto enum), shared by VerdictString, the
// emit sites, and any 3b consumer that filters on the verdict — so nobody
// re-spells the literals.
const (
	// VerdictStringDeny is the EvApproval label for a denied call.
	VerdictStringDeny = "deny"
	// VerdictStringAllowOnce is the EvApproval label for an allow-once verdict.
	VerdictStringAllowOnce = "allow_once"
	// VerdictStringAllowAlways is the EvApproval label for an allow-always verdict.
	VerdictStringAllowAlways = "allow_always"
)

// VerdictString maps an ApprovalVerdict to its EvApproval string-passthrough
// label (VerdictStringDeny / VerdictStringAllowOnce / VerdictStringAllowAlways).
// It is the SINGLE place the domain enum is projected onto the wire-neutral
// verdict string, so the EvApproval emit sites cannot drift.
func VerdictString(v ApprovalVerdict) string {
	switch v {
	case VerdictAllowOnce:
		return VerdictStringAllowOnce
	case VerdictAllowAlways:
		return VerdictStringAllowAlways
	default:
		return VerdictStringDeny
	}
}

// CompactionArchivePayload is the structured detail carried by an
// EvCompactionArchive Event: the pre-compaction conversation that ReplaceHistory
// replaced. It is the durable, non-destructive archive of the history a
// compaction would otherwise drop — a later consumer (the Phase 3 reconstruct
// gate, issue #28 session-scoped detach) replays the EventLog and recovers the
// pre-compaction turns that the session snapshot no longer holds.
//
// CAPTURE ORDERING (load-bearing): the loop captures the original Messages slice
// BEFORE ReplaceHistory mutates the conversation, and emits this event only AFTER
// a SUCCESSFUL ReplaceHistory. Messages are immutable per-element, so holding the
// slice reference across the replace is safe — Replaced is the genuine
// pre-compaction history, not the post-compaction tail.
//
// NO-LEAK CONTRACT (gauntlet #7): Replaced is the PARENT'S OWN conversation; no
// child content ever enters it (only a child's summarised ToolResult does), so
// archiving it verbatim opens no leak surface. See EvCompactionArchive.
//
// LOG-GROWTH COST (a reasoned decision, not an accident): Replaced is the FULL
// pre-compaction conversation, NOT just the span the compaction dropped. Across a
// long session with N compactions this RE-LOGS the retained tail each time, so the
// event log grows super-linearly in the retained history. We accept that ON PURPOSE:
// the full slice is robust — it never depends on guessing how the Compactor split
// the kept tail from the dropped head (the Compactor owns that cut and is a
// swappable seam), so the archive is correct for ANY Compactor, including a future
// LLM-backed one whose "drop" is not a clean prefix. The DEFERRED optimization, if
// log size ever bites, is a delta archive (only the messages absent from the
// compacted result) computed by diffing pre/post histories in maybeCompact — a
// strictly additive change to what this field carries, behind the same event. Until
// a real deployment shows the growth matters, robustness wins over a premature delta.
type CompactionArchivePayload struct {
	// Replaced is the pre-compaction conversation — the exact Messages slice that
	// ReplaceHistory replaced, captured before the mutation. It is the full
	// pre-compaction history (a superset of the dropped span), so the dropped turns
	// are always recoverable from it regardless of how the Compactor split the cut.
	// See the LOG-GROWTH COST note above for why the full slice (not a delta) is kept.
	Replaced []Message
}

// UserPromptPayload is the structured detail carried by an EvUserPrompt Event: the
// user-role message that was just recorded into the conversation. It carries the
// flattened Text plus any non-text media Parts, mirroring the user Message a
// reconstruction must rebuild (session.NewUserMessageWithParts). It is the durable
// record of WHAT THE USER ASKED — both a genuine client prompt and the harness's own
// synthetic continuation messages (nudges/notices), so an event-sourced fold of the
// log reconstructs a COMPLETE conversation, not just genuine prompts.
//
// NO-LEAK CONTRACT (gauntlet #7): it carries only the TOP-LEVEL run's own user input.
// A child run's prompt is emitted on the child stream (drained inside the delegation
// tool), never on the parent's, so no child content crosses here — the same posture as
// CompactionArchivePayload (the parent's own history).
type UserPromptPayload struct {
	// Text is the flattened user-message text (the prompt body, or a harness-authored
	// continuation/notice).
	Text string
	// Parts carries any non-text media (image/audio) that rode alongside the text on
	// the user message; nil for a text-only prompt. It mirrors Message.Parts so the
	// reconstructed user Message is faithful.
	Parts []Content
}

// ResultPayload is the terminal payload carried by an EvResult Event.
type ResultPayload struct {
	// Stop is the reason the run ended.
	Stop StopReason
	// Text is the final assistant text, if any.
	Text string
	// Usage is the cumulative token accounting for the run.
	Usage Usage
	// Error carries the failure detail when Stop is StopError (empty otherwise).
	// It surfaces the error the loop would otherwise drop so callers (the demo,
	// API clients) can see why a run failed instead of an opaque "error".
	Error string
	// Permanent reports whether a StopError failure is a PERMANENT provider
	// rejection — replaying the identical request cannot succeed (e.g. a 4xx
	// other than 408/429: invalid_encrypted_content, a policy-blocked model).
	// It is meaningful ONLY when Stop==StopError; false for a transient failure
	// (retryable, e.g. a 5xx) and for any non-Error terminal. Fail-open: an
	// unclassifiable error is treated as NOT permanent.
	Permanent bool
}

// TurnEndPayload is the payload carried by an EvTurnEnd Event. It is a typed
// envelope (mirroring ResultPayload) so turn.end owns its own usage semantics
// and has room to grow (finish reason, model id, retries) without overloading
// the shared Event fields. This keeps Event.Usage with a single meaning — the
// cumulative run total on EvResult — rather than two semantics on one field.
type TurnEndPayload struct {
	// Usage is THIS turn's model-call usage (not the cumulative run total).
	Usage Usage
	// Estimated is true when Usage.InputTokens is a conversation-size ESTIMATE
	// rather than a provider-reported figure — the issue-#82 zero-usage fallback
	// fired because the turn produced no usage frame (a stalled/usage-less turn).
	// It is DISPLAY-ONLY: the estimate never feeds the cumulative run total or a
	// token budget (those stay on provider truth), only the context meter, which a
	// client may flag with a "~" hint. False for an ordinary provider-reported turn.
	Estimated bool
	// DurationMs is the elapsed milliseconds for the turn's model call; 0 when no
	// Clock is injected.
	DurationMs int64
	// TTFTMs is the time-to-first-token: the elapsed milliseconds from the start
	// of the model stream to the FIRST observable output (text, reasoning, a
	// reasoning replay item, or a tool call) of the turn. It is 0 when no Clock is
	// injected OR when the turn produced no observable output at all (a genuinely
	// empty turn) — a 0 here is "not measured", never a real zero, so telemetry
	// must guard against recording bogus zeros.
	TTFTMs int64
	// InterTokenMeanMs is the per-turn MEAN gap, in milliseconds, between
	// consecutive streaming content deltas (text or reasoning) within the turn —
	// the typical streaming smoothness. It feeds the mecatl.inter_token histogram.
	// It is 0 when fewer than two streaming content deltas were observed (no gap
	// exists) or no Clock is injected. A tool call or reasoning replay blob is NOT a
	// streaming delta and never contributes a gap.
	InterTokenMeanMs int64
	// InterTokenMaxMs is the per-turn WORST (largest) single gap, in milliseconds,
	// between consecutive streaming content deltas within the turn — the jitter
	// spike users feel. It feeds the mecatl.inter_token.max histogram. It is 0 under
	// the same <2-delta / no-Clock conditions as InterTokenMeanMs.
	InterTokenMaxMs int64
}

// The three DELEGATION observability families — SubagentPayload / ParallelPayload /
// TeamPayload — all project the SAME underlying child-loop LIFECYCLE: a redacted child
// doing tool work, start → tool → end. The shared lifecycle is the parent call id, a
// child/branch/member identity, a tool name / error bool / running count, usage, stop
// reason, and duration, PLUS — as of ADR 0079 — the bounded preview fields (Text /
// Detail / InnerKind) all three families now carry for their tool/message events; and
// the redaction of that lifecycle (previews included) is shared in EXACTLY ONE place —
// agent.drainChildObserved (the single redaction chokepoint all three reuse, every
// preview fed through the shared clampPreview). The three payloads therefore differ
// ONLY in their AGGREGATION shape, not their lifecycle:
//   - SubagentPayload — a FLAT fleet (one row per child, no grouping).
//   - ParallelPayload — a fan-out GROUP (branches share a join mode + a single winner +
//     preserved per-branch fork paths).
//   - TeamPayload     — a coordinating ROSTER (a task board + findings ledger + mailbox).
//
// Do NOT merge these three into one discriminated payload: the shared part is already
// shared (drainChildObserved), and the divergent parts (join/winner/fork-paths vs
// tasks/findings/dispositions vs flat fleet) are exactly what each family exists to
// carry. TRIP-WIRE: a 4th delegation family is the point to extract a shared
// ChildActivity value object for the common lifecycle scalars — NOT before (three, with
// the redaction already shared, does not warrant the shipped-contract migration cost).
// The shared failure-cause projection (subagentCausePayload, the one normalisation
// chokepoint) now crosses Subagent AND Team (TeamPayload.Cause mirrors
// SubagentPayload.Cause on the per-round EvTeamMember result inner kind);
// ParallelPayload deliberately omits it (a branch failure reaches the model via the
// Parallel ToolResult text, not an event field). That shared chokepoint is NOT itself a
// ChildActivity extraction — it is a helper, not a value-object migration — and the
// 4th-family bar stands.

// SubagentPayload is the REDACTED observability projection carried by the three
// subagent.* events (EvSubagentStart / EvSubagentTool / EvSubagentEnd). It is the
// ONLY information about a Subagent tool's child run that surfaces to clients.
//
// REDACTION CONTRACT — bounded previews (ADR 0079, superseding the former
// metadata-only contract): on tool events it deliberately forwards BOUNDED previews
// of the child's content — Text is a bounded, clamped preview of the child's message
// text, Detail is a bounded, clamped preview of a child tool call's args or a tool
// result's body. Every such preview is CAPPED — a control-byte scrub plus a rune cap
// applied by clampPreview in engine/agent — so an unbounded args/result/message body
// can never be copied verbatim, and a child's permission.ask is DROPPED entirely: it
// is NEVER forwarded, so a pending-ask reason (which can quote secrets or sensitive
// args) never reaches the stream. The forwarding is CLIENT-ONLY: nothing here ever
// enters the parent Session's Conversation (gauntlet #7 unchanged) — only the
// Subagent tool's own ToolResult text does, so the LLM's context is untouched.
//
// Which fields are set depends on the event kind:
//   - EvSubagentStart: ParentCallID, ChildID, Goal, [RoutedCategory, RoutedModel, RoutingReason], Model.
//   - EvSubagentTool:  ParentCallID, ChildID, ToolName, IsError, ToolCount, and —
//     when a preview is available — Text / Detail / InnerKind (which inner event kind
//     the preview came from: message.delta / tool.call / tool.result / result).
//   - EvSubagentEnd:   ParentCallID, ChildID, ToolCount, Usage, Stop, [Cause], DurationMs.
type SubagentPayload struct {
	// ParentCallID is the parent's Subagent tool-call id, used by clients to attribute
	// this event to the originating Subagent card. Set on all three kinds.
	ParentCallID string
	// ChildID is the child session id, distinguishing concurrent subagents. Set on
	// all three kinds.
	ChildID string
	// Goal is a short, plain-text label for the delegated task (the Subagent call's
	// description, or a truncation of its prompt). Set on EvSubagentStart only.
	Goal string
	// Background marks a detached-delivery child (`background: true` on the Subagent
	// call): the tool call returned an immediate started-result and the child keeps
	// working while the parent continues; its result is collected via
	// SubagentStatus. Set on EvSubagentStart only. NOT a fourth delegation family —
	// one boolean on subagent.* (the ChildActivity trip-wire stands).
	Background bool
	// RoutedCategory / RoutedModel are the OPT-IN semantic model router's classification
	// for this child (ADR 0031): the chosen CATEGORY label and the concrete MODEL id the
	// child was minted on. Set on EvSubagentStart ONLY when the router was wired AND
	// classified this delegation (both empty otherwise — no router, or a fail-soft miss
	// that inherited the default model). They are BARE METADATA — a category label and a
	// model id, never the task prompt or the classifier's reasoning — so they are
	// gauntlet-#7 safe (no child content, no model-influenced free text crosses). They
	// surface end-to-end: the session struct + a per-classification INFO (dispatch-path
	// `routeTask` closure) + a Build-once "router ACTIVE" fact + the proto/client wire
	// (`routed_category`/`routed_model` on the `Subagent` event payload, relayed through
	// the gRPC + HTTP relays and the mecatui client).
	RoutedCategory string
	RoutedModel    string
	// RoutingReason names WHY the router did NOT classify this delegation (EvSubagentStart
	// only): EMPTY on a routed hit (RoutedCategory/RoutedModel set), otherwise one of the
	// RoutingReason* gate constants (pinned-model / agent-def-pinned-model / resume / fork /
	// router-disabled / breaker-open / aborted) or a static miss code from the classifier
	// (the RouterMiss* values) or composition (e.g. category-selector-empty). Open callback
	// detail is reduced to a static/generic code before emission. It is BARE METADATA — a
	// bounded harness/composition reason code, never the task prompt
	// or the classifier's reasoning — so it is gauntlet-#7 safe (no child content, no
	// model-influenced free text crosses). Clamped at the emit site.
	RoutingReason string
	// Model is the concrete MODEL id the child ACTUALLY ran on (EvSubagentStart only),
	// set unconditionally — inherited default, agent-def pin, per-call `model` override,
	// or the opt-in router — independent of whether the router fired. It is BARE METADATA
	// — a model id, never child content — so it is gauntlet-#7 safe (no child content
	// crosses). When the router classified this delegation, Model == RoutedModel. It rides
	// the proto/client wire end-to-end (subagent.start: Subagent.model = field 13),
	// surfaced via the server mapper — see ADR 0035.
	Model string
	// ToolName is the name of a child tool that just ran. Set on EvSubagentTool
	// only. It is the tool NAME alone — never the child's tool args or result.
	ToolName string
	// IsError reports whether the child tool call failed. Set on EvSubagentTool
	// only.
	IsError bool
	// ToolCount is the running (EvSubagentTool) or final (EvSubagentEnd) number of
	// child tool calls STARTED. It is CUMULATIVE and stamped on EVERY projection, so
	// a client assigns it (never sums) with no InnerKind guard — it is always current,
	// never 0-after-positive. (Counted at the call, not the result, because a result
	// may never arrive on a cancel.)
	ToolCount int
	// Text is a BOUNDED preview of the child's message/result text — control-byte
	// scrubbed and rune-capped by clampPreview in engine/agent, never the raw,
	// unbounded body. Set on EvSubagentTool for the message.delta / result inner
	// kinds when a preview is available.
	Text string
	// Detail is a BOUNDED preview of a child tool call's args (tool.call) or a
	// tool result's body (tool.result) — control-byte scrubbed and rune-capped by
	// clampPreview in engine/agent, never the raw, unbounded args/result body. Set
	// on EvSubagentTool for the tool.call / tool.result inner kinds when a preview
	// is available.
	Detail string
	// InnerKind discriminates which inner child event kind the projection came from
	// (message.delta / tool.call / tool.result / result / turn.end) and which preview
	// fields it populates (Text/Detail). A turn.end projection carries NO Text/Detail;
	// it only advances Usage. A child's permission.ask is never projected.
	InnerKind EventType
	// Usage is the child run's cumulative provider-reported token accounting. Like
	// ToolCount it is CUMULATIVE and stamped on EVERY projection (zero until the
	// first turn.end), so a client assigns it with no InnerKind guard and a dropped
	// frame cannot drift the figure. It is provider truth only — the issue-#82
	// display-only estimate is never folded in.
	Usage Usage
	// Stop is the child run's terminal stop reason. Set on EvSubagentEnd only.
	Stop StopReason
	// Cause carries the child run's failure detail when Stop is StopError (empty
	// otherwise) — the mirror of ResultPayload.Error for the delegation projection.
	// Set on EvSubagentEnd ONLY.
	//
	// It is HARNESS/PROVIDER metadata — a transport or loop error string, or (for a
	// failure BEFORE the child run started, e.g. workspace isolation) the harness's own
	// error text — NOT child-authored model output, so it is gauntlet-#7 safe on the
	// same footing as Stop/Usage.
	//
	// It is LINE-ORIENTED by contract: every emit site normalises it through one helper
	// (whitespace collapsed to single spaces, then rune-clamped), because its consumers
	// are single-line surfaces — a fleet-roster row, an ACP status line, a log line — and
	// a provider error body routinely carries real newlines. A consumer renders it as-is
	// rather than re-deriving the collapse. (The MODEL-facing failure body keeps its
	// newlines; that is prose in a conversation, not a row.)
	Cause string
	// DurationMs is the child run's wall-clock duration in milliseconds
	// (best-effort). Set on EvSubagentEnd only.
	DurationMs int64
}

// ParallelPayload is the REDACTED observability projection carried by the parallel.*
// events (EvParallelStart / EvParallelBranch / EvParallelEnd).
//
// REDACTION CONTRACT — bounded previews (ADR 0079, superseding the former
// metadata-only contract): on branch tool events it deliberately forwards BOUNDED
// previews of the branch's content — Text is a bounded, clamped preview of the
// branch's message text, Detail is a bounded, clamped preview of a branch tool
// call's args or a tool result's body. Every such preview is CAPPED — a
// control-byte scrub plus a rune cap applied by clampPreview in engine/agent — so an
// unbounded args/result/message body can never be copied verbatim, and a branch's
// permission.ask is DROPPED entirely: it is NEVER forwarded, so a pending-ask reason
// (which can quote secrets or sensitive args) never reaches the stream. The only
// other non-scalar it carries is the per-branch fork-root PATHS (a handle the model
// is already given in the Parallel ToolResult text, not branch content). The
// forwarding is CLIENT-ONLY: nothing here ever enters the parent Session's
// Conversation (gauntlet #7 unchanged).
//
// Unlike the FLAT SubagentPayload, a Parallel run is a GROUP: N branches of ONE call
// (keyed by ParentCallID) sharing a join strategy, a single winner (join=first/judge),
// and preserved per-branch fork paths. Those are RUN-LEVEL facts carried on the
// start/end events; the per-branch events carry per-branch metadata keyed by BranchIndex.
//
// Which fields are set depends on the event kind:
//   - EvParallelStart:                       ParentCallID, Join, BranchCount.
//   - EvParallelBranch (Kind=branch_start):  ParentCallID, Kind, BranchIndex, ChildID, BranchLabel, Goal, [RoutedCategory, RoutedModel, RoutingReason], Model.
//   - EvParallelBranch (Kind=branch_tool):   ParentCallID, Kind, BranchIndex, ToolName, IsError, ToolCount, and — when a preview is available — Text / Detail / InnerKind.
//   - EvParallelBranch (Kind=branch_end):    ParentCallID, Kind, BranchIndex, ChildID, ToolCount, Stop, Usage, DurationMs, Failed, Workspace.
//   - EvParallelEnd:                         ParentCallID, Join, BranchCount, Winner, WinnerWorkspace, Usage (run total), Stop.
type ParallelPayload struct {
	// ParentCallID is the parent's Parallel tool-call id; it is the GROUP key (one
	// Parallel call = one group) and attributes every parallel.* event to the
	// originating Parallel card. Set on all kinds.
	ParentCallID string
	// Kind discriminates the per-branch lifecycle transition (branch_start /
	// branch_tool / branch_end). Set on EvParallelBranch only; empty on the run-level
	// start/end events.
	Kind ParallelEventKind

	// Join is the normalized join strategy ("all" / "first" / "judge"). Set on
	// EvParallelStart and EvParallelEnd (run-level).
	Join string
	// BranchCount is the number of branches in the run. Set on EvParallelStart and
	// EvParallelEnd (run-level).
	BranchCount int

	// BranchIndex is the 0-based branch index — the stable per-branch group key within
	// a Parallel call. Set on every EvParallelBranch kind. (The index is the row key;
	// ChildID below is the ADDRESSING handle.)
	BranchIndex int
	// ChildID is the branch's child SESSION id ("parallel-<callID>-<i>") — the uniform
	// per-child cancel/inspect handle (the same single-handle convention as
	// SubagentPayload.ChildID / the Team member session id), surfaced so a client can
	// address a branch (CancelChild) WITHOUT deriving the id grammar. Set on the
	// branch_start and branch_end kinds. It is an id, never branch content.
	ChildID string
	// BranchLabel is the humanized 1-based branch label ("branch-1" …). Set on the
	// branch_start kind.
	BranchLabel string
	// Goal is a short, plain-text label for the branch's task (a truncation of the
	// model-authored branch prompt — the parent's own instruction, NOT branch content).
	// Set on the branch_start kind. Clamped identically to SubagentPayload.Goal.
	Goal string
	// RoutedCategory / RoutedModel are the OPT-IN semantic model router's classification
	// for this branch (ADR 0031 / ADR 0034): the chosen CATEGORY label and the concrete
	// MODEL id the branch was minted on. Set on the branch_start kind ONLY when the router
	// was wired AND classified this branch (both empty otherwise — no router, a fail-soft
	// miss that inherited the default branch model, or a branch cancelled before it started).
	// Like SubagentPayload.RoutedCategory/RoutedModel they are BARE METADATA — a category
	// label and a model id, never the branch prompt or the classifier's reasoning — so they
	// are gauntlet-#7 safe (no branch content, no model-influenced free text crosses). They
	// ride the proto/client wire end-to-end (parallel.branch_start: Parallel.routed_category
	// = field 19 / routed_model = field 20), surfaced via the server mapper — see ADR 0034.
	RoutedCategory string
	RoutedModel    string
	// RoutingReason names WHY the router did NOT classify this branch (branch_start kind
	// only): EMPTY on a routed hit (RoutedCategory/RoutedModel set), otherwise one of the
	// RoutingReason* gate constants (router-disabled, or aborted for a branch cancelled
	// before it started) or a static miss code from the classifier (the RouterMiss* values)
	// or composition. Open callback detail is reduced before emission. It is BARE METADATA
	// — a bounded harness/composition reason code, never the branch prompt or classifier
	// reasoning — so it is gauntlet-#7 safe (no branch content crosses). Clamped at the
	// emit site.
	RoutingReason string
	// Model is the concrete MODEL id the branch ACTUALLY ran on (branch_start kind only),
	// set unconditionally — inherited default branch model or the opt-in router —
	// independent of whether the router fired. It is BARE METADATA — a model id, never
	// branch content — so it is gauntlet-#7 safe (no branch content crosses). When the
	// router classified this branch, Model == RoutedModel. It rides the proto/client wire
	// end-to-end (parallel.branch_start: Parallel.model = field 21), surfaced via the
	// server mapper — see ADR 0035.
	Model string

	// ToolName is the name of a branch's child tool that just ran. Set on the
	// branch_tool kind only. It is the tool NAME alone — never branch args/result.
	ToolName string
	// IsError reports whether that branch tool call failed. Set on branch_tool only.
	IsError bool
	// ToolCount is the running (branch_tool) or final (branch_end) number of a branch's
	// child tool calls observed.
	ToolCount int
	// Text is a BOUNDED preview of the branch's message/result text — control-byte
	// scrubbed and rune-capped by clampPreview in engine/agent, never the raw,
	// unbounded body. Set on the branch_tool kind for the message.delta / result
	// inner kinds when a preview is available.
	Text string
	// Detail is a BOUNDED preview of a branch tool call's args (tool.call) or a
	// tool result's body (tool.result) — control-byte scrubbed and rune-capped by
	// clampPreview in engine/agent, never the raw, unbounded args/result body. Set
	// on the branch_tool kind for the tool.call / tool.result inner kinds when a
	// preview is available.
	Detail string
	// InnerKind discriminates which inner branch event kind the preview came from
	// (message.delta / tool.call / tool.result / result). Set on the branch_tool
	// kind alongside Text / Detail. A branch's permission.ask is never projected.
	InnerKind EventType

	// Failed reports whether the branch's child run failed (StopError / cancelled /
	// fork failure). Set on the branch_end kind.
	Failed bool
	// Workspace is this branch's forked workspace ROOT path — the no-auto-merge handle
	// (the same path surfaced in the Parallel ToolResult text). It is server-side path
	// text, NOT branch conversation content. Set on the branch_end kind.
	Workspace string

	// Stop is the branch's terminal stop reason (branch_end) or the run-level stop
	// (EvParallelEnd; the winner's stop for join=first/judge, zero/omitted for join=all).
	Stop StopReason
	// Usage is the branch's cumulative usage (branch_end) or the run TOTAL (EvParallelEnd,
	// summed across branches).
	Usage Usage
	// DurationMs is the branch's wall-clock duration in milliseconds. Set on branch_end.
	DurationMs int64

	// Winner is the branch index of the selected winner on EvParallelEnd — a real
	// BranchIndex for join=first/judge, or -1 for join=all and none-succeeded. Set on
	// EvParallelEnd only.
	Winner int
	// WinnerWorkspace is the PRESERVED winner fork root on EvParallelEnd (the deliverable
	// handle for join=first/judge); empty for join=all / none-succeeded. Set on
	// EvParallelEnd only.
	WinnerWorkspace string
}

// SchedulePayload is the structured detail carried by the schedule.* events
// (EvScheduleFired / EvScheduleSkipped / EvScheduleFailed). It mirrors the
// schedule.proto SchedulePayload one-for-one. It is emitted from COMPOSITION
// (the scheduler) at fire time, NOT the agent loop (engine/agent never imports
// port.ScheduleStore — the tick loop, cron parsing, misfire policy, and
// leader-lease acquisition all live in composition). For v1 it is delivered to
// the fire session's durable EventLog ONLY (pull-only via GetFire/ListFires);
// a live broadcast stream is a future phase. Skipped fires (no session id) are
// dropped from the durable log (session-keyed) and surface only via the
// operator diagnostic.
//
// STRING-PASSTHROUGH DISCIPLINE: Kind / Stop / Err are plain strings (the
// EvNoProgress / StopBudget precedent — no proto enum, no closed set a later
// value would silently mis-classify). Kind is "fired" / "skipped" / "failed";
// Stop is a session.StopReason; Err is a flat error string.
type SchedulePayload struct {
	// ScheduleName is the schedule that fired / was skipped / failed (the
	// ScheduleSpec.Name foreign key).
	ScheduleName string
	// FireID is the per-fire session id (the same id ScheduleFire.ID and
	// ScheduleFire.SessionID carry — the fire's id IS its session id on the wire,
	// the FireNowResponse.fire_id == session_id contract).
	FireID string
	// SessionID is the session the fire ran as. It equals FireID for a fired
	// fire; it is empty for a skipped fire (no session was created).
	SessionID SessionID
	// Kind is the event kind: "fired" / "skipped" / "failed". String passthrough.
	Kind string
	// Stop is the terminal stop reason of the fire's run (a StopReason). Empty
	// for a skipped fire (no run happened) and for a fired fire that has not yet
	// completed.
	Stop StopReason
	// Err is the error string if the fire's run failed (Kind="failed"), empty
	// otherwise. It is a flat string (no structured error crosses) so a consumer
	// can render it without importing the run's error types.
	Err string
}

// TeamMemberSpec is one roster entry forwarded on EvTeamStart: the member name,
// its role label, and the read-only/mutating and lead flags, plus the OPT-IN model
// router's bare-metadata routing projection ([RoutedCategory, RoutedModel,
// RoutingReason], Model). It is a small value type carrying ONLY model-supplied
// metadata about the team's shape — never any member content (no prompt body, no
// transcript). It mirrors the proto TeamMemberSpec.
type TeamMemberSpec struct {
	// Name is the member's unique handle.
	Name string
	// Role is the member's short role label (the model-supplied role string).
	Role string
	// Mutating reports whether the member runs in an isolated fork with
	// workspace-mutating tools (true) or shares the base read-only (false).
	Mutating bool
	// Lead marks the coordinating member.
	Lead bool
	// RoutedCategory / RoutedModel are the OPT-IN semantic model router's classification
	// for this member (ADR 0031 / ADR 0034): the chosen CATEGORY label and the concrete
	// MODEL id the member's engine was minted on. Set on the EvTeamStart roster entry ONLY
	// when the router was wired AND classified this member (both empty otherwise — no
	// router, a fail-soft miss that inherited the default member model, or a DEFINED member
	// whose agent def pinned its own model so the router never fired). Like the Subagent and
	// Parallel routed fields they are BARE METADATA — a category label and a model id, never
	// the member's role/prompt or the classifier's reasoning — so they are gauntlet-#7 safe
	// (no member content crosses). They ride the proto/client wire end-to-end (team.start
	// roster: TeamMemberSpec.routed_category = field 5 / routed_model = field 6), surfaced
	// via the server mapper — see ADR 0034.
	RoutedCategory string
	RoutedModel    string
	// RoutingReason names WHY the router did NOT classify this member (EvTeamStart roster
	// entry only): EMPTY on a routed hit (RoutedCategory/RoutedModel set), otherwise one of
	// the RoutingReason* gate constants (agent-def-pinned-model for a DEFINED member,
	// router-disabled when no router is wired) or a static miss code from the classifier
	// (the RouterMiss* values) or composition. Open callback detail is reduced before
	// emission. It is BARE METADATA — a bounded harness/composition reason code, never the
	// member's role/prompt or classifier reasoning — so it is gauntlet-#7 safe (no member
	// content crosses). Clamped at the emit site.
	RoutingReason string
	// Model is the concrete MODEL id the member's engine ACTUALLY runs on (EvTeamStart
	// roster entry only), set unconditionally — inherited default member model, agent-def
	// pin, or the opt-in router — independent of whether the router fired. It is BARE
	// METADATA — a model id, never member content — so it is gauntlet-#7 safe (no member
	// content crosses). When the router classified this member, Model == RoutedModel. It
	// rides the proto/client wire end-to-end (team.start roster: TeamMemberSpec.model =
	// field 7), surfaced via the server mapper — see ADR 0035.
	Model string
}

// Routing-reason gate constants (issue #397): the CLOSED set of harness-authored reasons
// a delegation-start event carries on RoutingReason when the OPT-IN model router did NOT
// classify it. Empty ("") is the routed-hit sentinel; every non-empty value is a
// miss/gate. The CLASSIFIER-side miss reasons (the RouterMiss* values in engine/agent)
// and composition's category-mapping reason codes (e.g. "category-selector-empty") are NOT
// duplicated here — they flow through the same internal string channel, then the event
// projection confines them to static codes.
//
// All reasons are metadata ONLY — never the task prompt or the classifier's output
// (gauntlet #7).
const (
	// RoutingReasonPinnedModel: a per-call `model:` arg pinned the child's engine, so
	// the router never fired (the explicit choice wins by design).
	RoutingReasonPinnedModel = "pinned-model"
	// RoutingReasonAgentDefPinned: a named `agent` (Subagent) or DEFINED team member
	// whose agent def pinned its own model, so the router never fired.
	RoutingReasonAgentDefPinned = "agent-def-pinned-model"
	// RoutingReasonResume: a `resume:` call continues a persisted child on its own
	// engine — the router never fires for a resume.
	RoutingReasonResume = "resume"
	// RoutingReasonFork: a `fork: true` call inherits the parent engine — the router
	// never fires for a fork.
	RoutingReasonFork = "fork"
	// RoutingReasonRouterDisabled: no router is wired (the parentCaps routeTask closure
	// is nil — router absent), or the delegation cannot consume a routed pick (for
	// example an ineligible named agent or an unwired writable/agent model factory).
	RoutingReasonRouterDisabled = "router-disabled"
	// RoutingReasonTargetUnavailable: the router selected a concrete model, but the
	// engine factory could not build that target and the delegation therefore fell
	// back to its inherited/default engine. The router is fail-soft, but the event
	// must not claim the rejected target was actually used.
	RoutingReasonTargetUnavailable = "route-target-unavailable"
	// RoutingReasonBreakerOpen: the per-run router circuit breaker was OPEN (too many
	// consecutive misses), so the classifier was skipped and the delegation inherited
	// the default model.
	RoutingReasonBreakerOpen = "breaker-open"
	// RoutingReasonAborted: the run was already tearing down (the parent run's
	// hardAbort fired), so the classifier was skipped; also a Parallel branch cancelled
	// before it ever started.
	RoutingReasonAborted = "aborted"
)

// TeamTaskSnapshot is one entry in the team's shared task list, projected onto the
// event stream so the ctrl+a agents task sub-view can render the team's task state
// (id · state · assignee · deps) without an out-of-band ListTeam RPC — the team is
// a Team-tool-local object the TUI cannot address. It is a plain value type
// mirroring the proto TeamTask; it carries only task metadata (no member content).
// Deps are the task ids this task depends on (it is blocked until they complete).
type TeamTaskSnapshot struct {
	// ID is the stable task identifier.
	ID string
	// Description is a BOUNDED preview of the work to do (capped like every other
	// member-derived preview).
	Description string
	// State mirrors team.TaskState: "pending" / "in_progress" / "completed".
	State string
	// Assignee is the member name that claimed the task, or empty if unclaimed.
	Assignee string
	// Deps lists the task ids that must complete before this task is claimable.
	Deps []string
}

// TeamFindingSnapshot is one entry of the team findings ledger, projected onto the
// event stream so a watching client (the ctrl+a agents overlay) can see findings
// accrue. It is a plain value type carrying only the recording member's name and a
// BOUNDED body preview (clampPreview), never the raw finding. Like TeamTaskSnapshot
// it lives in session (session never imports team); the team.Finding → snapshot
// bridge lives in engine/agent.
type TeamFindingSnapshot struct {
	// Member is the name of the member that recorded the finding.
	Member string
	// Body is a BOUNDED preview of the finding text (capped like every other
	// member-derived preview).
	Body string
}

// TeamMemberDisposition is one member's TERMINAL disposition, projected onto the
// team.end snapshot so a watching client can render a stopped member distinctly from
// a clean one (instead of recomputing "done" and contradicting the supervisor). It
// carries ONLY closed-enum supervisor verdicts — never member-authored content — so
// it needs no preview cap and opens no redaction surface (Name is already forwarded
// verbatim on the team.start roster). Disposition is "done"/"stopped"; Reason is
// "error"/"cancelled"/"budget" (empty for a done member). Like TeamTaskSnapshot it
// lives in session (session never imports team); the MemberOutcome → snapshot bridge
// lives in engine/agent. Disposition/Reason are plain strings here (the domain
// stays free of the engine/agent enum types, mirroring TeamTaskSnapshot.State);
// the closed-enum guarantee is enforced at the bridge.
type TeamMemberDisposition struct {
	// Name is the member name (matches a roster entry by name).
	Name string
	// Disposition is "done" / "stopped".
	Disposition string
	// Reason is "error" / "cancelled" / "budget"; empty when done.
	Reason string
	// ErrorRounds is how many of this member's rounds ended in a run-level error. It is
	// a plain COUNT of supervisor verdicts (no member content, no cap needed), and it is
	// what keeps the disposition HONEST now that a member's errored round is bounded-
	// retried rather than always terminal (issue #318): such a member finishes
	// Disposition "done" with no Reason, so the count is the only signal a client has
	// that the run was not clean.
	//
	// It is INDEPENDENT of Disposition/Reason: it counts errored rounds over the member's
	// whole LIFETIME, so it is 0 exactly when the member never had an errored round —
	// NOT when it ended cleanly. A member that failed a round, was recovered and retried,
	// and was then cancelled reports Reason "cancelled" with ErrorRounds 1. Render the
	// two together; do not derive either from the other.
	ErrorRounds int
}

// TeamPayload is the BOUNDED observability projection carried by the team.* events
// (EvTeamStart / EvTeamMember / EvTeamTasks / EvTeamFindings / EvTeamEnd). It is the ONLY information
// about an in-process team's run that surfaces to clients on the event stream.
//
// REDACTION CONTRACT — fuller-but-bounded. Like SubagentPayload / ParallelPayload
// (bounded previews per ADR 0079) but fuller, since a team is meant to be WATCHED:
// this payload deliberately forwards member
// CONTENT on team.member events: the member's streamed/terminal message text and
// BOUNDED previews of its tool calls (name + capped arg preview) and tool results
// (error bool + capped body preview). Every such preview is CAPPED (see
// maxTeamPreview) so an unbounded args/result body can never be copied verbatim,
// and a member's permission.ask is DROPPED entirely — it is NEVER forwarded, so a
// pending-ask reason (which can quote secrets or sensitive args) never reaches the
// stream. This forwarding is orthogonal to the parent conversation: the team's
// per-member transcripts NEVER enter the parent Session's Conversation; only the
// Team tool's joined-summary ToolResult does. So the LLM's context still sees only
// the summary, exactly like Subagent/Fork.
//
// Which fields are set depends on the event kind:
//   - EvTeamStart:  ParentCallID, TeamID, Roster.
//   - EvTeamMember: ParentCallID, TeamID, Member, MemberSessionID, InnerKind, and
//     the subset of {Text, ToolName, Detail, IsError, Usage, ContextUsed,
//     ContextWindow} relevant to InnerKind, and, when the round's result was
//     StopError, Cause.
//   - EvTeamTasks:  ParentCallID, TeamID, Tasks (the team-wide task snapshot; no
//     Member).
//   - EvTeamFindings: ParentCallID, TeamID, Findings (the team-wide findings ledger
//     snapshot; no Member).
//   - EvTeamEnd:    ParentCallID, TeamID, Rounds, Stop, Usage (cumulative), Tasks
//     (the terminal task snapshot), Findings (the terminal findings snapshot),
//     Dispositions (the per-member terminal disposition snapshot).
type TeamPayload struct {
	// ParentCallID is the parent's Team tool-call id, attributing every team.*
	// event to the originating Team card. Set on all three kinds.
	ParentCallID string
	// TeamID is the team id, distinguishing concurrent teams. Set on all kinds.
	TeamID string
	// Roster is the team's membership as the model formed it. Set on EvTeamStart
	// only. It carries only member metadata, never member content.
	Roster []TeamMemberSpec
	// Member is the name of the member whose activity this event projects. Set on
	// EvTeamMember only.
	Member string
	// MemberSessionID is the producing member's child SESSION id (MemberSessionID:
	// "team-<teamID>-<member>") — the uniform per-child cancel/inspect handle (the
	// same single-handle convention as SubagentPayload.ChildID), surfaced so a client
	// can address a member (CancelChild) WITHOUT deriving the id grammar. Set on
	// EvTeamMember only. It is an id, never member content.
	MemberSessionID string
	// InnerKind is the member's underlying session event kind being projected
	// (e.g. "message.delta", "tool.call", "tool.result", "turn.end", "result").
	// Set on EvTeamMember only. permission.ask is never projected.
	InnerKind EventType
	// Text is the member's message/result text or a BOUNDED preview of it. Set on
	// EvTeamMember for message.delta / result inner kinds.
	Text string
	// ToolName is the name of a member tool that was called. Set on EvTeamMember
	// for tool.call / tool.result inner kinds.
	ToolName string
	// Detail is a BOUNDED preview of a member tool call's args (tool.call) or
	// result body (tool.result) — capped at maxTeamPreview runes. It is never the
	// raw, unbounded args/result body. Set on EvTeamMember for tool.* inner kinds.
	Detail string
	// IsError reports whether a member tool.result failed. Set on EvTeamMember for
	// the tool.result inner kind.
	IsError bool
	// Rounds is the number of scheduling rounds that ran work. Set on EvTeamEnd
	// only.
	Rounds int
	// Stop is the team run's terminal stop reason. Set on EvTeamEnd only.
	Stop StopReason
	// Usage is the member's per-event usage (EvTeamMember turn.end/result) or, on
	// EvTeamEnd, the TEAM TOTAL — the sum of every member's per-turn usage.
	Usage Usage
	// ContextUsed is the member's CURRENT context occupancy — the most recent
	// turn's input-token count (Usage.InputTokens of the turn just ended), i.e.
	// what the next turn would carry into the model, not a cumulative sum. Set on
	// EvTeamMember turn.end; 0 when unknown. It feeds the per-member context meter
	// in the ctrl+a agents overlay (the team analogue of the main context meter).
	ContextUsed int64
	// ContextWindow is the producing member engine's context window in tokens (the
	// meter's denominator). Set on EvTeamMember turn.end; 0 when unknown (no meter
	// is drawn in that case).
	ContextWindow int64
	// Tasks is a snapshot of the team's SHARED TASK LIST in creation order. It is
	// set on an EvTeamTasks event (emitted on change, de-duped, from the Team tool's
	// member-event sink) and on EvTeamEnd (the terminal snapshot, so the final task
	// state always lands). It feeds the ctrl+a agents task sub-view; it carries only
	// task metadata, never member content.
	Tasks []TeamTaskSnapshot
	// Findings is a snapshot of the team's SHARED FINDINGS LEDGER in append order. It
	// is set on an EvTeamFindings event (emitted on change, de-duped) and on EvTeamEnd
	// (the terminal snapshot). It feeds the ctrl+a agents findings view; each entry
	// carries the recording member's name and a BOUNDED body preview, never the raw
	// finding.
	Findings []TeamFindingSnapshot
	// Dispositions is a per-member TERMINAL disposition snapshot, set ONLY on EvTeamEnd
	// (parallel to the terminal Tasks/Findings snapshots). It lets a client render a
	// stopped member distinctly from a clean one without recomputing "done". Each entry
	// carries closed-enum supervisor verdicts only, never member content. Empty on
	// every other kind.
	Dispositions []TeamMemberDisposition
	// Cause carries the member run's per-round FAILURE DETAIL when that round's
	// EvResult.Stop is StopError (empty otherwise) — the mirror of
	// ResultPayload.Error for the team-member projection. Set on EvTeamMember with
	// InnerKind=EvResult ONLY, and ONLY when the round failed; empty on every other
	// inner kind and on EvTeamEnd's disposition snapshot.
	//
	// It is HARNESS/PROVIDER metadata — a transport or loop error string, or (for a
	// failure BEFORE the member run started) the harness's own error text — NOT
	// member-authored model output, so it is gauntlet-#7 safe on the same footing as
	// Stop/Usage.
	//
	// It is LINE-ORIENTED by contract: the ONE emit site (projectTeamEvent) normalises
	// it through subagentCausePayload (whitespace collapsed to single spaces, then
	// rune-clamped to maxSubagentCausePreview), because its consumers are single-line
	// surfaces — a mecatui roster/focus row, an ACP status line, a log line — and a
	// provider error body routinely carries real newlines. A consumer renders it
	// as-is rather than re-deriving the collapse. The terminal EvTeamEnd disposition
	// stays the closed-enum reason; Cause is per-round, so a retried member's failed
	// rounds each surface their own cause. Mirrors SubagentPayload.Cause.
	Cause string
}

// Event is the domain-owned, provider-neutral unit of the streaming model. The
// loop runs as a producer writing Events to a channel; server adapters relay
// them to the gRPC server-stream or HTTP SSE.
type Event struct {
	// Type is the event kind.
	Type EventType
	// Seq is a monotonically increasing sequence number within a run.
	Seq int64
	// Turn is the turn index this event belongs to.
	Turn int
	// Text carries streamed or final text where applicable.
	Text string
	// ToolCall is set on EvToolCall.
	ToolCall *ToolCall
	// ToolResult is set on EvToolResult.
	ToolResult *ToolResult
	// Ask is set on EvPermissionAsk.
	Ask *PendingAsk
	// Result is set on EvResult.
	Result *ResultPayload
	// TurnEnd is set on EvTurnEnd (this turn's usage + elapsed time).
	TurnEnd *TurnEndPayload
	// Hook is set on EvHook: the structured phase/tool/decision so clients render
	// hook notices distinctly (and colour blocked ones) rather than parsing Text.
	Hook *HookPayload
	// Approval is set on EvApproval: the resolved verdict (tool name + verdict
	// string + askID + learned flag). It carries NO raw args and NO deny-reason
	// body (gauntlet #7); see ApprovalPayload.
	Approval *ApprovalPayload
	// CompactionArchive is set on EvCompactionArchive: the pre-compaction
	// conversation that ReplaceHistory replaced (the durable non-destructive
	// archive). It is the parent's own history, so it opens no leak surface; see
	// CompactionArchivePayload.
	CompactionArchive *CompactionArchivePayload
	// UserPrompt is set on EvUserPrompt: the user-role message just recorded (Text +
	// Parts). It is the durable record of what the user asked (and the harness's own
	// synthetic continuations), consumed by the EventLog and reconstructed by an
	// event-sourced fold; it is log-only (skipped on the live client wire). See
	// UserPromptPayload.
	UserPrompt *UserPromptPayload
	// Usage is set on usage-bearing events. On EvResult it is the cumulative run
	// total; turn.end carries its per-turn usage in TurnEnd, NOT here.
	Usage *Usage
	// Subagent is set on the three subagent.* events: the REDACTED observability
	// projection of a Subagent child run (metadata only, never child content).
	Subagent *SubagentPayload
	// Team is set on the team.* events (start / member / tasks / end): the BOUNDED
	// observability projection of an in-process team run (fuller-but-bounded; member
	// content is capped and permission.ask is dropped, and never enters the parent
	// conversation).
	Team *TeamPayload
	// Parallel is set on the parallel.* events (start / branch / end): the REDACTED,
	// metadata-only observability projection of a Parallel fork-join run (group-level
	// join/winner facts + per-branch metadata + fork paths, never branch content).
	Parallel *ParallelPayload
	// Actor is the verified caller who ACTED — who drove the request this event
	// belongs to (ADR 0100 decision 5). It is LOG-ONLY and DERIVE-AT-APPEND: every
	// emit site — the loop included — leaves it nil (the loop is storage-agnostic
	// and knows nothing about principals), and the server relay's single appendEvent
	// chokepoint stamps it from the CONTEXT PRINCIPAL just before the durable
	// Append. It never reaches the client wire (toProto has no field for it) and it
	// is not a reconstruction input: eventsource.Fold ignores it, so a folded
	// session keeps the owner its caller restored from the snapshot.
	//
	// Actor is NOT the session's Owner, and the two routinely differ: this phase
	// ships no authorization, so any authenticated caller may act on any session.
	// The owner answers "whose is this?" and remains the identity of record; the
	// actor answers "who did this?". Deriving it from the owner would stamp the
	// owner onto every event of a run somebody else drove — repudiation in both
	// directions, worst on EvApproval, where the record IS a human granting a tool
	// permission. The denormalization is deliberate: an event read in isolation
	// names its actor. A request with no verified caller records a nil Actor;
	// absence is never fabricated.
	Actor *Principal
	// Schedule is set on the schedule.* events (fired / skipped / failed): the
	// scheduler lifecycle projection emitted from composition (the scheduler), NOT
	// the loop. Client-visible (unlike the log-only EvApproval /
	// EvCompactionArchive / EvUserPrompt). See SchedulePayload.
	Schedule *SchedulePayload
}
