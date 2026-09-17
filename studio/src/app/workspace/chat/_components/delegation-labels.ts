import type { DelegationGroupInfo, DelegationInfo } from "@/features/agent";

/**
 * Pure label helpers for the inline delegation cards (subagent / team member
 * lane / parallel branch) and their group headers. They mirror the TUI's
 * `subagentStopLabel` / `teamLaneState` / roster lines in
 * `cmd/mecatui/ui/render.go` and `agents_overlay.go` so a Studio user reads
 * the same words a mecatui user does. Everything here is plain string work
 * over daemon-bounded metadata; nothing is markdown.
 */

/** The longest join-strategy token a parallel header renders verbatim. */
const MAX_JOIN_LEN = 24;

/** A team section shows this many lanes inline before folding to "+N more". */
export const TEAM_LANES_SHOWN = 6;

/**
 * Maps a child run's raw stop reason to the compact label shown on a resolved
 * card. An unknown or empty-but-present reason passes through verbatim so a
 * new stop reason is never hidden; an empty/absent one reads as a clean end.
 */
export function delegationStopLabel(stop: string | undefined): string {
  switch (stop ?? "") {
    case "end_turn":
    case "":
      return "done";
    case "max_tool_calls":
      return "max-tools";
    case "max_turns":
      return "max-turns";
    case "max_consecutive_failures":
      return "max-failures";
    case "cancelled":
      return "cancelled";
    case "error":
      return "error";
    case "budget":
      return "budget";
    case "structured_output":
      return "schema";
    case "no_progress":
      return "no-progress";
    default:
      return (stop ?? "").trim();
  }
}

/**
 * Whether a stop reason is a failure ("bad": error / cancelled) or a benign
 * terminal ("ok": clean ends and the cap family — a capped child still did
 * its work).
 */
export function stopFamily(stop: string | undefined): "ok" | "bad" {
  return stop === "error" || stop === "cancelled" ? "bad" : "ok";
}

/**
 * The short hash the TUI shows as `#<hash>` so two children with similar
 * goals stay unambiguous: the last six characters of the child id.
 */
export function childHash(childId: string): string {
  return childId.length <= 6 ? childId : childId.slice(-6);
}

/** Wall-clock child duration, humanized ("850ms", "12s", "3m 20s"). */
export function formatChildDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

/**
 * A team member's state label (TUI `teamLaneState`): once the team has ended,
 * a benched member reads `stopped — <reason>` (or bare `stopped`), one that
 * survived a recovered failure `done (retried)`, the rest `done`; before that,
 * `idle` between rounds, else the running tool (or `working`) with a trailing
 * ellipsis heartbeat so a quiet lane reads as in flight rather than stalled.
 */
export function teamMemberStateLabel(
  card: DelegationInfo,
  teamDone: boolean,
): string {
  if (teamDone && card.stopped) {
    return card.stopReason ? `stopped — ${card.stopReason}` : "stopped";
  }
  if (teamDone && (card.errorRounds ?? 0) > 0) return "done (retried)";
  if (teamDone) return "done";
  if (card.idle) return "idle";
  return `${card.lastTool || "working"}…`;
}

/**
 * Whether a card is still live. A subagent is live once it has a child id and
 * no stop; a parallel branch until its branch_end (or its group's end); a team
 * member until the team ends (its disposition sets the lane's stop).
 */
export function delegationRunning(
  card: DelegationInfo,
  group?: DelegationGroupInfo,
): boolean {
  switch (card.kind) {
    case "subagent":
      return Boolean(card.childId) && card.stop === undefined;
    case "parallel":
      return card.stop === undefined && !card.failed && !group?.done;
    case "team":
      return !group?.done && card.stop === undefined;
    default:
      return false;
  }
}

/**
 * Whether a card renders as failed: a child that stopped on error, a failed
 * parallel branch, or a team member the supervisor benched for an error.
 */
export function delegationFailed(card: DelegationInfo): boolean {
  if (card.stop === "error" || card.failed) return true;
  return (
    card.kind === "team" && Boolean(card.stopped) && card.stopReason === "error"
  );
}

/** One rendered section of a turn's card row: a headed team/parallel group or the flat subagent list. */
export interface DelegationSection<T extends DelegationInfo = DelegationInfo> {
  /** Stable key: the owning tool call id (team/parallel) or "subagents". */
  key: string;
  kind: DelegationInfo["kind"];
  /** Group header line; absent for the flat subagent section. */
  header?: string;
  /** The group facts this section's cards read (team/parallel only). */
  group?: DelegationGroupInfo;
  cards: T[];
}

const plural = (n: number, noun: string): string =>
  `${n} ${n === 1 ? noun : `${noun}s`}`;

const boundedJoin = (join: string | undefined): string => {
  const collapsed = (join ?? "").split(/\s+/).filter(Boolean).join(" ");
  const bounded =
    collapsed.length > MAX_JOIN_LEN
      ? `${collapsed.slice(0, MAX_JOIN_LEN - 1)}…`
      : collapsed;
  return bounded || "all";
};

/** Lead lanes first; a stable sort keeps arrival order among peers. */
const leadFirst = <T extends DelegationInfo>(lanes: T[]): T[] =>
  [...lanes].sort((a, b) => Number(Boolean(b.lead)) - Number(Boolean(a.lead)));

const byBranchIndex = <T extends DelegationInfo>(branches: T[]): T[] =>
  [...branches].sort(
    (a, b) =>
      (a.branchIndex ?? Number.MAX_SAFE_INTEGER) -
      (b.branchIndex ?? Number.MAX_SAFE_INTEGER),
  );

/** `team <id> · N members` + ` · R rounds · <stop>` (+ ` · S stopped`) once done. */
function teamHeader(
  cards: DelegationInfo[],
  group: DelegationGroupInfo | undefined,
): string {
  const teamId = group?.teamId ?? cards.find((c) => c.teamId)?.teamId;
  let header = `team${teamId ? ` ${teamId}` : ""} · ${plural(cards.length, "member")}`;
  if (group?.done) {
    if (group.rounds !== undefined) {
      header += ` · ${plural(group.rounds, "round")}`;
    }
    header += ` · ${delegationStopLabel(group.stop)}`;
    if ((group.stoppedCount ?? 0) > 0) {
      header += ` · ${group.stoppedCount} stopped`;
    }
  }
  return header;
}

/** The winner's branch label, else the 1-based `branch-N` fallback. */
function winnerLabel(cards: DelegationInfo[], winner: number): string {
  const branch = cards.find((c) => c.branchIndex === winner && c.label);
  return branch ? branch.label : `branch-${winner + 1}`;
}

/** `parallel · <join> · k/N done` + ` · ★ <winner>` + ` · run stop: <stop>`. */
function parallelHeader(
  cards: DelegationInfo[],
  group: DelegationGroupInfo | undefined,
): string {
  const done = cards.filter((c) => c.stop !== undefined || c.failed).length;
  const total = Math.max(group?.branchCount ?? 0, cards.length);
  let header = `parallel · ${boundedJoin(group?.join)} · ${done}/${total} done`;
  if (group?.winner !== undefined && group.winner >= 0) {
    header += ` · ★ ${winnerLabel(cards, group.winner)}`;
  }
  if (group?.stop) header += ` · run stop: ${delegationStopLabel(group.stop)}`;
  return header;
}

function sectionKey(card: DelegationInfo): string {
  switch (card.kind) {
    case "team":
      return `team:${card.parentCallId ?? card.teamId ?? ""}`;
    case "parallel":
      return `parallel:${card.parentCallId ?? ""}`;
    default:
      return "subagents";
  }
}

function groupFor(
  kind: DelegationInfo["kind"],
  cards: DelegationInfo[],
  groups: Record<string, DelegationGroupInfo> | undefined,
): DelegationGroupInfo | undefined {
  if (!groups || kind === "subagent") return undefined;
  const parentCallId = cards.find((c) => c.parentCallId)?.parentCallId;
  if (parentCallId && groups[parentCallId]) return groups[parentCallId];
  if (kind === "team") {
    const teamId = cards.find((c) => c.teamId)?.teamId;
    if (teamId) {
      return Object.values(groups).find(
        (g) => g.kind === "team" && g.teamId === teamId,
      );
    }
  }
  return undefined;
}

/**
 * Splits a turn's cards into ordered sections: one headed section per
 * Parallel/Team call (team lanes lead-first, branches by index) and one flat,
 * header-less section for the subagents, each in first-appearance order.
 * Generic over the card type so a caller's positional React ids survive.
 */
export function groupDelegations<T extends DelegationInfo>(
  cards: readonly T[],
  groups?: Record<string, DelegationGroupInfo>,
): DelegationSection<T>[] {
  const sections = new Map<string, DelegationSection<T>>();
  for (const card of cards) {
    const key = sectionKey(card);
    const section = sections.get(key);
    if (section) {
      section.cards.push(card);
    } else {
      sections.set(key, { key, kind: card.kind, cards: [card] });
    }
  }
  return [...sections.values()].map((section) => {
    if (section.kind === "subagent") return section;
    const group = groupFor(section.kind, section.cards, groups);
    if (section.kind === "team") {
      const lanes = leadFirst(section.cards);
      return {
        ...section,
        group,
        cards: lanes,
        header: teamHeader(lanes, group),
      };
    }
    const branches = byBranchIndex(section.cards);
    return {
      ...section,
      group,
      cards: branches,
      header: parallelHeader(branches, group),
    };
  });
}
