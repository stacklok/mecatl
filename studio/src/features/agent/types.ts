// The direct module, not the `@/lib/protocol` barrel: `protocol/events.ts`
// imports this file, so the barrel would close a cycle.
import type { DeliveryNoteInfo } from "@/lib/protocol/delivery-note";
import type { SessionRelationshipInfo } from "@/lib/protocol/sessions";

// ── Sessions ────────────────────────────────────────────────────────────────

export interface AgentSession {
  id: string;
  title: string;
  projectId: string | null;
  /** Enabled agent this chat belongs to, if any. Mutually exclusive with a project. */
  agentId?: string;
  model: string;
  createdAt: number;
  updatedAt: number;
  pinned: boolean;
  archived: boolean;
  messageCount: number;
  isStreaming: boolean;
  inputTokens: number;
  outputTokens: number;
  unread: boolean;
  estimatedCost: number | null;
  contextLength: number | null;
  lastPromptTokens: number | null;
  thresholdTokens: number | null;
  /** Daemon lifecycle state (idle/running/awaiting/completed/failed/cancelled). */
  state?: string;
  workspace?: string;
  /**
   * Action eligibility comes from the daemon row's capabilities, never
   * re-derived client-side; an omitted capability is a denial, and the closed
   * reason strings explain a disabled action.
   */
  canRename?: boolean;
  canDelete?: boolean;
  /** Whether the daemon offers this session's exact id for copying. */
  canCopyId?: boolean;
  /** Whether the daemon would mint a successor of this chat (fork / clear). */
  canFork?: boolean;
  renameReason?: string;
  deleteReason?: string;
  copyIdReason?: string;
  forkReason?: string;
  /**
   * Title provenance off the inventory row (F4): "operator" (hand-set — an
   * auto-rename must never clobber it), "first-prompt" (seeded, replaceable),
   * "" (unknown/older daemon — do not auto-rename).
   */
  titleProvenance?: string;
  /**
   * The title lifecycle revision off the inventory row (`title_metadata`);
   * null/absent on a legacy row or an older daemon. A live `session.title`
   * event is adopted only when its revision is newer (`session-title.ts`).
   */
  titleRevision?: number | null;
  /**
   * Non-empty on an AI-debug session (ADR 0254): the stored session this chat
   * diagnoses. Drives the sidebar's "debug" badge.
   */
  debugTargetSessionId?: string;
  /**
   * The daemon's closed kind (main | subagent | parallel_branch | team_member
   * | scheduled | debug | unknown; "" on an older daemon) — the sidebar's
   * Chats / Runs / Scheduled / Other tab key (`sessionTabFor`).
   */
  kind?: string;
  /** "draft" | "active" | "" — set only when the daemon advertises the
   *  `session_activity_inventory` feature; drives the Drafts tab. */
  activityState?: string;
  /**
   * Whether the row is an operator-facing chat (false for the inspect-only
   * kinds the Runs / Scheduled / Other tabs list). Omitted reads as a chat —
   * only the inventory decoder ever sets it false.
   */
  isChat?: boolean;
  /** Placement display metadata (ADR 0291): a label and branch, never a path. */
  placementLabel?: string;
  placementBranch?: string;
  /** The validated links for the row's kind (parent, call, team, schedule…). */
  relationship?: SessionRelationshipInfo;
  /** Whether the daemon offers the read-only inspect posture / the transcript. */
  canInspect?: boolean;
  canViewTranscript?: boolean;
  /** Why the row is not a public chat / cannot show its transcript ("" = it can). */
  publicChatReason?: string;
  viewTranscriptReason?: string;
}

export interface CreateSessionOpts {
  workspace?: string;
  model?: string;
  projectId?: string;
  /** Enabled agent this chat belongs to, if any. Mutually exclusive with a project. */
  agentId?: string;
}

// ── Messages ────────────────────────────────────────────────────────────────

export interface AgentMessage {
  id: string;
  role: "user" | "assistant" | "tool";
  content: string;
  timestamp: number;
  /**
   * Set on a user-role message that is a scheduled task's delivery note
   * (the daemon's fenced `[scheduled task …]` record): `content` then holds
   * ONLY the fire's outcome body — the fence and provenance header are
   * stripped, the card carries the attribution.
   */
  delivery?: DeliveryNoteInfo;
  /**
   * Set on a user-role message the HARNESS authored on the operator's behalf
   * — the plan-approved proceed prompt this tab sent to start the execution
   * run. Renders as a muted harness note, never as the user's own words.
   */
  synthetic?: boolean;
  attachments?: Attachment[];
  toolCalls?: ToolCallInfo[];
  reasoning?: string;
  artifact?: Artifact;
  /**
   * When set on an assistant message, names the specific agent that authored
   * that turn. This lets a single conversation surface several different agents
   * (e.g. a project chat where a Code Reviewer, Security Auditor, and Docs
   * Writer each contribute), rather than a single assistant identity. Falls back
   * to the chat's default bot name when unset.
   */
  agentName?: string;
  /**
   * A focused reply thread branched off this message. When present, the message
   * shows a reply indicator; opening it reveals the root message and these
   * replies in the side thread panel (mirrors the teams view's threading).
   */
  replies?: AgentMessage[];
  /** One-line advisories from the daemon (tool progress, unrendered events). */
  notices?: string[];
  /** Delegation cards: work this turn handed to child agents. */
  delegations?: DelegationInfo[];
  /**
   * Group-level facts for the Parallel fan-outs and Teams this turn ran,
   * keyed by the Parallel/Team tool call id (`parentCallId`) — the header the
   * card row renders above that group's branch/member cards.
   */
  delegationGroups?: Record<string, DelegationGroupInfo>;
  /**
   * The turn ended with result.stop === "error". A failed turn renders as
   * failed — never as an empty success.
   */
  failed?: boolean;
  failureDetail?: string;
  /**
   * The daemon typed the failure PERMANENT (ADR 0239): the identical
   * request is rejected, so the failed card says retrying won't help and
   * the error strip withholds Retry.
   */
  failurePermanent?: boolean;
  /**
   * How a NON-error run ended when that is worth saying (`max_turns`,
   * `budget`, `cancelled`, …): the stop-reason chip under the turn. Unset
   * for a clean `end_turn`; a failed turn uses `failed` instead.
   */
  stopReason?: string;
  /** A user message that reached the run as a mid-run steer (the daemon's
   *  drain echo landed): renders a small "steered" marker. */
  steered?: boolean;
  /**
   * The downstream provider the model gateway reported for THIS turn
   * (`provider.route`, ADR 0210) — the bubble's muted `via <route>` marker.
   * Display-only metadata: absent on a prompt-cache hit (never fabricated),
   * and the daemon never records it to the durable log, so it is set only
   * by a live stream/watch and is gone after a reload — a replay never
   * carries it.
   */
  route?: string;
  /**
   * Per-turn cost, accumulated from the daemon's `turn.end` frames (tokens,
   * cache reads, model-call time) — the muted stat line under a finished
   * assistant turn. `lastInputTokens` is the LATEST turn's input, i.e. the
   * current context occupancy the meter reads.
   */
  turnStats?: TurnStats;
}

/** Summed `turn.end` figures for one assistant message. */
export interface TurnStats {
  turns: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  durationMs: number;
  lastInputTokens: number;
}

/**
 * One delegated child on an assistant turn — a subagent, a team member lane,
 * or a parallel branch. Starts as a badge (subagent.start / team.start /
 * branch_start), then live-updates while the child works — `subagent.tool`,
 * `branch_tool`, and `team.member` frames tick the counters and the bounded
 * trace — and settles on its terminal (subagent.end / branch_end / the team's
 * dispositions) with the stop reason, duration, and (for a failed child) the
 * cause, so a failed child never silently vanishes.
 *
 * Update key: `childId` when both sides carry one; a parallel branch before
 * its branch_end is keyed by (`parentCallId`, `branchIndex`); a team member
 * by (`parentCallId` | `teamId`, `memberName`) until `team.member` backfills
 * its session id. See `delegation-fleet.ts` (`matchesDelegation`).
 *
 * `trace`, `lastTool`, and `cause` carry bounded, model-influenced previews
 * the daemon already clamped — render them as plain text nodes only, never
 * through markdown, and never persist or forward them.
 */
export interface DelegationInfo {
  kind: "subagent" | "team" | "parallel";
  label: string;
  detail: string;
  /** The child session id (`subagent.start` child_id) — the update key. */
  childId?: string;
  /** The Subagent/Parallel/Team tool call this child belongs to. */
  parentCallId?: string;
  /** Team member lane: the team id (from team.start / team.member). */
  teamId?: string;
  /** Team member lane: the roster name the `team.member` frames are tagged with. */
  memberName?: string;
  /** Parallel branch: the 0-based branch index within its group. */
  branchIndex?: number;
  /** Team member lane: this member leads the team (rendered first). */
  lead?: boolean;
  /** Team member lane: the member may mutate the workspace. */
  mutating?: boolean;
  /** The concrete model id the child actually runs on (bare metadata). */
  model?: string;
  /** Detached-delivery child: the parent run continues while it works. */
  background?: boolean;
  /** Why the model router did NOT route this delegation (bare metadata). */
  routingReason?: string;
  /** Cumulative child tool-call count (running, then final). */
  toolCount?: number;
  /** Cumulative child token accounting (live per turn, final on end). */
  inputTokens?: number;
  outputTokens?: number;
  /** The child's most recent tool, from the bounded activity projection. */
  lastTool?: string;
  /** The most recent tool.result reported an error. */
  lastToolError?: boolean;
  /**
   * Bounded per-child activity trace (tool chips + coalesced message lines),
   * capped at `MAX_TRACE_ENTRIES`, oldest dropped. Plain text only.
   */
  trace?: DelegationTraceEntry[];
  /** Team member: finished its round (a `result` frame), not terminal. */
  idle?: boolean;
  /** Team member: current context occupancy (most recent turn's input tokens). */
  contextUsed?: number;
  /** Team member: its engine's context window; sticky once known. */
  contextWindow?: number;
  /** Terminal stop reason; presence means the child has ended. */
  stop?: string;
  /** Wall-clock child duration in milliseconds (end only, best-effort). */
  durationMs?: number;
  /** Failure detail when stop === "error" (harness metadata, clamped). */
  cause?: string;
  /** Team member: benched by the supervisor (team.end disposition). */
  stopped?: boolean;
  /** Team member: why it was benched ("" = not stopped / unspecified). */
  stopReason?: DelegationStopReason;
  /** Team member: rounds that ended in error (retried up to the cap). */
  errorRounds?: number;
  /** Parallel branch: its child run failed (branch_end). */
  failed?: boolean;
  /** Parallel branch: the group's parallel.end named it the winner. */
  winner?: boolean;
  /** A cancel for this child is in flight (optimistic, cleared on its end). */
  cancelling?: boolean;
}

/** A team member's benching reason (proto TEAM_MEMBER_STOP_REASON_*; "" = unspecified). */
export type DelegationStopReason = "error" | "cancelled" | "budget" | "";

/**
 * One entry in a delegation lane's bounded trace: a tool chip (name + the
 * daemon-bounded arg/result preview + error state, `pending` until its
 * tool.result lands) or a coalesced message line. Plain text only.
 */
export interface DelegationTraceEntry {
  kind: "tool" | "message";
  /** Tool chip: the tool name. */
  name?: string;
  /** Tool chip: the bounded arg (call) or result preview. */
  detail?: string;
  /** Message line: the coalesced streamed text. */
  text?: string;
  isError?: boolean;
  /** Tool chip: no tool.result has resolved it yet. */
  pending?: boolean;
}

/**
 * Group-level facts for one Parallel fan-out or Team on a turn, keyed on the
 * message by the tool call id. `done` flips on parallel.end / team.end.
 */
export interface DelegationGroupInfo {
  kind: "parallel" | "team";
  /** Parallel: the join strategy (all / first / judge). */
  join?: string;
  /** Parallel: how many branches were fanned out. */
  branchCount?: number;
  /** Parallel: the winning branch index (−1 = none / join=all). */
  winner?: number;
  /** The group's run stop (parallel.end / team.end). */
  stop?: string;
  done?: boolean;
  /** Team: the team id (`InspectMember`'s handle). */
  teamId?: string;
  /** Team: rounds the supervisor ran. */
  rounds?: number;
  /** Group token total (team.end sums every member; parallel.end the run). */
  inputTokens?: number;
  outputTokens?: number;
  /** Team: members the supervisor benched (team.end dispositions). */
  stoppedCount?: number;
}

/** One task on a team's shared task board (team.tasks snapshot). */
export interface TeamTaskInfo {
  id: string;
  state: string;
  assignee: string;
  deps: string[];
  /** Model-authored; render as plain text only. */
  description: string;
}

/** One entry in a team's findings ledger (team.findings snapshot). */
export interface TeamFindingInfo {
  member: string;
  /** Daemon-clamped, model-authored; render as plain text only. */
  body: string;
}

/** How one team member ended (team.end). */
export interface TeamMemberDispositionInfo {
  name: string;
  stopped: boolean;
  errorRounds: number;
  reason: DelegationStopReason;
}

/**
 * A non-text part of a tool result (an MCP content block the daemon relayed):
 * an inline image, or a link to a resource the tool produced. Text blocks
 * are already folded into `output`; other block kinds are not rendered.
 */
export type ToolResultPart =
  | { kind: "image"; mimeType: string; data: string }
  | { kind: "resource_link"; url: string; name: string; title?: string };

/**
 * What a hook fire did (the wire's HookDecision, labelled): `blocked` vetoed
 * the action, `modified` rewrote its payload, `advisory` flagged content
 * without changing anything, `info` is a benign notice (also the daemon's
 * default for an unspecified decision).
 */
export type HookDecision = "info" | "blocked" | "modified" | "advisory";

/** One hook fire attributed to a tool call (`hook` event with a callId). */
export interface HookNotice {
  /** The hook phase (PreToolUse, PostToolUse, …), as the daemon names it. */
  phase: string;
  /** The tool the hook fired on; empty for a call-less lifecycle hook. */
  tool: string;
  decision: HookDecision;
  /** The daemon's human-readable message; may be empty. */
  text: string;
}

export interface ToolCallInfo {
  callId: string;
  name: string;
  input: unknown;
  /** The verbatim args JSON the live stream carried; the drill-down panel
   *  pretty-prints it instead of the flattened `input` preview. */
  rawArgs?: string;
  /** The file this call produced (Write), previewable in the canvas. */
  file?: import("@/lib/file-meta").ToolCallFile;
  /** The path this call mutated (Edit or Write) — the changed-files list. */
  changedPath?: string;
  output?: string;
  /** Image / resource-link blocks the result carried besides its text. */
  parts?: ToolResultPart[];
  isError?: boolean;
  /** Hook fires attributed to this call (PreToolUse / PostToolUse notices). */
  hooks?: HookNotice[];
  status: "running" | "completed" | "failed";
}

export interface Attachment {
  name: string;
  type: string;
  url?: string;
  content?: string;
}

// ── Stream Events ───────────────────────────────────────────────────────────

/**
 * Every translated event may carry the opaque, server-minted id of the run
 * that emitted it (Event.run_id, ADR 0249). Empty/absent is meaningful: the
 * event is session-scoped (e.g. schedule lifecycle), not run-scoped. Clients
 * compare it for equality only — it is the handle `expected_run_id` controls
 * (approve/cancel) are scoped to.
 */
export type StreamEvent = StreamEventBody & { runId?: string };

type StreamEventBody =
  | { type: "token"; text: string }
  | {
      type: "tool_call";
      name: string;
      callId: string;
      input: unknown;
      /** The verbatim args JSON string, alongside the flattened preview. */
      rawArgs?: string;
      file?: import("@/lib/file-meta").ToolCallFile;
      /** The path an Edit/Write call mutates (the changed-files list). */
      changedPath?: string;
    }
  | {
      type: "tool_result";
      callId: string;
      output: string;
      isError?: boolean;
      /** Image / resource-link blocks the result carried besides its text. */
      parts?: ToolResultPart[];
    }
  | {
      type: "approval";
      approvalId: string;
      sessionId: string;
      toolName: string;
      description: string;
      details: string;
      /** The ask's reason and verbatim args JSON (the raw tier `details`
       *  flattens), so the card can decode per tool and show the raw view. */
      reason?: string;
      args?: string;
    }
  | {
      type: "clarify";
      clarifyId: string;
      sessionId: string;
      question: string;
    }
  | { type: "reasoning"; text: string }
  | {
      type: "usage";
      inputTokens: number;
      outputTokens: number;
      cacheReadTokens?: number;
      cacheWriteTokens?: number;
      reasoningTokens?: number;
      estimatedCost: number | null;
    }
  /**
   * One model exchange closed (`turn.end`): that turn's own tokens and
   * elapsed model-call time. Per TURN, unlike `usage`, which is per RUN.
   */
  | {
      type: "turn_end";
      durationMs: number;
      inputTokens: number;
      outputTokens: number;
      cacheReadTokens: number;
      cacheWriteTokens: number;
      reasoningTokens: number;
    }
  /**
   * A `session.title` event: the daemon's durable title lifecycle changed
   * (first-prompt seed, auto-title, a rename from any client). Session
   * metadata for the inventory row — never a bubble. `revision` orders
   * replay against the row's `titleRevision`; null when the event had none.
   */
  | {
      type: "title";
      title: string;
      provenance: string;
      revision: number | null;
    }
  | {
      type: "done";
      session: AgentSession;
      messages: AgentMessage[];
    }
  | { type: "error"; message: string; details?: string }
  /** A previously surfaced permission ask was withdrawn by the daemon. */
  | { type: "retract"; approvalId: string }
  /**
   * Mid-run steer drain echo: the daemon merged the pending steer bundle into
   * the in-flight run. `text` is the drained bundle; `messageId` is the
   * watermark — the client-minted id of the LAST message the bundle absorbed.
   * `parts` is the committed media bundle (ADR 0251), byte-identical to what
   * history recorded, so a rebuilt transcript keeps the steer's attachments.
   */
  | { type: "steer"; text: string; messageId: string; parts?: SteerEchoPart[] }
  /** A one-line advisory (tool progress, compaction, unrendered event kinds). */
  | { type: "notice"; text: string }
  /**
   * A hook fired (`hook` event): phase + tool + labelled decision, with the
   * daemon's message in `text`. `callId` names the tool call a
   * PreToolUse/PostToolUse fire belongs to (the call's hook chips); a
   * lifecycle hook (SessionStart, UserPromptSubmit, Stop) has none and
   * renders as a marked transcript notice.
   */
  | {
      type: "hook";
      phase: string;
      tool: string;
      decision: HookDecision;
      callId: string;
      text: string;
    }
  /**
   * The downstream provider a turn was routed to (EvProviderRoute, ADR 0210):
   * metadata for the chat status strip's model segment (`model/route`),
   * never a transcript line. Absent on a prompt-cache hit — never fabricated.
   */
  | { type: "provider_route"; label: string }
  /**
   * A TRANSIENT advisory (the no-progress nudge, the pre-flight recover
   * notice): shown on the status line under the transcript until the next
   * run, never appended to a message's notices — a replay must not
   * resurrect it.
   */
  | {
      type: "status";
      text: string;
      tone: "muted" | "warn";
      kind: "no_progress" | "recover_notice";
    }
  /**
   * A recorded user message from the durable-log replay (EvUserPrompt): the
   * watch's record of what the user asked. Log-only — the live prompt stream
   * never carries it (the client already holds its own optimistic bubble).
   */
  | { type: "user_prompt"; text: string }
  /**
   * The resolved verdict half of a permission ask, from the durable-log
   * replay (EvApproval). Metadata only: tool NAME + verdict string
   * (allow_once / allow_always / deny) + the ask it resolved — never args.
   */
  | {
      type: "approval_verdict";
      approvalId: string;
      toolName: string;
      verdict: string;
    }
  /**
   * A tool call parked on a browser authorization (authorization.required):
   * the MCP server wants the operator to sign in before the call can run.
   * The daemon durably parks the run and CLOSES the prompt stream without a
   * result; the run continues over the authorization control stream
   * (recheck/cancel), never the prompt stream. `status` is the daemon's
   * closed grammar (pending / granted / denied / cancelled / expired / …).
   */
  | {
      type: "authorization";
      authorizationId: string;
      callId: string;
      displayName: string;
      status: string;
      /** Epoch milliseconds; absent when the daemon set no expiry. */
      expiresAt?: number;
    }
  /** The authorization reached a terminal status (authorization.resolved). */
  | {
      type: "authorization_resolved";
      authorizationId: string;
      displayName: string;
      status: string;
    }
  /**
   * Delegation activity: the run handed work to a child agent — one event per
   * subagent (subagent.start), per team roster member (team.start, lead
   * first), or per parallel branch (branch_start).
   */
  | {
      type: "delegation";
      kind: "subagent" | "team" | "parallel";
      label: string;
      detail: string;
      /** Child session id (subagent.start / branch_start), keying later live updates. */
      childId?: string;
      /** The Subagent/Team/Parallel tool call id this child belongs to. */
      parentCallId?: string;
      /** Team member: the team id. */
      teamId?: string;
      /** Team member: the roster name later `team_member` frames are tagged with. */
      memberName?: string;
      /** Parallel branch: its 0-based index within the group. */
      branchIndex?: number;
      lead?: boolean;
      mutating?: boolean;
      /** The concrete model the child runs on (bare metadata). */
      model?: string;
      background?: boolean;
      /** Why the router did not route this delegation (D2.1). */
      routingReason?: string;
    }
  /**
   * Live child activity (subagent.tool / parallel branch_tool): cumulative
   * tool/token counters plus the bounded activity preview for the delegation
   * card. Keyed by `childId` (subagent) or (`parentCallId`, `branchIndex`)
   * (a branch_tool carries no child id). `detail`/`text` are daemon-clamped,
   * model-influenced previews — plain text only.
   */
  | {
      type: "delegation_progress";
      childId?: string;
      parentCallId?: string;
      branchIndex?: number;
      toolCount?: number;
      inputTokens?: number;
      outputTokens?: number;
      toolName?: string;
      /** The child's inner event kind (tool.call / tool.result / message.delta / result). */
      innerKind?: string;
      isError?: boolean;
      detail?: string;
      text?: string;
    }
  /**
   * Child terminal (subagent.end / parallel branch_end): final counters, the
   * stop reason, the wall-clock duration, and — when stop === "error" — the
   * failure cause, so a failed child renders as failed instead of vanishing.
   */
  | {
      type: "delegation_end";
      childId?: string;
      parentCallId?: string;
      branchIndex?: number;
      stop: string;
      toolCount?: number;
      inputTokens?: number;
      outputTokens?: number;
      durationMs?: number;
      cause?: string;
      /** Parallel branch: the branch's child run failed. */
      failed?: boolean;
    }
  /** A Parallel fan-out began: the group header facts (parallel.start). */
  | {
      type: "parallel_start";
      parentCallId: string;
      join: string;
      branchCount: number;
    }
  /**
   * A Parallel fan-out ended (parallel.end): the winning branch index (−1 =
   * none / join=all), the run's stop, and the group's usage.
   */
  | {
      type: "parallel_end";
      parentCallId: string;
      join: string;
      branchCount: number;
      winner: number;
      stop: string;
      inputTokens?: number;
      outputTokens?: number;
    }
  /**
   * One team member's redacted activity (team.member), tagged with the member
   * name. `innerKind` routes it: tool.call / tool.result / message.delta /
   * turn.end (usage + context meter) / result (round finished, optional
   * cause). `text`/`detail` are daemon-clamped previews — plain text only.
   */
  | {
      type: "team_member";
      teamId: string;
      parentCallId: string;
      member: string;
      /** The member's child session id (`team-<teamId>-<member>`), when carried. */
      memberSessionId?: string;
      innerKind: string;
      text?: string;
      toolName?: string;
      detail?: string;
      isError: boolean;
      /** Per-event usage (turn.end / result) — the lane SUMS these. */
      inputTokens?: number;
      outputTokens?: number;
      /** Context meter (turn.end): current occupancy / window; 0 = unknown. */
      contextUsed: number;
      contextWindow: number;
      cause?: string;
    }
  /** The team's shared task board, a full snapshot (team.tasks). */
  | {
      type: "team_tasks";
      teamId: string;
      parentCallId: string;
      tasks: TeamTaskInfo[];
    }
  /** The team's findings ledger, a full snapshot (team.findings). */
  | {
      type: "team_findings";
      teamId: string;
      parentCallId: string;
      findings: TeamFindingInfo[];
    }
  /**
   * The team's terminal (team.end): rounds, stop, the team-total usage, the
   * final task/findings snapshots, and how each member ended.
   */
  | {
      type: "team_end";
      teamId: string;
      parentCallId: string;
      rounds: number;
      stop: string;
      inputTokens?: number;
      outputTokens?: number;
      tasks: TeamTaskInfo[];
      findings: TeamFindingInfo[];
      dispositions: TeamMemberDispositionInfo[];
    }
  /**
   * The run's terminal frame. `stop === "error"` is a FAILED turn and must
   * render as one, even when no token ever streamed. `retryDisposition` /
   * `streamProgress` are the typed failed-terminal classification (ADR 0239),
   * presence-aware: absent against a daemon that predates them.
   */
  | {
      type: "run_result";
      stop: string;
      text: string;
      errorText: string;
      permanent: boolean;
      retryDisposition?: RetryDisposition;
      streamProgress?: StreamProgress;
    };

/** Typed failed-terminal classification (ADR 0239). */
export type RetryDisposition = "unknown" | "retryable" | "permanent";
/** How far the failed step's stream got before it died (ADR 0239). */
export type StreamProgress = "unknown" | "precommit" | "visible" | "complete";

/** One committed media part off the steer drain echo (ADR 0251). */
export interface SteerEchoPart {
  kind: "image" | "audio";
  mimeType: string;
  /** Standard base64 (inline bytes); empty when url-sourced. */
  data?: string;
  url?: string;
}

// ── Projects ────────────────────────────────────────────────────────────────

export interface ProjectMemory {
  id: string;
  content: string;
  source: string;
}

export interface ProjectSuggestion {
  id: string;
  label: string;
}

export interface AgentProject {
  id: string;
  name: string;
  color: string;
  createdAt: number;
  summary?: string;
  status?: string;
  memories?: ProjectMemory[];
  suggestions?: ProjectSuggestion[];
  outputFiles?: Artifact[];
}

// ── Agents ──────────────────────────────────────────────────────────────────

export interface AgentRoster {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
}

// ── Approvals ───────────────────────────────────────────────────────────────

export type ApprovalChoice = "once" | "session" | "always" | "deny";

export interface ApprovalRequest {
  approvalId: string;
  sessionId: string;
  /** The tool being authorized ("" when the daemon did not name one). */
  toolName?: string;
  description: string;
  details: string;
  /** The daemon's reason for asking, on its own (details joins it with the
   *  flattened args). */
  reason?: string;
  /** The verbatim args JSON string the daemon sent — exactly what is being
   *  approved. Absent on asks built without the raw tier. */
  args?: string;
  /**
   * True when a CHILD (subagent / team member / parallel branch) is asking,
   * classified from the ask id against the daemon session id (the TUI
   * heuristic, `isChildAsk`). A child ask offers no "Always allow": a
   * persistent grant learned from a throwaway child would outlive it.
   */
  child?: boolean;
  /**
   * True on a developer-tools FAKE ask (`debug-ask.ts`, the TUI's
   * `/debug-ask`): minted locally to exercise the panel, never sent by the
   * daemon. Its verdict is short-circuited (no request), and a genuine ask
   * arriving meanwhile displaces it.
   */
  synthetic?: boolean;
}

// ── Clarifications ──────────────────────────────────────────────────────────

export interface ClarificationRequest {
  clarifyId: string;
  sessionId: string;
  question: string;
}

// ── MCP browser authorization ───────────────────────────────────────────────

/**
 * A pending browser authorization the chat is parked on: the MCP server named
 * by `displayName` needs the operator to sign in before the tool call
 * (`callId`) can run. The URL is never held here — it is fetched live from
 * the daemon's presentation control at the moment it is opened or copied.
 */
export interface AuthorizationRequest {
  authorizationId: string;
  sessionId: string;
  callId: string;
  displayName: string;
  /** Epoch milliseconds; absent when the daemon set no expiry. */
  expiresAt?: number;
  /** The last control failure, in the daemon's own words. */
  error?: string;
  /** A one-line status the last control produced (copied, still pending…). */
  notice?: string;
  /**
   * True once the sign-in page was opened or its link copied: Studio then
   * re-checks the authorization every 3 seconds until it resolves or the
   * operator cancels, so a completed sign-in resumes the run on its own.
   */
  polling?: boolean;
}

// ── Models ──────────────────────────────────────────────────────────────────

export interface ModelInfo {
  id: string;
  name: string;
  provider: string;
}

// ── Memory ──────────────────────────────────────────────────────────────────

export interface MemoryEntry {
  id: string;
  title: string;
  content: string;
  section: string;
  updatedAt: number;
}

// ── Skills ──────────────────────────────────────────────────────────────────

export interface Skill {
  name: string;
  description: string;
  category: string;
  content?: string;
}

// ── Cron ────────────────────────────────────────────────────────────────────

export interface CronRunRecord {
  id: string;
  startedAt: string;
  durationMs: number;
  status: "success" | "error" | "retrying";
  message: string;
}

export interface CronJob {
  id: string;
  name: string;
  schedule: string;
  instruction: string;
  enabled: boolean;
  status: "idle" | "running" | "error";
  lastRunAt: number | null;
  output: string | null;
  /** Live harness only: session id of the last completed fire. */
  lastRunSessionId?: string;
  prompt?: string;
  skills?: string[];
  tools?: string[];
  targetChannel?: string;
  history?: CronRunRecord[];
}

export interface CreateCronOpts {
  name: string;
  schedule: string;
  instruction: string;
  deliver?: string;
  skills?: string[];
  model?: string;
}

// ── Files ───────────────────────────────────────────────────────────────────

export interface FileEntry {
  name: string;
  path: string;
  type: "file" | "dir" | "symlink";
  size: number | null;
}

export interface FileContent {
  path: string;
  content: string;
  size: number;
  lines: number;
}

export interface GitInfo {
  isGit: boolean;
  branch: string | null;
  dirty: number;
  modified: number;
  untracked: number;
  ahead: number;
  behind: number;
}

// ── Artifacts (UI-level) ────────────────────────────────────────────────────

export interface Artifact {
  name: string;
  type: "spreadsheet" | "document" | "code" | "image" | "pdf" | "markdown";
  content?: string;
  /** Binary payloads (images, PDFs) that don't fit `content` as text ride a
   *  URL — a data: URI or a fetchable location. */
  url?: string;
  createdAt?: string;
}
