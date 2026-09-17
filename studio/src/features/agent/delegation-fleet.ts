/**
 * Pure delegation bookkeeping — no React. Two consumers share it:
 *
 * - the transcript: `applyDelegationEvent` lands every delegation StreamEvent
 *   on the assistant turn that owns it (its Subagent/Parallel/Team tool call),
 *   updating that turn's `delegations` cards and `delegationGroups` headers;
 * - the session-scoped fleet: `reduceDelegationFleet` aggregates every child a
 *   session ran across turns — a flat subagent roster, Parallel fan-out
 *   groups, and Team boards (lanes + task board + findings ledger) — the model
 *   behind the Agents panel.
 *
 * Both key a child the same way (`matchesDelegation`) and route its activity
 * through the same trace projection (`routeTraceEvent`), mirroring
 * cmd/mecatui/ui/conversation.go so the two clients can never disagree on
 * what a child did. Every function returns new objects on change and the
 * SAME reference when nothing changed.
 *
 * Text safety: `text`, `detail`, task `description`, and finding `body` are
 * bounded, model-influenced previews the daemon already clamped. Render them
 * as plain React text nodes only — never through markdown — and never persist
 * or forward them.
 */

import type {
  AgentMessage,
  DelegationGroupInfo,
  DelegationInfo,
  DelegationTraceEntry,
  StreamEvent,
  TeamFindingInfo,
  TeamMemberDispositionInfo,
  TeamTaskInfo,
} from "./types";

/**
 * Trace cap per lane, mirroring cmd/mecatui/ui/conversation.go
 * `maxTraceEntries` (12): the oldest entries drop once exceeded so a long
 * child investigation never unbounds a card.
 */
export const MAX_TRACE_ENTRIES = 12;

export const DELEGATION_EVENT_TYPES = [
  "delegation",
  "delegation_progress",
  "delegation_end",
  "parallel_start",
  "parallel_end",
  "team_member",
  "team_tasks",
  "team_findings",
  "team_end",
] as const;

export type DelegationEventType = (typeof DELEGATION_EVENT_TYPES)[number];
export type DelegationStreamEvent = Extract<
  StreamEvent,
  { type: DelegationEventType }
>;

type DelegationStart = Extract<StreamEvent, { type: "delegation" }>;
type DelegationProgress = Extract<StreamEvent, { type: "delegation_progress" }>;
type DelegationEnd = Extract<StreamEvent, { type: "delegation_end" }>;
type ParallelEnd = Extract<StreamEvent, { type: "parallel_end" }>;
type TeamMember = Extract<StreamEvent, { type: "team_member" }>;
type TeamEnd = Extract<StreamEvent, { type: "team_end" }>;

const DELEGATION_EVENT_TYPE_SET: ReadonlySet<string> = new Set(
  DELEGATION_EVENT_TYPES,
);

/** Type guard over the nine delegation StreamEvent variants. */
export function isDelegationEvent(
  event: StreamEvent,
): event is DelegationStreamEvent {
  return DELEGATION_EVENT_TYPE_SET.has(event.type);
}

// ── Keys ────────────────────────────────────────────────────────────────────

/** The handles an activity/terminal event carries to name its child. */
export interface DelegationRef {
  childId?: string;
  parentCallId?: string;
  teamId?: string;
  branchIndex?: number;
  /** Team member name (`team.member` tag). */
  member?: string;
}

/**
 * Whether `card` is the child `ref` names. Child-id equality when both sides
 * carry one; else a parallel branch by (parentCallId, branchIndex) — a
 * branch_tool carries no child id; else a team member by name within the
 * same call or team — a team lane has no session id until `team.member`
 * backfills it.
 */
export function matchesDelegation(
  card: DelegationInfo,
  ref: DelegationRef,
): boolean {
  if (card.childId && ref.childId) return card.childId === ref.childId;
  if (card.kind === "parallel" && ref.branchIndex !== undefined) {
    return (
      Boolean(ref.parentCallId) &&
      card.parentCallId === ref.parentCallId &&
      card.branchIndex === ref.branchIndex
    );
  }
  if (card.kind === "team" && ref.member) {
    if (card.memberName !== ref.member) return false;
    return (
      (Boolean(ref.parentCallId) && card.parentCallId === ref.parentCallId) ||
      (Boolean(ref.teamId) && card.teamId === ref.teamId)
    );
  }
  return false;
}

/** A stable identity string for a card (React keys, de-duplication). */
export function delegationKey(card: DelegationInfo): string {
  if (card.childId) return `child:${card.childId}`;
  if (card.kind === "parallel" && card.branchIndex !== undefined) {
    return `branch:${card.parentCallId ?? ""}:${card.branchIndex}`;
  }
  if (card.kind === "team" && card.memberName) {
    return `member:${card.parentCallId ?? card.teamId ?? ""}:${card.memberName}`;
  }
  return `${card.kind}:${card.label}`;
}

const refOf = (event: DelegationProgress | DelegationEnd): DelegationRef => ({
  childId: event.childId,
  parentCallId: event.parentCallId,
  branchIndex: event.branchIndex,
});

const startRefOf = (event: DelegationStart): DelegationRef => ({
  childId: event.childId,
  parentCallId: event.parentCallId,
  teamId: event.teamId,
  branchIndex: event.branchIndex,
  member: event.memberName,
});

const memberRefOf = (event: TeamMember): DelegationRef => ({
  childId: event.memberSessionId,
  parentCallId: event.parentCallId || undefined,
  teamId: event.teamId || undefined,
  member: event.member,
});

// ── Trace ───────────────────────────────────────────────────────────────────

/** Appends one entry, dropping the oldest past `MAX_TRACE_ENTRIES`. */
export function appendTrace(
  trace: DelegationTraceEntry[] | undefined,
  entry: DelegationTraceEntry,
): DelegationTraceEntry[] {
  const next = [...(trace ?? []), entry];
  return next.length > MAX_TRACE_ENTRIES
    ? next.slice(next.length - MAX_TRACE_ENTRIES)
    : next;
}

/** Streamed message fragments coalesce onto the trailing message line. */
function traceAppendMessage(
  trace: DelegationTraceEntry[] | undefined,
  text: string,
): DelegationTraceEntry[] | undefined {
  if (!text) return trace;
  const last = trace?.at(-1);
  if (trace && last && last.kind === "message") {
    return [...trace.slice(0, -1), { ...last, text: (last.text ?? "") + text }];
  }
  return appendTrace(trace, { kind: "message", text });
}

/**
 * Resolves the most recent chip for `name` (a pending one first), stamping
 * its error state and the result preview when one came; with no chip to
 * resolve it appends a resolved one, so a result is never silently lost.
 */
function traceMarkToolResult(
  trace: DelegationTraceEntry[] | undefined,
  name: string,
  detail: string,
  isError: boolean,
): DelegationTraceEntry[] {
  if (trace) {
    const candidates = [
      (entry: DelegationTraceEntry) =>
        entry.kind === "tool" && entry.name === name && entry.pending === true,
      (entry: DelegationTraceEntry) =>
        entry.kind === "tool" && entry.name === name,
    ];
    for (const matches of candidates) {
      for (let index = trace.length - 1; index >= 0; index -= 1) {
        const entry = trace[index];
        if (!matches(entry)) continue;
        const resolved: DelegationTraceEntry = {
          ...entry,
          isError: isError || undefined,
          pending: false,
          detail: detail || entry.detail,
        };
        return [...trace.slice(0, index), resolved, ...trace.slice(index + 1)];
      }
    }
  }
  return appendTrace(trace, {
    kind: "tool",
    name,
    detail: detail || undefined,
    isError: isError || undefined,
    pending: false,
  });
}

/** The bounded activity preview an activity frame carries. */
export interface DelegationActivity {
  innerKind?: string;
  toolName?: string;
  detail?: string;
  text?: string;
  isError?: boolean;
}

/**
 * Routes one activity frame onto a card's trace by its inner kind — the
 * shared projection every family uses (subagent, parallel branch, team
 * member): a tool.call sets the live current tool and appends a pending chip
 * with its arg preview; a tool.result resolves that chip (error state, result
 * preview) and records `lastToolError`; a message.delta/result extends the
 * trailing message line. A kind-less frame with a tool name (an older daemon)
 * reads as a tool.call.
 */
export function routeTraceEvent(
  card: DelegationInfo,
  activity: DelegationActivity,
): DelegationInfo {
  const innerKind = activity.innerKind ?? "";
  const toolName = activity.toolName ?? "";
  const detail = activity.detail ?? "";
  const isError = activity.isError === true;
  switch (innerKind) {
    case "message.delta":
    case "result": {
      const trace = traceAppendMessage(card.trace, activity.text ?? "");
      return trace === card.trace ? card : { ...card, trace };
    }
    case "tool.result":
      if (!toolName) return card;
      return {
        ...card,
        trace: traceMarkToolResult(card.trace, toolName, detail, isError),
        lastTool: toolName,
        lastToolError: isError,
      };
    default:
      if (!toolName) return card;
      return {
        ...card,
        trace: appendTrace(card.trace, {
          kind: "tool",
          name: toolName,
          detail: detail || undefined,
          isError: isError || undefined,
          pending: true,
        }),
        lastTool: toolName,
        lastToolError: undefined,
      };
  }
}

// ── Cards ───────────────────────────────────────────────────────────────────

/** The fields a `delegation` start event defines (undefined ones omitted). */
function startFields(event: DelegationStart): DelegationInfo {
  const card: DelegationInfo = {
    kind: event.kind,
    label: event.label,
    detail: event.detail,
  };
  if (event.childId) card.childId = event.childId;
  if (event.parentCallId) card.parentCallId = event.parentCallId;
  if (event.teamId) card.teamId = event.teamId;
  if (event.memberName) card.memberName = event.memberName;
  if (event.branchIndex !== undefined) card.branchIndex = event.branchIndex;
  if (event.lead) card.lead = true;
  if (event.mutating) card.mutating = true;
  if (event.model) card.model = event.model;
  if (event.background) card.background = true;
  if (event.routingReason) card.routingReason = event.routingReason;
  return card;
}

/** A fresh card for a start event. */
export function cardFromDelegation(event: DelegationStart): DelegationInfo {
  return startFields(event);
}

/**
 * A start for a card that already exists (a resumed child, a replayed
 * roster): refresh the start facts and clear the terminal — it runs again.
 */
function restartCard(
  existing: DelegationInfo,
  event: DelegationStart,
): DelegationInfo {
  const {
    stop: _stop,
    durationMs: _durationMs,
    cause: _cause,
    failed: _failed,
    cancelling: _cancelling,
    ...kept
  } = existing;
  return { ...kept, ...startFields(event) };
}

/** A lane backfilled from an activity frame whose start this visit never saw. */
function backfilledCard(ref: DelegationRef): DelegationInfo | undefined {
  if (ref.branchIndex !== undefined && ref.parentCallId) {
    const card: DelegationInfo = {
      kind: "parallel",
      label: `branch ${ref.branchIndex + 1}`,
      detail: "",
      parentCallId: ref.parentCallId,
      branchIndex: ref.branchIndex,
    };
    if (ref.childId) card.childId = ref.childId;
    return card;
  }
  if (ref.childId) {
    const card: DelegationInfo = {
      kind: "subagent",
      label: ref.childId,
      detail: "",
      childId: ref.childId,
    };
    if (ref.parentCallId) card.parentCallId = ref.parentCallId;
    return card;
  }
  return undefined;
}

/** A team lane backfilled from a `team.member` frame (team.start missed). */
function backfilledLane(event: TeamMember): DelegationInfo {
  const card: DelegationInfo = {
    kind: "team",
    label: event.member,
    detail: "",
    memberName: event.member,
  };
  if (event.parentCallId) card.parentCallId = event.parentCallId;
  if (event.teamId) card.teamId = event.teamId;
  return card;
}

/** Live counters + activity preview onto a card (subagent.tool / branch_tool). */
export function applyProgressToCard(
  card: DelegationInfo,
  event: DelegationProgress,
): DelegationInfo {
  const routed = routeTraceEvent(card, event);
  return {
    ...routed,
    parentCallId: routed.parentCallId ?? event.parentCallId,
    toolCount: event.toolCount ?? routed.toolCount,
    inputTokens: event.inputTokens ?? routed.inputTokens,
    outputTokens: event.outputTokens ?? routed.outputTokens,
  };
}

/** The child terminal onto a card (subagent.end / branch_end). */
export function applyEndToCard(
  card: DelegationInfo,
  event: DelegationEnd,
): DelegationInfo {
  const next: DelegationInfo = {
    ...card,
    childId: card.childId ?? event.childId,
    parentCallId: card.parentCallId ?? event.parentCallId,
    toolCount: event.toolCount ?? card.toolCount,
    inputTokens: event.inputTokens ?? card.inputTokens,
    outputTokens: event.outputTokens ?? card.outputTokens,
    stop: event.stop || "end_turn",
    durationMs: event.durationMs,
    cause: event.cause,
  };
  if (event.failed) next.failed = true;
  delete next.cancelling;
  return next;
}

const sumTokens = (current: number | undefined, delta: number | undefined) =>
  delta === undefined ? current : (current ?? 0) + delta;

/**
 * One `team.member` frame onto its lane, per inner kind (mirroring the TUI's
 * addTeamMember): message.delta / tool.call clear `idle` and extend the
 * trace; tool.result resolves the chip; turn.end SUMS the per-turn usage and
 * sets the context meter (window sticky — only a positive value overwrites);
 * result marks the lane idle (round finished, not terminal), sums its usage,
 * lands its text, and keeps the last non-empty cause. The member's session
 * id and the team id backfill from any frame carrying them.
 */
export function applyTeamMemberToCard(
  card: DelegationInfo,
  event: TeamMember,
): DelegationInfo {
  const next: DelegationInfo = { ...card };
  if (!next.childId && event.memberSessionId) {
    next.childId = event.memberSessionId;
  }
  if (!next.teamId && event.teamId) next.teamId = event.teamId;
  if (!next.parentCallId && event.parentCallId) {
    next.parentCallId = event.parentCallId;
  }
  switch (event.innerKind) {
    case "message.delta":
      return routeTraceEvent({ ...next, idle: false }, event);
    case "tool.call": {
      const routed = routeTraceEvent({ ...next, idle: false }, event);
      // team.member carries no cumulative count; each call is one more.
      return { ...routed, toolCount: (routed.toolCount ?? 0) + 1 };
    }
    case "tool.result":
      return routeTraceEvent(next, event);
    case "turn.end": {
      const turn: DelegationInfo = {
        ...next,
        idle: false,
        inputTokens: sumTokens(next.inputTokens, event.inputTokens),
        outputTokens: sumTokens(next.outputTokens, event.outputTokens),
        // Current occupancy, not cumulative cost: the most recent turn's
        // input tokens.
        contextUsed:
          event.contextUsed > 0 ? event.contextUsed : (event.inputTokens ?? 0),
      };
      if (event.contextWindow > 0) turn.contextWindow = event.contextWindow;
      return turn;
    }
    case "result": {
      let done: DelegationInfo = {
        ...next,
        idle: true,
        inputTokens: sumTokens(next.inputTokens, event.inputTokens),
        outputTokens: sumTokens(next.outputTokens, event.outputTokens),
      };
      delete done.lastTool;
      if (event.text) {
        done = routeTraceEvent(done, { innerKind: "result", text: event.text });
      }
      if (event.cause) done.cause = event.cause;
      return done;
    }
    default:
      return next;
  }
}

/**
 * How a team member ended (team.end): the disposition plus a `stop` so the
 * lane resolves — a benched member stops on its reason, the rest on the
 * team's own stop.
 */
export function applyDispositionToCard(
  card: DelegationInfo,
  disposition: TeamMemberDispositionInfo | undefined,
  teamStop: string,
): DelegationInfo {
  const next: DelegationInfo = {
    ...card,
    idle: true,
    stop: disposition?.stopped
      ? disposition.reason || "stopped"
      : teamStop || "end_turn",
  };
  if (disposition) {
    next.stopped = disposition.stopped;
    next.stopReason = disposition.reason;
    next.errorRounds = disposition.errorRounds;
  }
  delete next.cancelling;
  return next;
}

/** Lead lanes first; a stable sort keeps arrival order among peers. */
const leadFirst = (lanes: DelegationInfo[]): DelegationInfo[] =>
  [...lanes].sort((a, b) => Number(Boolean(b.lead)) - Number(Boolean(a.lead)));

const byBranchIndex = (branches: DelegationInfo[]): DelegationInfo[] =>
  [...branches].sort(
    (a, b) =>
      (a.branchIndex ?? Number.MAX_SAFE_INTEGER) -
      (b.branchIndex ?? Number.MAX_SAFE_INTEGER),
  );

const replaceAt = <T>(list: T[], index: number, item: T): T[] => [
  ...list.slice(0, index),
  item,
  ...list.slice(index + 1),
];

/** Inserts or replaces the card `ref` names; `sort` orders the result. */
function upsertCard(
  cards: DelegationInfo[],
  ref: DelegationRef,
  make: (existing: DelegationInfo | undefined) => DelegationInfo,
  sort: (cards: DelegationInfo[]) => DelegationInfo[] = (list) => list,
): DelegationInfo[] {
  const index = cards.findIndex((card) => matchesDelegation(card, ref));
  if (index === -1) return sort([...cards, make(undefined)]);
  return sort(replaceAt(cards, index, make(cards[index])));
}

// ── Transcript ──────────────────────────────────────────────────────────────

/** Where a delegation event lands when no turn already owns its call. */
export interface DelegationAnchor {
  /** The assistant bubble this tab's run streams into (the prompt path). */
  assistantId?: string;
  /** Opens a fresh assistant bubble when nothing else anchors the event. */
  openAssistant?: () => AgentMessage;
}

/**
 * The turn that owns a delegation call: the message whose group header, a
 * card, or a tool call already carries `parentCallId` (or a team card with
 * `teamId`), searched backwards — a background child's end can arrive after
 * a later assistant turn has started.
 */
function findHost(
  messages: AgentMessage[],
  ref: Pick<DelegationRef, "parentCallId" | "teamId">,
): number {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (ref.parentCallId) {
      if (message.delegationGroups?.[ref.parentCallId]) return index;
      if (
        message.delegations?.some(
          (card) => card.parentCallId === ref.parentCallId,
        )
      ) {
        return index;
      }
      if (message.toolCalls?.some((call) => call.callId === ref.parentCallId)) {
        return index;
      }
    }
    if (
      ref.teamId &&
      message.delegations?.some(
        (card) => card.kind === "team" && card.teamId === ref.teamId,
      )
    ) {
      return index;
    }
  }
  return -1;
}

/** The message that holds a card matching `ref`, plus the card's index. */
function findCard(
  messages: AgentMessage[],
  ref: DelegationRef,
): { index: number; cardIndex: number } | undefined {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const cardIndex =
      messages[index].delegations?.findIndex((card) =>
        matchesDelegation(card, ref),
      ) ?? -1;
    if (cardIndex !== -1) return { index, cardIndex };
  }
  return undefined;
}

/**
 * A host for a START-like event with no owning turn yet: the anchored
 * assistant bubble, else the trailing assistant, else a freshly opened one.
 * Returns the (possibly extended) list and the host index, or -1.
 */
function fallbackHost(
  messages: AgentMessage[],
  anchor: DelegationAnchor,
): { messages: AgentMessage[]; index: number } {
  if (anchor.assistantId) {
    const index = messages.findIndex(
      (message) => message.id === anchor.assistantId,
    );
    if (index !== -1) return { messages, index };
  }
  const last = messages.at(-1);
  if (last?.role === "assistant") {
    return { messages, index: messages.length - 1 };
  }
  if (anchor.openAssistant) {
    return {
      messages: [...messages, anchor.openAssistant()],
      index: messages.length,
    };
  }
  return { messages, index: -1 };
}

function withGroup(
  message: AgentMessage,
  parentCallId: string,
  update: (group: DelegationGroupInfo | undefined) => DelegationGroupInfo,
): AgentMessage {
  return {
    ...message,
    delegationGroups: {
      ...message.delegationGroups,
      [parentCallId]: update(message.delegationGroups?.[parentCallId]),
    },
  };
}

/** Ensures the group header a start card implies (team has no group event). */
function ensureGroupForCard(
  message: AgentMessage,
  card: DelegationInfo,
): AgentMessage {
  if (!card.parentCallId || card.kind === "subagent") return message;
  const kind = card.kind;
  return withGroup(message, card.parentCallId, (group) => ({
    ...group,
    kind,
    ...(card.teamId ? { teamId: card.teamId } : {}),
  }));
}

/**
 * Applies one delegation StreamEvent to the transcript. Pure: a new array on
 * change, the SAME array when nothing matched (an activity frame whose child
 * has no card and no owning turn, a task/findings snapshot — panel-only).
 *
 * A start lands on the turn whose tool call it belongs to (else the anchor);
 * a repeated start for a known child refreshes it in place. Progress and
 * terminals find their card by `matchesDelegation` anywhere in the transcript;
 * when the owning turn is known but the start was never seen, the lane is
 * backfilled there. Team members create/refresh the team's group header on
 * the first card carrying its call id — team.start has no group event.
 */
export function applyDelegationEvent(
  messages: AgentMessage[],
  event: DelegationStreamEvent,
  anchor: DelegationAnchor = {},
): AgentMessage[] {
  switch (event.type) {
    case "delegation": {
      const ref = startRefOf(event);
      const hit = findCard(messages, ref);
      if (hit) {
        const message = messages[hit.index];
        const cards = message.delegations ?? [];
        const card = restartCard(cards[hit.cardIndex], event);
        return replaceAt(
          messages,
          hit.index,
          ensureGroupForCard(
            { ...message, delegations: replaceAt(cards, hit.cardIndex, card) },
            card,
          ),
        );
      }
      let index = findHost(messages, ref);
      let list = messages;
      if (index === -1) {
        ({ messages: list, index } = fallbackHost(messages, anchor));
        if (index === -1) return messages;
      }
      const card = cardFromDelegation(event);
      const message = list[index];
      return replaceAt(
        list,
        index,
        ensureGroupForCard(
          {
            ...message,
            delegations: [...(message.delegations ?? []), card],
          },
          card,
        ),
      );
    }
    case "delegation_progress":
    case "delegation_end": {
      const ref = refOf(event);
      const apply = (card: DelegationInfo) =>
        event.type === "delegation_progress"
          ? applyProgressToCard(card, event)
          : applyEndToCard(card, event);
      const hit = findCard(messages, ref);
      if (hit) {
        const message = messages[hit.index];
        const cards = message.delegations ?? [];
        return replaceAt(messages, hit.index, {
          ...message,
          delegations: replaceAt(
            cards,
            hit.cardIndex,
            apply(cards[hit.cardIndex]),
          ),
        });
      }
      // Backfill only where the owning turn is known.
      const index = findHost(messages, ref);
      const seed = backfilledCard(ref);
      if (index === -1 || !seed) return messages;
      const message = messages[index];
      return replaceAt(
        messages,
        index,
        ensureGroupForCard(
          {
            ...message,
            delegations: [...(message.delegations ?? []), apply(seed)],
          },
          seed,
        ),
      );
    }
    case "parallel_start": {
      let index = findHost(messages, { parentCallId: event.parentCallId });
      let list = messages;
      if (index === -1) {
        ({ messages: list, index } = fallbackHost(messages, anchor));
        if (index === -1) return messages;
      }
      return replaceAt(
        list,
        index,
        withGroup(list[index], event.parentCallId, (group) => ({
          ...group,
          kind: "parallel",
          join: event.join,
          branchCount: event.branchCount || group?.branchCount,
        })),
      );
    }
    case "parallel_end": {
      const index = findHost(messages, { parentCallId: event.parentCallId });
      if (index === -1) return messages;
      const message = withGroup(
        messages[index],
        event.parentCallId,
        (group) => ({
          ...group,
          kind: "parallel",
          join: event.join || group?.join,
          branchCount: event.branchCount || group?.branchCount,
          winner: event.winner,
          stop: event.stop,
          done: true,
          inputTokens: event.inputTokens ?? group?.inputTokens,
          outputTokens: event.outputTokens ?? group?.outputTokens,
        }),
      );
      const delegations =
        event.winner >= 0
          ? message.delegations?.map((card) =>
              card.kind === "parallel" &&
              card.parentCallId === event.parentCallId &&
              card.branchIndex === event.winner
                ? { ...card, winner: true }
                : card,
            )
          : message.delegations;
      return replaceAt(messages, index, { ...message, delegations });
    }
    case "team_member": {
      const ref = memberRefOf(event);
      let index = findHost(messages, ref);
      let list = messages;
      if (index === -1) {
        ({ messages: list, index } = fallbackHost(messages, anchor));
        if (index === -1) return messages;
      }
      const message = list[index];
      const cards = upsertCard(
        message.delegations ?? [],
        ref,
        (existing) =>
          applyTeamMemberToCard(existing ?? backfilledLane(event), event),
        leadFirst,
      );
      const seed = backfilledLane(event);
      return replaceAt(
        list,
        index,
        ensureGroupForCard({ ...message, delegations: cards }, seed),
      );
    }
    case "team_end": {
      const ref = {
        parentCallId: event.parentCallId || undefined,
        teamId: event.teamId || undefined,
      };
      const index = findHost(messages, ref);
      if (index === -1) return messages;
      let message = messages[index];
      const isLane = (card: DelegationInfo) =>
        card.kind === "team" &&
        ((ref.parentCallId && card.parentCallId === ref.parentCallId) ||
          (ref.teamId && card.teamId === ref.teamId));
      const byName = new Map(event.dispositions.map((d) => [d.name, d]));
      let cards = (message.delegations ?? []).map((card) =>
        isLane(card)
          ? applyDispositionToCard(
              card,
              card.memberName ? byName.get(card.memberName) : undefined,
              event.stop,
            )
          : card,
      );
      // A benched member the roster never showed still gets its lane.
      for (const disposition of event.dispositions) {
        if (
          cards.some(
            (card) => isLane(card) && card.memberName === disposition.name,
          )
        ) {
          continue;
        }
        const lane: DelegationInfo = {
          kind: "team",
          label: disposition.name,
          detail: "",
          memberName: disposition.name,
        };
        if (ref.parentCallId) lane.parentCallId = ref.parentCallId;
        if (ref.teamId) lane.teamId = ref.teamId;
        cards = [
          ...cards,
          applyDispositionToCard(lane, disposition, event.stop),
        ];
      }
      message = { ...message, delegations: leadFirst(cards) };
      if (ref.parentCallId) {
        message = withGroup(message, ref.parentCallId, (group) => ({
          ...group,
          kind: "team",
          teamId: ref.teamId ?? group?.teamId,
          done: true,
          rounds: event.rounds,
          stop: event.stop,
          inputTokens: event.inputTokens ?? group?.inputTokens,
          outputTokens: event.outputTokens ?? group?.outputTokens,
          stoppedCount: event.dispositions.filter((d) => d.stopped).length,
        }));
      }
      return replaceAt(messages, index, message);
    }
    case "team_tasks":
    case "team_findings":
      // Panel-only snapshots: the transcript card row never renders them.
      return messages;
    default:
      return messages;
  }
}

/** Marks/clears an optimistic cancel on the card for `childId`. */
export function markCardCancelling(
  messages: AgentMessage[],
  childId: string,
  on: boolean,
): AgentMessage[] {
  const hit = findCard(messages, { childId });
  if (!hit) return messages;
  const message = messages[hit.index];
  const cards = message.delegations ?? [];
  const card = cards[hit.cardIndex];
  if (Boolean(card.cancelling) === on) return messages;
  const next: DelegationInfo = { ...card };
  if (on) next.cancelling = true;
  else delete next.cancelling;
  return replaceAt(messages, hit.index, {
    ...message,
    delegations: replaceAt(cards, hit.cardIndex, next),
  });
}

// ── Fleet ───────────────────────────────────────────────────────────────────

/** One Parallel fan-out: the group facts plus its branches by index. */
export interface ParallelGroupState {
  parentCallId: string;
  join: string;
  branchCount: number;
  /** The winning branch index; −1 = none / join=all. */
  winner: number;
  stop: string;
  done: boolean;
  branches: DelegationInfo[];
  inputTokens?: number;
  outputTokens?: number;
}

/** One Team: its lanes (lead first), shared task board, and findings ledger. */
export interface TeamBoardState {
  parentCallId: string;
  teamId: string;
  lanes: DelegationInfo[];
  tasks: TeamTaskInfo[];
  findings: TeamFindingInfo[];
  done: boolean;
  rounds: number;
  stop: string;
  inputTokens?: number;
  outputTokens?: number;
}

/**
 * Every child a session ran, aggregated across turns. Per-visit (like
 * `usage`): the transcript rehydrate carries no delegation data, so the
 * fleet holds only what this tab observed live or via the durable watch's
 * replay.
 */
export interface DelegationFleet {
  subagents: DelegationInfo[];
  parallelGroups: ParallelGroupState[];
  teams: TeamBoardState[];
}

export function emptyFleet(): DelegationFleet {
  return { subagents: [], parallelGroups: [], teams: [] };
}

const newGroup = (parentCallId: string): ParallelGroupState => ({
  parentCallId,
  join: "",
  branchCount: 0,
  winner: -1,
  stop: "",
  done: false,
  branches: [],
});

const newTeam = (parentCallId: string, teamId: string): TeamBoardState => ({
  parentCallId,
  teamId,
  lanes: [],
  tasks: [],
  findings: [],
  done: false,
  rounds: 0,
  stop: "",
});

/** Finds or creates the group for `parentCallId` and applies `update`. */
function withFleetGroup(
  fleet: DelegationFleet,
  parentCallId: string,
  update: (group: ParallelGroupState) => ParallelGroupState,
): DelegationFleet {
  const index = fleet.parallelGroups.findIndex(
    (group) => group.parentCallId === parentCallId,
  );
  const current =
    index === -1 ? newGroup(parentCallId) : fleet.parallelGroups[index];
  const next = update(current);
  if (next === current && index !== -1) return fleet;
  return {
    ...fleet,
    parallelGroups:
      index === -1
        ? [...fleet.parallelGroups, next]
        : replaceAt(fleet.parallelGroups, index, next),
  };
}

const findTeamIndex = (
  fleet: DelegationFleet,
  ref: Pick<DelegationRef, "parentCallId" | "teamId">,
): number =>
  fleet.teams.findIndex(
    (team) =>
      (Boolean(ref.parentCallId) && team.parentCallId === ref.parentCallId) ||
      (Boolean(ref.teamId) && team.teamId === ref.teamId),
  );

/** Finds or creates the team `ref` names (backfilling its ids) and applies `update`. */
function withFleetTeam(
  fleet: DelegationFleet,
  ref: Pick<DelegationRef, "parentCallId" | "teamId">,
  update: (team: TeamBoardState) => TeamBoardState,
): DelegationFleet {
  const index = findTeamIndex(fleet, ref);
  let current =
    index === -1
      ? newTeam(ref.parentCallId ?? "", ref.teamId ?? "")
      : fleet.teams[index];
  if (index !== -1) {
    if (!current.parentCallId && ref.parentCallId) {
      current = { ...current, parentCallId: ref.parentCallId };
    }
    if (!current.teamId && ref.teamId) {
      current = { ...current, teamId: ref.teamId };
    }
  }
  const next = update(current);
  if (index !== -1 && next === fleet.teams[index]) return fleet;
  return {
    ...fleet,
    teams:
      index === -1
        ? [...fleet.teams, next]
        : replaceAt(fleet.teams, index, next),
  };
}

/** Applies `apply` to the first lane (subagent, then branch) matching `ref`. */
function updateFleetLane(
  fleet: DelegationFleet,
  ref: DelegationRef,
  apply: (card: DelegationInfo) => DelegationInfo,
): DelegationFleet | undefined {
  const subIndex = fleet.subagents.findIndex((card) =>
    matchesDelegation(card, ref),
  );
  if (subIndex !== -1) {
    return {
      ...fleet,
      subagents: replaceAt(
        fleet.subagents,
        subIndex,
        apply(fleet.subagents[subIndex]),
      ),
    };
  }
  for (const [groupIndex, group] of fleet.parallelGroups.entries()) {
    const branchIndex = group.branches.findIndex((card) =>
      matchesDelegation(card, ref),
    );
    if (branchIndex === -1) continue;
    return {
      ...fleet,
      parallelGroups: replaceAt(fleet.parallelGroups, groupIndex, {
        ...group,
        branches: replaceAt(
          group.branches,
          branchIndex,
          apply(group.branches[branchIndex]),
        ),
      }),
    };
  }
  return undefined;
}

/**
 * Lands an activity/terminal frame on its lane, backfilling the lane when
 * its start was never seen (the TUI's rule): a branch under its group by
 * (parentCallId, branchIndex); a subagent by child id.
 */
function reduceLaneActivity(
  fleet: DelegationFleet,
  ref: DelegationRef,
  apply: (card: DelegationInfo) => DelegationInfo,
): DelegationFleet {
  const updated = updateFleetLane(fleet, ref, apply);
  if (updated) return updated;
  const seed = backfilledCard(ref);
  if (!seed) return fleet;
  if (seed.kind === "parallel" && seed.parentCallId) {
    return withFleetGroup(fleet, seed.parentCallId, (group) => ({
      ...group,
      branches: byBranchIndex([...group.branches, apply(seed)]),
    }));
  }
  return { ...fleet, subagents: [...fleet.subagents, apply(seed)] };
}

function reduceStart(
  fleet: DelegationFleet,
  event: DelegationStart,
): DelegationFleet {
  const ref = startRefOf(event);
  const make = (existing: DelegationInfo | undefined) =>
    existing ? restartCard(existing, event) : cardFromDelegation(event);
  switch (event.kind) {
    case "subagent":
      return { ...fleet, subagents: upsertCard(fleet.subagents, ref, make) };
    case "parallel":
      return withFleetGroup(fleet, event.parentCallId ?? "", (group) => ({
        ...group,
        branches: upsertCard(group.branches, ref, make, byBranchIndex),
      }));
    case "team":
      return withFleetTeam(fleet, ref, (team) => ({
        ...team,
        lanes: upsertCard(team.lanes, ref, make, leadFirst),
      }));
    default:
      return fleet;
  }
}

function reduceTeamMember(
  fleet: DelegationFleet,
  event: TeamMember,
): DelegationFleet {
  const ref = memberRefOf(event);
  return withFleetTeam(fleet, ref, (team) => ({
    ...team,
    lanes: upsertCard(
      team.lanes,
      ref,
      (existing) =>
        applyTeamMemberToCard(existing ?? backfilledLane(event), event),
      leadFirst,
    ),
  }));
}

function reduceTeamEnd(
  fleet: DelegationFleet,
  event: TeamEnd,
): DelegationFleet {
  const ref = {
    parentCallId: event.parentCallId || undefined,
    teamId: event.teamId || undefined,
  };
  return withFleetTeam(fleet, ref, (team) => {
    const byName = new Map(event.dispositions.map((d) => [d.name, d]));
    let lanes = team.lanes.map((lane) =>
      applyDispositionToCard(
        lane,
        lane.memberName ? byName.get(lane.memberName) : undefined,
        event.stop,
      ),
    );
    for (const disposition of event.dispositions) {
      if (lanes.some((lane) => lane.memberName === disposition.name)) continue;
      const lane: DelegationInfo = {
        kind: "team",
        label: disposition.name,
        detail: "",
        memberName: disposition.name,
        parentCallId: team.parentCallId || undefined,
        teamId: team.teamId || undefined,
      };
      lanes = [...lanes, applyDispositionToCard(lane, disposition, event.stop)];
    }
    return {
      ...team,
      lanes: leadFirst(lanes),
      done: true,
      rounds: event.rounds,
      stop: event.stop,
      tasks: event.tasks.length ? event.tasks : team.tasks,
      findings: event.findings.length ? event.findings : team.findings,
      inputTokens: event.inputTokens ?? team.inputTokens,
      outputTokens: event.outputTokens ?? team.outputTokens,
    };
  });
}

function reduceParallelEnd(
  fleet: DelegationFleet,
  event: ParallelEnd,
): DelegationFleet {
  return withFleetGroup(fleet, event.parentCallId, (group) => ({
    ...group,
    join: event.join || group.join,
    branchCount: event.branchCount || group.branchCount,
    winner: event.winner,
    stop: event.stop,
    done: true,
    inputTokens: event.inputTokens ?? group.inputTokens,
    outputTokens: event.outputTokens ?? group.outputTokens,
    branches:
      event.winner >= 0
        ? group.branches.map((branch) =>
            branch.branchIndex === event.winner
              ? { ...branch, winner: true }
              : branch,
          )
        : group.branches,
  }));
}

/**
 * Folds one StreamEvent into the fleet. Returns the SAME fleet for a
 * non-delegation event (and for a task/findings snapshot of a team it then
 * creates only if needed), a new fleet otherwise.
 */
export function reduceDelegationFleet(
  fleet: DelegationFleet,
  event: StreamEvent,
): DelegationFleet {
  if (!isDelegationEvent(event)) return fleet;
  switch (event.type) {
    case "delegation":
      return reduceStart(fleet, event);
    case "delegation_progress":
      return reduceLaneActivity(fleet, refOf(event), (card) =>
        applyProgressToCard(card, event),
      );
    case "delegation_end":
      return reduceLaneActivity(fleet, refOf(event), (card) =>
        applyEndToCard(card, event),
      );
    case "parallel_start":
      return withFleetGroup(fleet, event.parentCallId, (group) => ({
        ...group,
        join: event.join,
        branchCount: event.branchCount || group.branchCount,
      }));
    case "parallel_end":
      return reduceParallelEnd(fleet, event);
    case "team_member":
      return reduceTeamMember(fleet, event);
    case "team_tasks":
      return withFleetTeam(
        fleet,
        {
          parentCallId: event.parentCallId || undefined,
          teamId: event.teamId || undefined,
        },
        (team) => ({ ...team, tasks: event.tasks }),
      );
    case "team_findings":
      return withFleetTeam(
        fleet,
        {
          parentCallId: event.parentCallId || undefined,
          teamId: event.teamId || undefined,
        },
        (team) => ({ ...team, findings: event.findings }),
      );
    case "team_end":
      return reduceTeamEnd(fleet, event);
    default:
      return fleet;
  }
}

/** The footer/panel tallies for each delegation family. */
export interface FleetCounts {
  /** A subagent runs until its end frame, background or not. */
  subagents: { running: number; done: number };
  /** A fan-out group is done once its parallel_end arrived. */
  parallel: { running: number; done: number };
  /**
   * The latest team: `live` until its team_end; `working` = lanes not idle
   * (and not benched) while live, 0 once done; `total` = its lane count.
   */
  team: { live: boolean; working: number; total: number; teamId: string };
}

export function fleetCounts(fleet: DelegationFleet): FleetCounts {
  const subagentsDone = fleet.subagents.filter(
    (card) => card.stop !== undefined,
  ).length;
  const parallelDone = fleet.parallelGroups.filter(
    (group) => group.done,
  ).length;
  const team =
    fleet.teams.findLast((board) => !board.done) ?? fleet.teams.at(-1);
  const live = Boolean(team && !team.done);
  return {
    subagents: {
      running: fleet.subagents.length - subagentsDone,
      done: subagentsDone,
    },
    parallel: {
      running: fleet.parallelGroups.length - parallelDone,
      done: parallelDone,
    },
    team: {
      live,
      working: live
        ? (team?.lanes.filter((lane) => !lane.idle && !lane.stopped).length ??
          0)
        : 0,
      total: team?.lanes.length ?? 0,
      teamId: team?.teamId ?? "",
    },
  };
}

/** Marks/clears an optimistic cancel on every fleet lane for `childId`. */
export function markCancelling(
  fleet: DelegationFleet,
  childId: string,
  on: boolean,
): DelegationFleet {
  let changed = false;
  const mark = (card: DelegationInfo): DelegationInfo => {
    if (card.childId !== childId || Boolean(card.cancelling) === on) {
      return card;
    }
    changed = true;
    const next: DelegationInfo = { ...card };
    if (on) next.cancelling = true;
    else delete next.cancelling;
    return next;
  };
  const next: DelegationFleet = {
    subagents: fleet.subagents.map(mark),
    parallelGroups: fleet.parallelGroups.map((group) => ({
      ...group,
      branches: group.branches.map(mark),
    })),
    teams: fleet.teams.map((team) => ({
      ...team,
      lanes: team.lanes.map(mark),
    })),
  };
  return changed ? next : fleet;
}
