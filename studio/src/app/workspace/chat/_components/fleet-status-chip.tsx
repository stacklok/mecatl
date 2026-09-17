"use client";

import { Bot, GitFork, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { type DelegationFleet, fleetCounts } from "@/features/agent";
import { cn } from "@/lib/utils";
import type { DelegationTab } from "./delegation-panel";

/**
 * The fleet status chip — the web analogue of mecatui's footer delegation
 * segments (cmd/mecatui/ui/footer.go): an always-visible, aggregate glance
 * at every child this chat ran, sitting beside the context meter in the
 * composer strip so it survives scrolling the transcript. One segment per
 * delegation family, each opening the Agents panel on its tab:
 *
 * - subagents `N running · M done` once ≥1 subagent has started; a
 *   background child counts as running until its `subagent.end`;
 * - parallel `N running · M done` counting fan-out GROUPS (not branches),
 *   once ≥1 Parallel run has started; a group is done at its `parallel.end`;
 * - team `k/N working` only while the latest team is live (before its
 *   `team.end`) — a finished team sheds its segment.
 *
 * Below 500px (the composer strip's own breakpoint) the long label gives way
 * to the TUI's compact glyphs (`N◐ M✓` / `k/N`). The counts come from the
 * same `fleetCounts` the header's Agents badge reads, so the two agree.
 */

type FleetSegmentIcon = "subagents" | "parallel" | "team";

export interface FleetSegment {
  /** The Agents-panel tab this segment opens. */
  tab: DelegationTab;
  icon: FleetSegmentIcon;
  /** The family name, spelled as the panel's roster headers spell it. */
  name: string;
  running: number;
  /** Ended children/groups; always 0 for a team (it works or it is gone). */
  done: number;
  /** `N running · M done` or `k/N working`. */
  label: string;
  /** `N◐ M✓` or `k/N` — for narrow widths. */
  compact: string;
}

const countSegment = (
  tab: DelegationTab,
  icon: FleetSegmentIcon,
  counts: { running: number; done: number },
): FleetSegment => ({
  tab,
  icon,
  name: tab,
  running: counts.running,
  done: counts.done,
  label: `${counts.running} running · ${counts.done} done`,
  compact: `${counts.running}◐ ${counts.done}✓`,
});

/** The segments to show, in the TUI's footer order; [] hides the chip. */
export function fleetSegments(
  fleet: DelegationFleet | undefined,
): FleetSegment[] {
  if (!fleet) return [];
  const counts = fleetCounts(fleet);
  const segments: FleetSegment[] = [];
  if (fleet.subagents.length > 0) {
    segments.push(countSegment("subagents", "subagents", counts.subagents));
  }
  if (fleet.parallelGroups.length > 0) {
    segments.push(countSegment("parallel", "parallel", counts.parallel));
  }
  if (counts.team.live) {
    const { working, total } = counts.team;
    segments.push({
      tab: "teams",
      icon: "team",
      name: "team",
      running: working,
      done: 0,
      label: `${working}/${total} working`,
      compact: `${working}/${total}`,
    });
  }
  return segments;
}

const ICONS: Record<FleetSegmentIcon, typeof Bot> = {
  subagents: Bot,
  parallel: GitFork,
  team: Users,
};

export function FleetStatusChip({
  fleet,
  onOpen,
  className,
}: {
  fleet?: DelegationFleet;
  onOpen: (tab: DelegationTab) => void;
  className?: string;
}) {
  const segments = fleetSegments(fleet);
  if (segments.length === 0) return null;
  return (
    <ul
      aria-label="Agents in this chat"
      className={cn("flex flex-wrap items-center gap-1", className)}
    >
      {segments.map((segment) => {
        const Icon = ICONS[segment.icon];
        const running = segment.running > 0;
        return (
          <li key={segment.tab} className="flex">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 gap-1.5 px-2 text-xs text-muted-foreground"
              aria-label={`${segment.name} · ${segment.label}`}
              title={`Open the Agents panel — ${segment.name}`}
              data-running={running || undefined}
              onClick={() => onOpen(segment.tab)}
            >
              <Icon aria-hidden="true" />
              {running && (
                <span
                  aria-hidden="true"
                  className="size-1.5 shrink-0 animate-pulse rounded-full bg-brand"
                />
              )}
              <span className="max-[499px]:hidden">{segment.label}</span>
              <span className="min-[500px]:hidden">{segment.compact}</span>
            </Button>
          </li>
        );
      })}
    </ul>
  );
}
