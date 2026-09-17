"use client";

import { AlertCircle, Eye, GitBranch, Pencil, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { DelegationGroupInfo, DelegationInfo } from "@/features/agent";
import { formatTokens } from "@/lib/formatters";
import { toolDisplayName } from "@/lib/tool-names";
import { cn } from "@/lib/utils";
import {
  childHash,
  delegationFailed,
  delegationRunning,
  delegationStopLabel,
  formatChildDuration,
  groupDelegations,
  TEAM_LANES_SHOWN,
  teamMemberStateLabel,
} from "./delegation-labels";

/** Opens a card in the side panel (the child's trace / its group's tab). */
export type OpenDelegation = (
  card: DelegationInfo,
  group?: DelegationGroupInfo,
) => void;

/** Cancels one live child by its session id. */
export type CancelDelegation = (childId: string) => void;

/** Opens one child's stored transcript read-only, by its session id. */
export type InspectDelegation = (childId: string, label: string) => void;

const BADGE_CLASS =
  "max-w-full gap-1 border-transparent text-xs font-normal text-muted-foreground";

/**
 * One delegated child on a turn: the start-time badge upgraded into a live
 * card for all three families. A subagent ticks tool/token counters and its
 * current tool while it runs, then settles on `done in <duration>` or the
 * compact stop label; a parallel branch does the same and marks the ★ winner
 * or a failed branch; a team member lane shows `[lead]`, a read-write cue,
 * and its supervisor state (`working…` / `idle` / `done` / `stopped — error`).
 * A failed child renders its cause under the badge instead of vanishing.
 *
 * With `onOpen` the badge is a button (click/keyboard) that hands the card and
 * its group to the caller; with `onCancel` a live child gets a small cancel
 * control beside it; with `onInspect` a child that has a session id gets an
 * Inspect control that opens its stored transcript read-only (the TUI's
 * Child-runs Inspect). All text is plain metadata the daemon already bounded.
 */
export function DelegationCard({
  delegation,
  group,
  onOpen,
  onCancel,
  onInspect,
}: {
  delegation: DelegationInfo;
  group?: DelegationGroupInfo;
  onOpen?: OpenDelegation;
  onCancel?: CancelDelegation;
  onInspect?: InspectDelegation;
}) {
  const isTeam = delegation.kind === "team";
  const running = delegationRunning(delegation, group);
  const failed = delegationFailed(delegation);
  const teamDone =
    isTeam && (Boolean(group?.done) || delegation.stop !== undefined);
  const stopLabel =
    delegation.stop !== undefined ? delegationStopLabel(delegation.stop) : "";

  const counters: string[] = [];
  if (isTeam) counters.push(teamMemberStateLabel(delegation, teamDone));
  if (delegation.toolCount !== undefined && delegation.toolCount > 0) {
    counters.push(
      `${delegation.toolCount} ${delegation.toolCount === 1 ? "tool" : "tools"}`,
    );
  }
  const inputTokens = delegation.inputTokens ?? 0;
  const outputTokens = delegation.outputTokens ?? 0;
  const totalTokens = inputTokens + outputTokens;
  if (totalTokens > 0) counters.push(`${formatTokens(totalTokens)} tok`);
  if (!isTeam && running && delegation.lastTool) {
    counters.push(toolDisplayName(delegation.lastTool));
  }
  if (!isTeam && delegation.stop !== undefined && !failed) {
    const duration = formatChildDuration(delegation.durationMs ?? 0);
    if (stopLabel === "done") {
      counters.push(duration ? `done in ${duration}` : "done");
    } else {
      counters.push(
        duration ? `done (${stopLabel}) · ${duration}` : `done (${stopLabel})`,
      );
    }
  }
  if (!isTeam && failed) {
    counters.push(
      delegation.stop && stopLabel !== "error"
        ? `failed (${stopLabel})`
        : "failed",
    );
  }
  if (delegation.cancelling) counters.push("cancelling…");

  const detailHasModel =
    Boolean(delegation.model) &&
    Boolean(delegation.detail?.includes(delegation.model ?? ""));
  const tooltip = [
    delegation.childId && `#${childHash(delegation.childId)}`,
    delegation.routingReason && `routing: ${delegation.routingReason}`,
    delegation.detail,
    delegation.model && !detailHasModel && `model: ${delegation.model}`,
    totalTokens > 0 &&
      `↑${formatTokens(inputTokens)} ↓${formatTokens(outputTokens)}`,
    failed && delegation.cause,
  ]
    .filter(Boolean)
    .join(" · ");

  const text = [
    `${delegation.winner ? "★ " : ""}${delegation.kind}: ${delegation.label}`,
    delegation.lead ? " [lead]" : "",
    delegation.background ? " · background" : "",
    delegation.detail ? ` · ${delegation.detail}` : "",
    counters.length > 0 ? ` · ${counters.join(" · ")}` : "",
  ].join("");

  const glyph = running ? (
    <span
      role="status"
      aria-label={isTeam && delegation.idle ? "idle" : "running"}
      className={cn(
        "size-2 shrink-0 rounded-full bg-brand",
        !(isTeam && delegation.idle) && "animate-pulse",
      )}
    />
  ) : failed ? (
    <AlertCircle className="size-3 shrink-0" />
  ) : (
    <GitBranch className="size-3 shrink-0" />
  );
  const body = (
    <>
      {glyph}
      {delegation.mutating && (
        <Pencil
          role="img"
          aria-label="read-write"
          className="size-3 shrink-0"
        />
      )}
      <span className="truncate">{text}</span>
    </>
  );
  const badgeClass = cn(
    BADGE_CLASS,
    failed && "bg-destructive/10 text-destructive",
  );

  const cancellable =
    onCancel !== undefined &&
    running &&
    Boolean(delegation.childId) &&
    !delegation.cancelling;
  const inspectable = onInspect !== undefined && Boolean(delegation.childId);

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex min-w-0 items-center gap-1">
        {onOpen ? (
          <Badge
            asChild
            variant="secondary"
            className={cn(
              badgeClass,
              "cursor-pointer hover:bg-secondary/80",
              failed && "hover:bg-destructive/15",
            )}
          >
            <button
              type="button"
              aria-label={`Open ${delegation.kind} ${delegation.label}`}
              title={tooltip || undefined}
              onClick={() => onOpen(delegation, group)}
            >
              {body}
            </button>
          </Badge>
        ) : (
          <Badge
            variant="secondary"
            title={tooltip || undefined}
            className={badgeClass}
          >
            {body}
          </Badge>
        )}
        {cancellable && delegation.childId && (
          <button
            type="button"
            aria-label={`Cancel ${delegation.kind} ${delegation.label}`}
            title="Cancel this child"
            className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-secondary hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            onClick={() => onCancel(delegation.childId ?? "")}
          >
            <X className="size-3" aria-hidden="true" />
          </button>
        )}
        {inspectable && delegation.childId && (
          <button
            type="button"
            aria-label={`Inspect ${delegation.kind} ${delegation.label}`}
            title="Open this child's transcript (read-only)"
            className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-secondary hover:text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            onClick={() =>
              onInspect(
                delegation.childId ?? "",
                `${delegation.kind}: ${delegation.label}`,
              )
            }
          >
            <Eye className="size-3" aria-hidden="true" />
          </button>
        )}
      </div>
      {failed && delegation.cause && (
        <p
          className="mt-0.5 truncate pl-1 text-xs text-destructive/90"
          title={delegation.cause}
        >
          {delegation.cause}
        </p>
      )}
    </div>
  );
}

/**
 * The card row under an assistant turn: one headed section per Parallel
 * fan-out / Team (`parallel · first · 1/3 done · ★ fast`, `team t1 · 3
 * members · 2 rounds · done`) and a flat row for the subagents. A team with
 * more than `TEAM_LANES_SHOWN` lanes folds the rest behind a `+N more` badge
 * that opens the group.
 */
export function DelegationCardRow({
  delegations,
  groups,
  onOpen,
  onCancel,
  onInspect,
}: {
  delegations: readonly DelegationInfo[];
  groups?: Record<string, DelegationGroupInfo>;
  onOpen?: OpenDelegation;
  onCancel?: CancelDelegation;
  onInspect?: InspectDelegation;
}) {
  // Cards can repeat verbatim within a turn, so React keys are positional
  // (by identity, so the exact message card reaches `onOpen` — never a copy).
  const keys = new Map(
    delegations.map((card, index) => [
      card,
      card.childId ?? `${index}:${card.kind}:${card.label}`,
    ]),
  );
  const sections = groupDelegations(delegations, groups);
  return (
    <div className="mt-1.5 flex flex-col gap-1.5">
      {sections.map((section) => {
        const folded =
          section.kind === "team" && section.cards.length > TEAM_LANES_SHOWN;
        const shown = folded
          ? section.cards.slice(0, TEAM_LANES_SHOWN)
          : section.cards;
        const hidden = section.cards.length - shown.length;
        const firstHidden = section.cards[TEAM_LANES_SHOWN];
        return (
          <div key={section.key} className="flex min-w-0 flex-col gap-1">
            {section.header && (
              <p
                className="truncate text-xs text-muted-foreground"
                title={section.header}
              >
                {section.header}
              </p>
            )}
            <div className="flex flex-wrap gap-1.5">
              {shown.map((card) => (
                <DelegationCard
                  key={keys.get(card)}
                  delegation={card}
                  group={section.group}
                  onOpen={onOpen}
                  onCancel={onCancel}
                  onInspect={onInspect}
                />
              ))}
              {hidden > 0 &&
                (onOpen && firstHidden ? (
                  <Badge
                    asChild
                    variant="secondary"
                    className={cn(
                      BADGE_CLASS,
                      "cursor-pointer hover:bg-secondary/80",
                    )}
                  >
                    <button
                      type="button"
                      aria-label={`Open team members (${hidden} more)`}
                      onClick={() => onOpen(firstHidden, section.group)}
                    >
                      +{hidden} more
                    </button>
                  </Badge>
                ) : (
                  <Badge variant="secondary" className={BADGE_CLASS}>
                    +{hidden} more
                  </Badge>
                ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}
