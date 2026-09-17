"use client";

import {
  AlertCircle,
  ArrowLeft,
  Check,
  Copy,
  Network,
  Pencil,
  ScrollText,
  X,
} from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  type DelegationFleet,
  type DelegationInfo,
  type DelegationTraceEntry,
  emptyFleet,
  fleetCounts,
  type ParallelGroupState,
  type TeamBoardState,
  type TeamTaskInfo,
} from "@/features/agent";
import { TranscriptDialog } from "@/features/agent/components/transcript-dialog";
import { formatTokens } from "@/lib/formatters";
import { toolDisplayName } from "@/lib/tool-names";
import { cn } from "@/lib/utils";
import { contextUtilisation } from "./context-meter";
import {
  childHash,
  delegationFailed,
  delegationStopLabel,
  formatChildDuration,
  stopFamily,
  teamMemberStateLabel,
} from "./delegation-labels";
import { SidePanel } from "./side-panel";

/**
 * The Agents side panel — the web analogue of the TUI's unified f6 overlay
 * (`cmd/mecatui/ui/agents_overlay.go`): every child this chat's runs handed
 * work to, across turns, in three tabs. Subagents is the flat fleet roster
 * with a per-child focus pane (redacted trace, ids, transcript); Parallel
 * lists each fan-out group with its join strategy, branch tally, ★ winner
 * and run stop, then the branches inline; Teams shows the roster lead-first
 * with live member state and a per-member context meter, plus the shared
 * task board and the findings ledger.
 *
 * The panel is stateless about the fleet: it reads the live `fleet` prop the
 * chat hook reduces from the event stream, and reports tab/focus changes to
 * its owner (chat-view keeps them on the ActivePanel so Esc can step a focus
 * back to its roster before closing the panel).
 *
 * Text safety: every preview here (`detail`, trace `text`, task
 * `description`, finding `body`, `cause`) is a bounded, model-influenced
 * string the daemon already clamped. They render as plain React text nodes
 * only — never through markdown.
 */

export type DelegationTab = "subagents" | "parallel" | "teams";

/** The Teams tab's sub-views; "roster" is the un-focused default. */
type TeamView = "roster" | "tasks" | "findings";

/**
 * What the panel is drilled into, if anything. `null` is the active tab's
 * roster. A team focus carries both handles a team is keyed by (the Team
 * tool call id, and the team id the member frames are tagged with) so a
 * team that arrived under either one still resolves.
 */
export type DelegationFocus =
  | { kind: "subagent"; childId: string }
  | { kind: "parallel-group"; parentCallId: string }
  | {
      kind: "team-member";
      parentCallId: string;
      teamId?: string;
      member: string;
    }
  | {
      kind: "team-view";
      parentCallId: string;
      teamId?: string;
      view: "tasks" | "findings";
    };

/** The tab a focus belongs to. */
function delegationTabFor(focus: DelegationFocus): DelegationTab {
  switch (focus.kind) {
    case "subagent":
      return "subagents";
    case "parallel-group":
      return "parallel";
    default:
      return "teams";
  }
}

/**
 * The context-sensitive default tab, mirroring the TUI's
 * `preferredAgentsTab`: a live team wins, then a live parallel fan-out, then
 * whichever family has history (subagents, parallel, teams), else Subagents.
 */
export function preferredDelegationTab(
  fleet: DelegationFleet | undefined,
): DelegationTab {
  if (!fleet) return "subagents";
  const counts = fleetCounts(fleet);
  if (counts.team.live) return "teams";
  if (counts.parallel.running > 0) return "parallel";
  if (fleet.subagents.length > 0) return "subagents";
  if (fleet.parallelGroups.length > 0) return "parallel";
  if (fleet.teams.length > 0) return "teams";
  return "subagents";
}

/**
 * Where a click on an inline delegation card lands: a subagent card opens
 * its child, a parallel branch its group, a team lane its member. A card
 * without the handle its focus needs (a pre-start badge) opens the family's
 * roster instead.
 */
export function focusForDelegationCard(card: DelegationInfo): {
  tab: DelegationTab;
  focus: DelegationFocus | null;
} {
  switch (card.kind) {
    case "subagent":
      return {
        tab: "subagents",
        focus: card.childId
          ? { kind: "subagent", childId: card.childId }
          : null,
      };
    case "parallel":
      return {
        tab: "parallel",
        focus: card.parentCallId
          ? { kind: "parallel-group", parentCallId: card.parentCallId }
          : null,
      };
    default: {
      const member = card.memberName ?? card.label;
      const parentCallId = card.parentCallId ?? "";
      return {
        tab: "teams",
        focus:
          (parentCallId || card.teamId) && member
            ? {
                kind: "team-member",
                parentCallId,
                teamId: card.teamId,
                member,
              }
            : null,
      };
    }
  }
}

/**
 * A task's state glyph (TUI `taskGlyph`): ✓ completed, ◆ in progress, ○
 * pending and claimable, ⊘ pending but blocked on an unmet dependency. An
 * unknown state reads as not-yet-done. Colour is never the sole signal.
 */
export function teamTaskGlyph(state: string, blocked = false): string {
  switch (state) {
    case "completed":
    case "done":
      return "✓";
    case "in_progress":
      return "◆";
    default:
      return blocked ? "⊘" : "○";
  }
}

/** A pending task with a dependency that has not completed is blocked. */
export function teamTaskBlocked(
  task: TeamTaskInfo,
  tasks: readonly TeamTaskInfo[],
): boolean {
  if (task.state !== "pending") return false;
  return task.deps.some((dep) => {
    const state = tasks.find((t) => t.id === dep)?.state;
    return state !== "completed" && state !== "done";
  });
}

/** Team lanes lead-first; a stable sort keeps arrival order among peers. */
const leadFirst = (lanes: readonly DelegationInfo[]): DelegationInfo[] =>
  [...lanes].sort((a, b) => Number(Boolean(b.lead)) - Number(Boolean(a.lead)));

const findTeam = (
  fleet: DelegationFleet,
  ref: { parentCallId: string; teamId?: string },
): TeamBoardState | undefined =>
  fleet.teams.find(
    (team) =>
      (Boolean(ref.parentCallId) && team.parentCallId === ref.parentCallId) ||
      (Boolean(ref.teamId) && team.teamId === ref.teamId),
  );

const findLane = (
  team: TeamBoardState,
  member: string,
): DelegationInfo | undefined =>
  team.lanes.find((lane) => (lane.memberName ?? lane.label) === member);

const laneTokens = (card: DelegationInfo): number =>
  (card.inputTokens ?? 0) + (card.outputTokens ?? 0);

const plural = (n: number, noun: string): string =>
  `${n} ${n === 1 ? noun : `${noun}s`}`;

/**
 * Stable React keys for an append-only list whose items may repeat verbatim
 * (two identical trace chips, the same finding twice): the item's content
 * plus its occurrence count, so a repeat never collides and a new tail entry
 * never re-keys the ones before it.
 */
function contentKeys<T>(items: readonly T[], describe: (item: T) => string) {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const base = describe(item);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return `${base}#${n}`;
  });
}

/**
 * A subagent's or branch's state text: its current tool while it runs,
 * `done in <duration>` on a clean end, `done (<stop>) · <duration>` on a cap,
 * `failed` / `failed (<stop>)` on the bad family.
 */
function laneStateText(card: DelegationInfo, running: boolean): string {
  if (running) {
    return card.lastTool ? toolDisplayName(card.lastTool) : "working…";
  }
  const failed = delegationFailed(card);
  const stopLabel =
    card.stop !== undefined ? delegationStopLabel(card.stop) : "";
  const duration = formatChildDuration(card.durationMs ?? 0);
  if (failed) {
    const text =
      card.stop && stopLabel !== "error" ? `failed (${stopLabel})` : "failed";
    return duration ? `${text} · ${duration}` : text;
  }
  if (stopLabel === "done" || stopLabel === "") {
    return duration ? `done in ${duration}` : "done";
  }
  return duration ? `done (${stopLabel}) · ${duration}` : `done (${stopLabel})`;
}

/** `N tools · X tok`, empty pieces dropped. */
function laneCounters(card: DelegationInfo): string[] {
  const counters: string[] = [];
  if ((card.toolCount ?? 0) > 0) {
    counters.push(plural(card.toolCount ?? 0, "tool"));
  }
  const tokens = laneTokens(card);
  if (tokens > 0) counters.push(`${formatTokens(tokens)} tok`);
  if (card.cancelling) counters.push("cancelling…");
  return counters;
}

/** A subagent runs from its start frame until its end frame. */
const subagentRunning = (card: DelegationInfo): boolean =>
  card.stop === undefined;

/**
 * Whether a lane's glyph reads as bad: a failed child (error / failed
 * branch / benched-for-error member) or a cancelled one (the TUI's "bad"
 * stop family). The text label stays the compact stop label either way.
 */
const laneBad = (card: DelegationInfo): boolean =>
  delegationFailed(card) ||
  (card.stop !== undefined && stopFamily(card.stop) === "bad");

/** A branch runs until its branch_end, its failure, or the group's end. */
const branchRunning = (
  card: DelegationInfo,
  group: ParallelGroupState,
): boolean => card.stop === undefined && !card.failed && !group.done;

/** A team member runs until the team ends or its disposition lands. */
const memberRunning = (card: DelegationInfo, team: TeamBoardState): boolean =>
  !team.done && card.stop === undefined;

// ── Shared pieces ───────────────────────────────────────────────────────────

/**
 * The state glyph: a pulsing dot while running (static when a team member is
 * idle between rounds), a check on a benign end, an alert on a failure.
 */
function StateGlyph({
  running,
  failed,
  idle = false,
}: {
  running: boolean;
  failed: boolean;
  idle?: boolean;
}) {
  if (running) {
    return (
      <span
        role="status"
        aria-label={idle ? "idle" : "running"}
        className={cn(
          "size-2 shrink-0 rounded-full bg-brand",
          !idle && "animate-pulse",
        )}
      />
    );
  }
  if (failed) {
    return (
      <AlertCircle
        role="img"
        aria-label="failed"
        className="size-3.5 shrink-0 text-destructive"
      />
    );
  }
  return (
    <Check
      role="img"
      aria-label="done"
      className="size-3.5 shrink-0 text-muted-foreground"
    />
  );
}

function RosterHeader({ children }: { children: React.ReactNode }) {
  return (
    <p className="mb-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}

function EmptyState({ children }: { children: React.ReactNode }) {
  return <p className="py-6 text-sm text-muted-foreground">{children}</p>;
}

/** One clickable roster row: glyph, then a single truncating text line. */
function RosterRow({
  glyph,
  label,
  text,
  title,
  onClick,
  failed = false,
  children,
  actions,
}: {
  glyph: React.ReactNode;
  /** The accessible name ("Open subagent explore"). */
  label: string;
  text: string;
  title?: string;
  onClick?: () => void;
  failed?: boolean;
  /** Extra inline markers between the glyph and the text (a read-write cue). */
  children?: React.ReactNode;
  /** Controls beside the row (a cancel button): siblings of the row button,
   *  never nested inside it, so activating one never opens the focus pane. */
  actions?: React.ReactNode;
}) {
  const body = (
    <>
      {glyph}
      {children}
      <span className="min-w-0 flex-1 truncate">{text}</span>
    </>
  );
  const className = cn(
    "flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs",
    failed ? "text-destructive" : "text-foreground/90",
  );
  if (!onClick) {
    return (
      <li className="flex items-center gap-1">
        <div className={className} title={title}>
          {body}
        </div>
        {actions}
      </li>
    );
  }
  return (
    <li className="flex items-center gap-1">
      <button
        type="button"
        aria-label={label}
        title={title}
        onClick={onClick}
        className={cn(
          className,
          "hover:bg-secondary/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        {body}
      </button>
      {actions}
    </li>
  );
}

/** The focus pane's header: the back-to-roster button and the title line. */
function FocusHeader({ title, onBack }: { title: string; onBack: () => void }) {
  return (
    <div className="mb-3 flex items-center gap-2">
      <Button
        variant="ghost"
        size="sm"
        className="h-7 gap-1 px-2 text-xs text-muted-foreground"
        onClick={onBack}
        aria-label="Back to roster"
      >
        <ArrowLeft className="size-3.5" aria-hidden="true" />
        Back
      </Button>
      <h4 className="min-w-0 flex-1 truncate text-sm font-medium" title={title}>
        {title}
      </h4>
    </div>
  );
}

/** A monospace child id with a copy-to-clipboard control. */
function ChildIdRow({ childId }: { childId: string }) {
  return (
    <div className="flex items-center gap-1">
      <code
        className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground"
        title={childId}
      >
        {childId}
      </code>
      <Button
        variant="ghost"
        size="icon"
        className="size-6 shrink-0 text-muted-foreground"
        aria-label="Copy child id"
        onClick={() => {
          navigator.clipboard?.writeText(childId).catch(() => {});
        }}
      >
        <Copy className="size-3.5" aria-hidden="true" />
      </Button>
    </div>
  );
}

/** `label: value` fact rows; empty values are skipped. */
function FactList({ facts }: { facts: [string, string | undefined][] }) {
  const shown = facts.filter(([, value]) => Boolean(value));
  if (shown.length === 0) return null;
  return (
    <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
      {shown.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * The bounded activity trace: tool chips (`✓/✗/… name — detail`) and
 * coalesced message lines as muted text. Plain text nodes only.
 */
function TraceList({ trace }: { trace?: DelegationTraceEntry[] }) {
  if (!trace || trace.length === 0) {
    return <p className="text-xs text-muted-foreground">(no activity yet)</p>;
  }
  const keys = contentKeys(
    trace,
    (entry) => `${entry.kind}:${entry.name ?? ""}:${entry.text ?? ""}`,
  );
  return (
    <ul className="flex flex-col gap-1" aria-label="Activity trace">
      {trace.map((entry, index) => {
        const key = keys[index];
        if (entry.kind === "message") {
          return (
            <li
              key={key}
              className="whitespace-pre-wrap break-words text-xs text-muted-foreground"
            >
              {entry.text}
            </li>
          );
        }
        const mark = entry.pending ? "…" : entry.isError ? "✗" : "✓";
        return (
          <li
            key={key}
            className={cn(
              "rounded-md border border-border bg-muted/30 px-2 py-1 font-mono text-[11px]",
              entry.isError && "border-destructive/40 text-destructive/90",
            )}
          >
            <span aria-hidden="true">{mark} </span>
            <span className="font-medium" title={entry.name}>
              {entry.name === undefined
                ? undefined
                : toolDisplayName(entry.name)}
            </span>
            {entry.detail && (
              <span className="text-muted-foreground"> — {entry.detail}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="mt-4 mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}

/** Why a cancel control is disabled, or "" when it is live. */
function cancelDisabledReason(card: DelegationInfo): string {
  if (card.cancelling) return "cancelling…";
  if (!card.childId) {
    const noun =
      card.kind === "team"
        ? "Member"
        : card.kind === "parallel"
          ? "Branch"
          : "Child";
    return `${noun} id not known yet`;
  }
  return "";
}

/**
 * The per-child cancel control (the TUI's `x`): rendered only while the
 * child runs and a handler exists; a team member idle between rounds still
 * counts while its team is live. Disabled — with the reason in its tooltip —
 * while a cancel is in flight, or before the daemon has named the child (a
 * team lane has no session id until its first `team.member` frame, a
 * parallel branch none until its branch_start). Never nested inside the row
 * button, and the click never bubbles, so cancelling does not open a pane.
 * `compact` is the roster row's icon-only form; the default is the focus
 * pane's labelled button.
 */
function CancelChildButton({
  card,
  label,
  running,
  onCancel,
  compact = false,
}: {
  card: DelegationInfo;
  label: string;
  running: boolean;
  onCancel?: (childId: string) => void;
  compact?: boolean;
}) {
  if (!onCancel || !running) return null;
  const reason = cancelDisabledReason(card);
  const childId = card.childId;
  const name = `Cancel ${card.kind} ${label}`;
  const handleClick = (event: React.MouseEvent) => {
    event.stopPropagation();
    if (!reason && childId) onCancel(childId);
  };
  const control = compact ? (
    <button
      type="button"
      aria-label={name}
      title={reason ? undefined : "Cancel this child"}
      disabled={reason !== ""}
      onClick={handleClick}
      className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
    >
      <X className="size-3.5" aria-hidden="true" />
    </button>
  ) : (
    <Button
      variant="outline"
      size="sm"
      className="h-7 gap-1 text-xs text-destructive"
      aria-label={name}
      disabled={reason !== ""}
      onClick={handleClick}
    >
      <X className="size-3.5" aria-hidden="true" />
      {card.cancelling ? "Cancelling…" : "Cancel"}
    </Button>
  );
  if (!reason) return control;
  // A disabled button swallows pointer events, so the explanation rides a
  // wrapping span's tooltip (the same trick the disabled Teams tab uses).
  return (
    <span className="inline-flex shrink-0" title={reason}>
      {control}
    </span>
  );
}

/** Open transcript + the cancel slot, shared by every focus pane. */
function ChildActions({
  card,
  label,
  running,
  onOpenTranscript,
  onCancel,
}: {
  card: DelegationInfo;
  label: string;
  running: boolean;
  onOpenTranscript: (sessionId: string, label: string) => void;
  onCancel?: (childId: string) => void;
}) {
  const childId = card.childId;
  // Without a session id there is no transcript to open; the cancel control
  // still renders (disabled, naming why) so a live lane never looks inert.
  if (!childId && (!onCancel || !running)) return null;
  return (
    <div className="mt-3 flex flex-wrap gap-2">
      {childId && (
        <Button
          variant="outline"
          size="sm"
          className="h-7 gap-1 text-xs"
          onClick={() => onOpenTranscript(childId, label)}
        >
          <ScrollText className="size-3.5" aria-hidden="true" />
          Open transcript
        </Button>
      )}
      <CancelChildButton
        card={card}
        label={label}
        running={running}
        onCancel={onCancel}
      />
    </div>
  );
}

// ── Subagents ───────────────────────────────────────────────────────────────

function subagentRowText(card: DelegationInfo): string {
  const running = subagentRunning(card);
  return [
    card.label || "subagent",
    card.childId ? `#${childHash(card.childId)}` : "",
    card.background ? "bg" : "",
    laneStateText(card, running),
    ...laneCounters(card),
  ]
    .filter(Boolean)
    .join(" · ");
}

function SubagentRoster({
  fleet,
  onFocus,
  onCancel,
}: {
  fleet: DelegationFleet;
  onFocus: (focus: DelegationFocus) => void;
  onCancel?: (childId: string) => void;
}) {
  const counts = fleetCounts(fleet).subagents;
  if (fleet.subagents.length === 0) {
    return <EmptyState>No subagents have run in this chat yet.</EmptyState>;
  }
  return (
    <>
      <RosterHeader>
        subagents · {counts.running} running · {counts.done} done
      </RosterHeader>
      <ul className="flex flex-col" aria-label="Subagents">
        {fleet.subagents.map((card, index) => {
          const running = subagentRunning(card);
          const failed = laneBad(card);
          return (
            <RosterRow
              key={card.childId ?? `${index}:${card.label}`}
              glyph={<StateGlyph running={running} failed={failed} />}
              label={`Open subagent ${card.label || "subagent"}`}
              text={subagentRowText(card)}
              title={card.detail || undefined}
              failed={failed}
              onClick={
                card.childId
                  ? () =>
                      onFocus({
                        kind: "subagent",
                        childId: card.childId ?? "",
                      })
                  : undefined
              }
              actions={
                <CancelChildButton
                  card={card}
                  label={card.label || "subagent"}
                  running={running}
                  onCancel={onCancel}
                  compact
                />
              }
            />
          );
        })}
      </ul>
    </>
  );
}

function SubagentFocus({
  fleet,
  childId,
  onBack,
  onOpenTranscript,
  onCancel,
}: {
  fleet: DelegationFleet;
  childId: string;
  onBack: () => void;
  onOpenTranscript: (sessionId: string, label: string) => void;
  onCancel?: (childId: string) => void;
}) {
  const card = fleet.subagents.find((c) => c.childId === childId);
  if (!card) {
    return (
      <>
        <FocusHeader title="subagent" onBack={onBack} />
        <EmptyState>This subagent is no longer in the fleet.</EmptyState>
      </>
    );
  }
  const running = subagentRunning(card);
  const goal = card.label || "subagent";
  const state = running
    ? `running${card.lastTool ? ` · ${toolDisplayName(card.lastTool)}` : ""}`
    : laneStateText(card, false);
  return (
    <>
      <FocusHeader title={`subagent · ${goal}`} onBack={onBack} />
      <ChildIdRow childId={childId} />
      <FactList
        facts={[
          ["routing", card.detail || undefined],
          ["reason", card.routingReason],
          ["model", card.model],
          ["state", state],
          ["duration", formatChildDuration(card.durationMs ?? 0) || undefined],
          ["tools", (card.toolCount ?? 0) > 0 ? String(card.toolCount) : ""],
          [
            "tokens",
            laneTokens(card) > 0
              ? `↑${formatTokens(card.inputTokens ?? 0)} ↓${formatTokens(card.outputTokens ?? 0)}`
              : "",
          ],
        ]}
      />
      {card.cause && (
        <p className="mt-2 break-words text-xs text-destructive">
          {card.cause}
        </p>
      )}
      {card.background && (
        // The events carry background + done only; whether the agent has
        // collected the result is not on the wire, so name the channel
        // without claiming a delivery state.
        <p className="mt-2 text-xs text-muted-foreground">
          {running
            ? "background: runs detached; the agent collects its result via SubagentStatus"
            : "background: done — result ready for the agent (SubagentStatus)"}
        </p>
      )}
      <SectionLabel>Activity</SectionLabel>
      <TraceList trace={card.trace} />
      <ChildActions
        card={card}
        label={goal}
        running={running}
        onOpenTranscript={onOpenTranscript}
        onCancel={onCancel}
      />
    </>
  );
}

// ── Parallel ────────────────────────────────────────────────────────────────

const MAX_JOIN_LEN = 24;

const boundedJoin = (join: string): string => {
  const collapsed = join.split(/\s+/).filter(Boolean).join(" ");
  const bounded =
    collapsed.length > MAX_JOIN_LEN
      ? `${collapsed.slice(0, MAX_JOIN_LEN - 1)}…`
      : collapsed;
  return bounded || "all";
};

const winnerLabel = (group: ParallelGroupState): string => {
  const branch = group.branches.find(
    (c) => c.branchIndex === group.winner && c.label,
  );
  return branch ? branch.label : `branch-${group.winner + 1}`;
};

function parallelGroupText(group: ParallelGroupState): string {
  const done = group.branches.filter(
    (c) => c.stop !== undefined || c.failed,
  ).length;
  const total = Math.max(group.branchCount, group.branches.length);
  const pieces = [
    `join ${boundedJoin(group.join)}`,
    `${done}/${total} branches done`,
  ];
  if (group.winner >= 0) pieces.push(`★ ${winnerLabel(group)}`);
  return pieces.join(" · ");
}

const byBranchIndex = (branches: readonly DelegationInfo[]): DelegationInfo[] =>
  [...branches].sort(
    (a, b) =>
      (a.branchIndex ?? Number.MAX_SAFE_INTEGER) -
      (b.branchIndex ?? Number.MAX_SAFE_INTEGER),
  );

function ParallelRoster({
  fleet,
  onFocus,
}: {
  fleet: DelegationFleet;
  onFocus: (focus: DelegationFocus) => void;
}) {
  const counts = fleetCounts(fleet).parallel;
  if (fleet.parallelGroups.length === 0) {
    return (
      <EmptyState>No parallel runs have happened in this chat yet.</EmptyState>
    );
  }
  return (
    <>
      <RosterHeader>
        parallel · {counts.running} running · {counts.done} done
      </RosterHeader>
      <ul className="flex flex-col" aria-label="Parallel groups">
        {fleet.parallelGroups.map((group, index) => {
          const failed =
            group.done && group.branches.some((c) => delegationFailed(c));
          return (
            <RosterRow
              key={group.parentCallId || String(index)}
              glyph={
                <StateGlyph
                  running={!group.done}
                  failed={failed && group.winner < 0}
                />
              }
              label={`Open parallel group ${index + 1}`}
              text={`parallel ${index + 1} · ${parallelGroupText(group)}`}
              onClick={() =>
                onFocus({
                  kind: "parallel-group",
                  parentCallId: group.parentCallId,
                })
              }
            />
          );
        })}
      </ul>
    </>
  );
}

function branchText(card: DelegationInfo, group: ParallelGroupState): string {
  const running = branchRunning(card, group);
  return [
    `${card.winner ? "★ " : ""}${card.label || `branch-${(card.branchIndex ?? 0) + 1}`}`,
    card.detail,
    laneStateText(card, running),
    ...laneCounters(card),
  ]
    .filter(Boolean)
    .join(" · ");
}

function ParallelFocus({
  fleet,
  parentCallId,
  onBack,
  onOpenTranscript,
  onCancel,
}: {
  fleet: DelegationFleet;
  parentCallId: string;
  onBack: () => void;
  onOpenTranscript: (sessionId: string, label: string) => void;
  onCancel?: (childId: string) => void;
}) {
  const index = fleet.parallelGroups.findIndex(
    (g) => g.parentCallId === parentCallId,
  );
  const group = fleet.parallelGroups[index];
  if (!group) {
    return (
      <>
        <FocusHeader title="parallel" onBack={onBack} />
        <EmptyState>This parallel group is no longer in the fleet.</EmptyState>
      </>
    );
  }
  const tokens = (group.inputTokens ?? 0) + (group.outputTokens ?? 0);
  return (
    <>
      <FocusHeader title={`parallel ${index + 1}`} onBack={onBack} />
      <p className="text-xs text-muted-foreground">
        {parallelGroupText(group)}
      </p>
      <FactList
        facts={[
          ["run stop", group.stop ? delegationStopLabel(group.stop) : ""],
          ["tokens", tokens > 0 ? `${formatTokens(tokens)} tok` : ""],
        ]}
      />
      <SectionLabel>Branches</SectionLabel>
      <ul className="flex flex-col gap-2" aria-label="Branches">
        {byBranchIndex(group.branches).map((card, i) => {
          const running = branchRunning(card, group);
          const failed = laneBad(card);
          return (
            <li
              key={card.childId ?? `${card.branchIndex ?? i}`}
              className={cn(
                "rounded-md border border-border px-2 py-1.5 text-xs",
                failed && "border-destructive/40",
              )}
            >
              <div className="flex items-center gap-2">
                <StateGlyph running={running} failed={failed} />
                <span
                  className={cn(
                    "min-w-0 flex-1 truncate",
                    failed && "text-destructive",
                  )}
                  title={branchText(card, group)}
                >
                  {branchText(card, group)}
                </span>
              </div>
              {card.cause && (
                <p className="mt-1 break-words text-destructive/90">
                  {card.cause}
                </p>
              )}
              <ChildActions
                card={card}
                label={card.label || `branch-${(card.branchIndex ?? i) + 1}`}
                running={running}
                onOpenTranscript={onOpenTranscript}
                onCancel={onCancel}
              />
            </li>
          );
        })}
      </ul>
    </>
  );
}

// ── Teams ───────────────────────────────────────────────────────────────────

function teamHeaderText(team: TeamBoardState): string {
  const name = team.teamId ? `team ${team.teamId}` : "team";
  if (!team.done) {
    const working = team.lanes.filter(
      (lane) => !lane.idle && !lane.stopped,
    ).length;
    return `${name} · ${working}/${team.lanes.length} working`;
  }
  const pieces = [
    name,
    plural(team.rounds, "round"),
    delegationStopLabel(team.stop),
  ];
  const tokens = (team.inputTokens ?? 0) + (team.outputTokens ?? 0);
  if (tokens > 0) pieces.push(`${formatTokens(tokens)} tok`);
  const stopped = team.lanes.filter((lane) => lane.stopped).length;
  if (stopped > 0) pieces.push(`${stopped} stopped`);
  return pieces.join(" · ");
}

/** `~NN%` of the member's context window, null when the window is unknown. */
function memberContextPercent(card: DelegationInfo): number | null {
  const fraction = contextUtilisation(
    card.contextUsed ?? 0,
    card.contextWindow ?? 0,
  );
  return fraction === null ? null : Math.round(fraction * 100);
}

function MemberContextMeter({ card }: { card: DelegationInfo }) {
  const percent = memberContextPercent(card);
  if (percent === null) return null;
  return (
    <span
      className="inline-flex shrink-0 items-center gap-1 tabular-nums"
      title={`context: ${formatTokens(card.contextUsed ?? 0)} / ${formatTokens(card.contextWindow ?? 0)}`}
    >
      <span
        className="h-1 w-10 overflow-hidden rounded-full bg-border"
        aria-hidden="true"
      >
        <span
          className={cn(
            "block h-full rounded-full",
            percent >= 90
              ? "bg-destructive"
              : percent >= 75
                ? "bg-warning"
                : "bg-brand/60",
          )}
          style={{ width: `${Math.max(2, percent)}%` }}
        />
      </span>
      <span>~{percent}%</span>
    </span>
  );
}

function memberRowText(card: DelegationInfo, team: TeamBoardState): string {
  return [
    `${card.label || card.memberName || "member"}${card.lead ? " [lead]" : ""}`,
    card.detail,
    teamMemberStateLabel(card, team.done),
    ...laneCounters(card),
  ]
    .filter(Boolean)
    .join(" · ");
}

function TeamRoster({
  team,
  onFocus,
  onCancel,
}: {
  team: TeamBoardState;
  onFocus: (focus: DelegationFocus) => void;
  onCancel?: (childId: string) => void;
}) {
  if (team.lanes.length === 0) {
    return <EmptyState>No members have joined this team yet.</EmptyState>;
  }
  return (
    <ul className="flex flex-col" aria-label="Team roster">
      {leadFirst(team.lanes).map((card, index) => {
        const member = card.memberName ?? card.label;
        const running = memberRunning(card, team);
        const failed = delegationFailed(card);
        return (
          <RosterRow
            key={member || String(index)}
            glyph={
              <StateGlyph
                running={running}
                failed={failed}
                idle={Boolean(card.idle)}
              />
            }
            label={`Open team member ${member || "member"}`}
            text={memberRowText(card, team)}
            title={card.detail || undefined}
            failed={failed}
            onClick={() =>
              onFocus({
                kind: "team-member",
                parentCallId: team.parentCallId,
                teamId: team.teamId || undefined,
                member,
              })
            }
            actions={
              <CancelChildButton
                card={card}
                label={card.label || member || "member"}
                running={running}
                onCancel={onCancel}
                compact
              />
            }
          >
            {card.mutating && (
              <Pencil
                role="img"
                aria-label="read-write"
                className="size-3 shrink-0 text-muted-foreground"
              />
            )}
            <MemberContextMeter card={card} />
          </RosterRow>
        );
      })}
    </ul>
  );
}

function TeamTasks({ tasks }: { tasks: readonly TeamTaskInfo[] }) {
  if (tasks.length === 0) {
    return <EmptyState>No tasks on the board yet.</EmptyState>;
  }
  return (
    <ul className="flex flex-col gap-1 text-xs" aria-label="Team tasks">
      {tasks.map((task) => {
        const blocked = teamTaskBlocked(task, tasks);
        return (
          <li key={task.id} className="flex min-w-0 items-center gap-2">
            <span
              className="shrink-0 font-mono"
              title={blocked ? `${task.state} (blocked)` : task.state}
            >
              {teamTaskGlyph(task.state, blocked)}
            </span>{" "}
            <span className="min-w-0 flex-1 truncate">
              <span className="font-mono">{task.id}</span>
              {task.description && (
                <span title={task.description}> · {task.description}</span>
              )}
              <span className="text-muted-foreground">
                {" · "}
                {task.assignee || "—"}
                {task.deps.length > 0 && ` · deps: ${task.deps.join(", ")}`}
              </span>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function TeamFindings({ team }: { team: TeamBoardState }) {
  if (team.findings.length === 0) {
    return <EmptyState>No findings recorded yet.</EmptyState>;
  }
  const keys = contentKeys(
    team.findings,
    (finding) => `${finding.member}:${finding.body}`,
  );
  return (
    <ul className="flex flex-col gap-1.5 text-xs" aria-label="Team findings">
      {team.findings.map((finding, index) => (
        <li key={keys[index]} className="whitespace-pre-wrap break-words">
          <span className="font-medium">{finding.member}:</span> {finding.body}
        </li>
      ))}
    </ul>
  );
}

function TeamSection({
  team,
  view,
  onView,
  onFocus,
  onCancel,
}: {
  team: TeamBoardState;
  view: TeamView;
  onView: (view: TeamView) => void;
  onFocus: (focus: DelegationFocus) => void;
  onCancel?: (childId: string) => void;
}) {
  return (
    <section className="flex flex-col" aria-label={teamHeaderText(team)}>
      <RosterHeader>{teamHeaderText(team)}</RosterHeader>
      <ToggleGroup
        type="single"
        size="sm"
        variant="outline"
        value={view}
        onValueChange={(next) => {
          if (next) onView(next as TeamView);
        }}
        aria-label="Team view"
        className="mb-2"
      >
        <ToggleGroupItem value="roster" className="text-xs">
          Roster
        </ToggleGroupItem>
        <ToggleGroupItem value="tasks" className="text-xs">
          Tasks{team.tasks.length > 0 ? ` (${team.tasks.length})` : ""}
        </ToggleGroupItem>
        <ToggleGroupItem value="findings" className="text-xs">
          Findings
          {team.findings.length > 0 ? ` (${team.findings.length})` : ""}
        </ToggleGroupItem>
      </ToggleGroup>
      {view === "tasks" ? (
        <TeamTasks tasks={team.tasks} />
      ) : view === "findings" ? (
        <TeamFindings team={team} />
      ) : (
        <TeamRoster team={team} onFocus={onFocus} onCancel={onCancel} />
      )}
    </section>
  );
}

function TeamMemberFocus({
  team,
  member,
  onBack,
  onOpenTranscript,
  onCancel,
}: {
  team: TeamBoardState | undefined;
  member: string;
  onBack: () => void;
  onOpenTranscript: (sessionId: string, label: string) => void;
  onCancel?: (childId: string) => void;
}) {
  const card = team ? findLane(team, member) : undefined;
  if (!team || !card) {
    return (
      <>
        <FocusHeader title={`member · ${member}`} onBack={onBack} />
        <EmptyState>This team member is no longer in the fleet.</EmptyState>
      </>
    );
  }
  const running = memberRunning(card, team);
  const percent = memberContextPercent(card);
  return (
    <>
      <FocusHeader
        title={`member · ${card.label || member}${card.lead ? " [lead]" : ""}`}
        onBack={onBack}
      />
      {card.childId && <ChildIdRow childId={card.childId} />}
      <FactList
        facts={[
          ["routing", card.detail || undefined],
          ["reason", card.routingReason],
          ["model", card.model],
          ["access", card.mutating ? "read-write" : ""],
          ["state", teamMemberStateLabel(card, team.done)],
          ["tools", (card.toolCount ?? 0) > 0 ? String(card.toolCount) : ""],
          [
            "tokens",
            laneTokens(card) > 0
              ? `↑${formatTokens(card.inputTokens ?? 0)} ↓${formatTokens(card.outputTokens ?? 0)}`
              : "",
          ],
          [
            "context",
            percent === null
              ? ""
              : `${formatTokens(card.contextUsed ?? 0)} / ${formatTokens(card.contextWindow ?? 0)} · ${percent}%`,
          ],
          [
            "retried",
            (card.errorRounds ?? 0) > 0
              ? plural(card.errorRounds ?? 0, "error round")
              : "",
          ],
        ]}
      />
      {card.stopped && card.cause && (
        <p className="mt-2 break-words text-xs text-destructive">
          {card.cause}
        </p>
      )}
      <SectionLabel>Activity</SectionLabel>
      <TraceList trace={card.trace} />
      <ChildActions
        card={card}
        label={card.label || member}
        running={running}
        onOpenTranscript={onOpenTranscript}
        onCancel={onCancel}
      />
    </>
  );
}

function TeamsTab({
  fleet,
  focus,
  teamsSupported,
  onFocus,
  onOpenTranscript,
  onCancel,
}: {
  fleet: DelegationFleet;
  focus: DelegationFocus | null;
  teamsSupported?: boolean;
  onFocus: (focus: DelegationFocus | null) => void;
  onOpenTranscript: (sessionId: string, label: string) => void;
  onCancel?: (childId: string) => void;
}) {
  if (focus?.kind === "team-member") {
    return (
      <TeamMemberFocus
        team={findTeam(fleet, focus)}
        member={focus.member}
        onBack={() => onFocus(null)}
        onOpenTranscript={onOpenTranscript}
        onCancel={onCancel}
      />
    );
  }
  if (fleet.teams.length === 0) {
    return (
      <EmptyState>
        No teams have run in this chat yet.
        {teamsSupported === false && " Teams are disabled on this daemon."}
      </EmptyState>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      {fleet.teams.map((team, index) => {
        const viewed =
          focus?.kind === "team-view" &&
          ((Boolean(focus.parentCallId) &&
            focus.parentCallId === team.parentCallId) ||
            (Boolean(focus.teamId) && focus.teamId === team.teamId));
        const view: TeamView = viewed ? focus.view : "roster";
        return (
          <TeamSection
            key={team.parentCallId || team.teamId || String(index)}
            team={team}
            view={view}
            onView={(next) =>
              onFocus(
                next === "roster"
                  ? null
                  : {
                      kind: "team-view",
                      parentCallId: team.parentCallId,
                      teamId: team.teamId || undefined,
                      view: next,
                    },
              )
            }
            onFocus={onFocus}
            onCancel={onCancel}
          />
        );
      })}
    </div>
  );
}

// ── Panel ───────────────────────────────────────────────────────────────────

const TAB_BODY_CLASS = "min-h-0 flex-1 overflow-y-auto px-4 py-3";

export function DelegationPanel({
  fleet,
  tab,
  focus,
  onTabChange,
  onFocus,
  teamsSupported,
  onCancelChild,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  /** The session's fleet; undefined before the chat hook has one. */
  fleet: DelegationFleet | undefined;
  tab: DelegationTab;
  /** The drilled-into child/group/view, or null for the tab's roster. */
  focus: DelegationFocus | null;
  onTabChange: (tab: DelegationTab) => void;
  onFocus: (focus: DelegationFocus | null) => void;
  /** The daemon's `teams` capability; false disables the Teams tab when no
      team has run (the fleet's history still opens). */
  teamsSupported?: boolean;
  /** Cancels one live child by its session id (absent = no cancel controls). */
  onCancelChild?: (childId: string) => void | Promise<void>;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const resolved = fleet ?? emptyFleet();
  const teamsDisabled = teamsSupported === false && resolved.teams.length === 0;
  // A focus only applies inside its own tab; switching tabs shows the roster.
  const activeFocus = focus && delegationTabFor(focus) === tab ? focus : null;
  const [transcript, setTranscript] = useState<{
    sessionId: string;
    label: string;
  } | null>(null);
  const openTranscript = (sessionId: string, label: string) =>
    setTranscript({ sessionId, label });
  const cancel = onCancelChild
    ? (childId: string) => {
        void onCancelChild(childId);
      }
    : undefined;
  const back = () => onFocus(null);

  return (
    <SidePanel
      icon={Network}
      title="Agents"
      closeLabel="Close agents panel"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <Tabs
        value={tab}
        onValueChange={(next) => onTabChange(next as DelegationTab)}
        className="min-h-0 flex-1 gap-0"
      >
        <div className="shrink-0 border-b border-border px-4 py-2">
          <TabsList className="w-full" aria-label="Agent families">
            <TabsTrigger value="subagents" className="text-xs">
              Subagents
            </TabsTrigger>
            <TabsTrigger value="parallel" className="text-xs">
              Parallel
            </TabsTrigger>
            {teamsDisabled ? (
              // A disabled Radix trigger swallows pointer events, so the
              // explanation rides a wrapping span's tooltip.
              <span
                className="flex flex-1"
                title="Teams are disabled on this daemon"
              >
                <TabsTrigger value="teams" className="text-xs" disabled>
                  Teams
                </TabsTrigger>
              </span>
            ) : (
              <TabsTrigger value="teams" className="text-xs">
                Teams
              </TabsTrigger>
            )}
          </TabsList>
        </div>
        <TabsContent value="subagents" className={TAB_BODY_CLASS}>
          {activeFocus?.kind === "subagent" ? (
            <SubagentFocus
              fleet={resolved}
              childId={activeFocus.childId}
              onBack={back}
              onOpenTranscript={openTranscript}
              onCancel={cancel}
            />
          ) : (
            <SubagentRoster
              fleet={resolved}
              onFocus={onFocus}
              onCancel={cancel}
            />
          )}
        </TabsContent>
        <TabsContent value="parallel" className={TAB_BODY_CLASS}>
          {activeFocus?.kind === "parallel-group" ? (
            <ParallelFocus
              fleet={resolved}
              parentCallId={activeFocus.parentCallId}
              onBack={back}
              onOpenTranscript={openTranscript}
              onCancel={cancel}
            />
          ) : (
            <ParallelRoster fleet={resolved} onFocus={onFocus} />
          )}
        </TabsContent>
        <TabsContent value="teams" className={TAB_BODY_CLASS}>
          <TeamsTab
            fleet={resolved}
            focus={activeFocus}
            teamsSupported={teamsSupported}
            onFocus={onFocus}
            onOpenTranscript={openTranscript}
            onCancel={cancel}
          />
        </TabsContent>
      </Tabs>
      {transcript && (
        <TranscriptDialog
          sessionId={transcript.sessionId}
          label={transcript.label}
          onClose={() => setTranscript(null)}
        />
      )}
    </SidePanel>
  );
}
