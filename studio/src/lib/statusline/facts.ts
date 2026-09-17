/**
 * The facts a user's status-line template may render — Studio's analogue of
 * mecatui's `status_customization` status input (`user-docs/mecatui/
 * status-line.md`): a small, display-safe allowlist of session, model,
 * context, usage, runtime and clock facts. Never prompts, transcript or tool
 * content, credentials, or a filesystem path (Studio rule 2: the workspace is
 * a display label the controller chose, not a placement input).
 *
 * `buildStatusFacts` is the one adapter from ChatView's props + the runtime
 * status to this shape, so the header and footer lanes and the Settings
 * preview all render from the same vocabulary.
 */

import { formatTokens } from "@/lib/formatters";
import { shortSessionHandle } from "@/lib/protocol/session-handle";

/** What the main agent is doing right now, as the lane sees it. */
export type StatusAgentState = "idle" | "streaming" | "awaiting";

export interface StatusFacts {
  /** The effective model id (`resolved_model.model_id`); "" when unknown. */
  model: string;
  /** The effective reasoning-effort tier; "" when the daemon echoes none. */
  effort: string;
  /** The controller's active provider name ("" when unknown). */
  provider: string;
  /** The downstream provider route of the current/last turn ("" = none). */
  route: string;
  sessionTitle: string;
  /** The documented 12-column session handle. */
  sessionHandle: string;
  /** Daemon lifecycle state (idle/running/awaiting/completed/failed/cancelled). */
  sessionState: string;
  /** The session's permission mode id (`default`/`plan`/`acceptEdits`); "". */
  mode: string;
  agentState: StatusAgentState;
  /** The latest turn's input tokens (turn.end); 0 before any turn this visit. */
  contextOccupancy: number;
  /** The meter's numerator: the occupancy, else cumulative input+output. */
  contextUsed: number;
  /** The resolved model's context window; <= 0 when unknown. */
  contextWindow: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  /** Messages queued behind the running turn. */
  queued: number;
  /** "managed" | "external" — how Studio reaches the daemon; "" while unknown. */
  server: string;
  /** The daemon's connection state as the runtime provider reports it. */
  connection: string;
  /** Operator-set deployment label ("" when unset). */
  deployment: string;
  /** The controller's workspace label — display only. */
  workspace: string;
  /** The daemon-reported effective posture tier ("" on an older daemon). */
  posture: string;
}

/** One row of the template reference: the placeholder key and what it shows. */
interface StatusFactRef {
  key: string;
  label: string;
  /** How the fact renders from `SAMPLE_STATUS_FACTS` (human formatting). */
  example: string;
}

/**
 * Every placeholder the template grammar accepts, in the order the Settings
 * page lists them. `context_meter` and `context_bar` are the two shipped
 * components; every other key is text. Add a key here AND in
 * `template.ts`'s `formatFact` — the reference table is what the chips and
 * the docs table render from.
 */
const STATUS_FACT_KEYS: readonly StatusFactRef[] = [
  { key: "model", label: "Effective model", example: "gpt-5.1" },
  { key: "effort", label: "Reasoning effort", example: "High" },
  { key: "provider", label: "Active provider", example: "openai" },
  { key: "route", label: "Downstream route", example: "azure" },
  {
    key: "session_title",
    label: "Session title",
    example: "Fix the flaky scheduler test",
  },
  { key: "session_handle", label: "Session handle", example: "session-fix" },
  { key: "session_state", label: "Session state", example: "running" },
  { key: "mode", label: "Permission mode", example: "Plan" },
  { key: "agent_state", label: "Agent state", example: "working" },
  { key: "context_percent", label: "Context in use (%)", example: "~42%" },
  { key: "context_used", label: "Context in use (tokens)", example: "84.0k" },
  { key: "context_window", label: "Context window", example: "200.0k" },
  { key: "input_tokens", label: "Input tokens (session)", example: "312.5k" },
  { key: "output_tokens", label: "Output tokens (session)", example: "18.2k" },
  { key: "total_tokens", label: "Input + output tokens", example: "330.7k" },
  { key: "cache_read_tokens", label: "Cache-read tokens", example: "150.0k" },
  { key: "cached_percent", label: "Cache hit rate", example: "48%" },
  { key: "queued", label: "Queued messages", example: "2" },
  { key: "server", label: "Daemon connection", example: "managed daemon" },
  { key: "deployment", label: "Deployment label", example: "staging-eu" },
  { key: "workspace", label: "Workspace label", example: "mecatl" },
  { key: "posture", label: "Operator posture", example: "trusted" },
  {
    key: "clock",
    label: "Time (HH:mm; use {{clock|HH:mm:ss}} for seconds)",
    example: "09:41",
  },
  { key: "date", label: "Date (YYYY-MM-DD)", example: "2026-01-05" },
  {
    key: "context_meter",
    label: "The shipped meter: model, bar, tokens",
    example: "(meter)",
  },
  {
    key: "context_bar",
    label: "The context bar with used / window · %",
    example: "(bar)",
  },
];

const KNOWN_KEYS: ReadonlySet<string> = new Set(
  STATUS_FACT_KEYS.map((fact) => fact.key),
);

export function isStatusFactKey(key: string): boolean {
  return KNOWN_KEYS.has(key);
}

/**
 * Context in use as an integer percentage of the window, or null when it
 * cannot be stated honestly: an unknown window (no made-up denominator) or
 * nothing counted yet ("0%" on a chat whose history the daemon still carries
 * would be a lie — the same rule the shipped meter follows).
 */
export function contextPercent(facts: StatusFacts): number | null {
  if (facts.contextUsed <= 0) return null;
  if (!Number.isFinite(facts.contextWindow) || facts.contextWindow <= 0) {
    return null;
  }
  const fraction = Math.min(1, facts.contextUsed / facts.contextWindow);
  return Math.round(fraction * 100);
}

/**
 * The context meter as text: `84.0k / 200.0k · 42%`, the bare `ctx 84.0k`
 * when the window is unknown, "" when nothing is counted. The text-only
 * rendering of `{{context_meter}}`/`{{context_bar}}` (previews, tooltips).
 */
export function contextMeterLabel(used: number, contextWindow: number): string {
  if (!Number.isFinite(used) || used <= 0) return "";
  if (!Number.isFinite(contextWindow) || contextWindow <= 0) {
    return `ctx ${formatTokens(used)}`;
  }
  const percent = Math.round(Math.min(1, used / contextWindow) * 100);
  return `${formatTokens(used)} / ${formatTokens(contextWindow)} · ${percent}%`;
}

/** The meter's numerator: the occupancy when a turn.end has been seen, else
 *  the cumulative input+output (the older-daemon fallback). */
function statusContextUsed(
  occupancy: number,
  inputTokens: number,
  outputTokens: number,
): number {
  if (Number.isFinite(occupancy) && occupancy > 0) return occupancy;
  return Math.max(0, inputTokens) + Math.max(0, outputTokens);
}

/** Realistic facts for the Settings preview and tests. */
export const SAMPLE_STATUS_FACTS: StatusFacts = {
  model: "gpt-5.1",
  effort: "high",
  provider: "openai",
  route: "azure",
  sessionTitle: "Fix the flaky scheduler test",
  sessionHandle: "session-fix",
  sessionState: "running",
  mode: "plan",
  agentState: "streaming",
  contextOccupancy: 84_000,
  contextUsed: 84_000,
  contextWindow: 200_000,
  inputTokens: 312_500,
  outputTokens: 18_200,
  cacheReadTokens: 150_000,
  cacheWriteTokens: 4_000,
  queued: 2,
  server: "managed",
  connection: "connected",
  deployment: "staging-eu",
  workspace: "mecatl",
  posture: "trusted",
};

/** What ChatView hands `buildStatusFacts` — its own props plus the runtime. */
export interface StatusFactsInput {
  session: { id: string; title?: string; state?: string };
  mode?: string;
  isStreaming: boolean;
  /** A permission ask is pending on this run. */
  awaitingApproval: boolean;
  contextInfo?: {
    modelLabel: string;
    contextWindow: number;
    effort?: string;
  } | null;
  contextOccupancy?: number;
  usage?: {
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens?: number;
    cacheWriteTokens?: number;
  } | null;
  queued: number;
  providerRoute?: string;
  runtime: {
    provider: string;
    mode: string;
    state: string;
    deployment: string;
    workspace: string;
    /** The daemon's capability document; `posture` is read when a string. */
    serverCapabilities: Record<string, unknown>;
  };
}

/** The single adapter from ChatView's props + runtime status to facts. */
export function buildStatusFacts(input: StatusFactsInput): StatusFacts {
  const inputTokens = Math.max(0, input.usage?.inputTokens ?? 0);
  const outputTokens = Math.max(0, input.usage?.outputTokens ?? 0);
  const occupancy = Math.max(0, input.contextOccupancy ?? 0);
  const posture = input.runtime.serverCapabilities.posture;
  return {
    model: input.contextInfo?.modelLabel ?? "",
    effort: input.contextInfo?.effort ?? "",
    provider: input.runtime.provider,
    route: input.providerRoute ?? "",
    sessionTitle: input.session.title ?? "",
    sessionHandle: shortSessionHandle(input.session.id),
    sessionState: input.session.state ?? "",
    mode: input.mode ?? "",
    agentState: input.awaitingApproval
      ? "awaiting"
      : input.isStreaming
        ? "streaming"
        : "idle",
    contextOccupancy: occupancy,
    contextUsed: statusContextUsed(occupancy, inputTokens, outputTokens),
    contextWindow: input.contextInfo?.contextWindow ?? 0,
    inputTokens,
    outputTokens,
    cacheReadTokens: Math.max(0, input.usage?.cacheReadTokens ?? 0),
    cacheWriteTokens: Math.max(0, input.usage?.cacheWriteTokens ?? 0),
    queued: Math.max(0, input.queued),
    server: input.runtime.mode,
    connection: input.runtime.state,
    deployment: input.runtime.deployment,
    workspace: input.runtime.workspace,
    posture: typeof posture === "string" ? posture : "",
  };
}
