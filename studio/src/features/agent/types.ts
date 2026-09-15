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
  renameReason?: string;
  deleteReason?: string;
  /**
   * Title provenance off the inventory row (F4): "operator" (hand-set — an
   * auto-rename must never clobber it), "first-prompt" (seeded, replaceable),
   * "" (unknown/older daemon — do not auto-rename).
   */
  titleProvenance?: string;
  /**
   * Non-empty on an AI-debug session (ADR 0254): the stored session this chat
   * diagnoses. Drives the sidebar's "debug" badge.
   */
  debugTargetSessionId?: string;
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
   * The turn ended with result.stop === "error". A failed turn renders as
   * failed — never as an empty success.
   */
  failed?: boolean;
  failureDetail?: string;
}

/**
 * One delegated child on an assistant turn. Starts as a badge (subagent.start
 * & friends), then live-updates while the child works — `subagent.tool`
 * frames tick `toolCount`/token counters — and settles on `subagent.end`
 * with the stop reason, duration, and (for a failed child) the cause, so a
 * failed child never silently vanishes. Keyed by `childId` where the daemon
 * names one; team/parallel entries may not carry an id and stay static.
 */
export interface DelegationInfo {
  kind: "subagent" | "team" | "parallel";
  label: string;
  detail: string;
  /** The child session id (`subagent.start` child_id) — the update key. */
  childId?: string;
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
  /** Terminal stop reason; presence means the child has ended. */
  stop?: string;
  /** Wall-clock child duration in milliseconds (end only, best-effort). */
  durationMs?: number;
  /** Failure detail when stop === "error" (harness metadata, clamped). */
  cause?: string;
}

export interface ToolCallInfo {
  callId: string;
  name: string;
  input: unknown;
  /** The file this call produced (Write), previewable in the canvas. */
  file?: import("@/lib/file-meta").ToolCallFile;
  output?: string;
  isError?: boolean;
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
      file?: import("@/lib/file-meta").ToolCallFile;
    }
  | {
      type: "tool_result";
      callId: string;
      output: string;
      isError?: boolean;
    }
  | {
      type: "approval";
      approvalId: string;
      sessionId: string;
      toolName: string;
      description: string;
      details: string;
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
  | { type: "title"; title: string }
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
  /** Delegation activity: the run handed work to a child agent. */
  | {
      type: "delegation";
      kind: "subagent" | "team" | "parallel";
      label: string;
      detail: string;
      /** Child session id (subagent.start), keying later live updates. */
      childId?: string;
      background?: boolean;
      /** Why the router did not route this delegation (D2.1). */
      routingReason?: string;
    }
  /**
   * Live child activity (subagent.tool): cumulative tool/token counters for
   * the delegation card keyed by `childId`. Redacted metadata only.
   */
  | {
      type: "delegation_progress";
      childId: string;
      toolCount?: number;
      inputTokens?: number;
      outputTokens?: number;
      toolName?: string;
    }
  /**
   * Child terminal (subagent.end): final counters, the stop reason, the
   * wall-clock duration, and — when stop === "error" — the failure cause,
   * so a failed child renders as failed instead of vanishing.
   */
  | {
      type: "delegation_end";
      childId: string;
      stop: string;
      toolCount?: number;
      inputTokens?: number;
      outputTokens?: number;
      durationMs?: number;
      cause?: string;
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
}

// ── Clarifications ──────────────────────────────────────────────────────────

export interface ClarificationRequest {
  clarifyId: string;
  sessionId: string;
  question: string;
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
