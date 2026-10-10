// SPDX-License-Identifier: Apache-2.0

import { Bot, GitFork, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { DelegationFocus } from "./delegation-card";
import type { DelegationFleet, DelegationState } from "./delegation-fleet";

type Family = DelegationFocus["family"];

export interface FleetSegment {
  /** The short form a narrow header shows: `N◐ M✓`, or `k/N` for a live team. */
  compact: string;
  family: Family;
  label: string;
  name: string;
  running: boolean;
}

function tally(states: DelegationState[]) {
  const running = states.filter((state) => state === "running").length;
  return { done: states.length - running, running };
}

function countSegment(
  family: Family,
  name: string,
  { done, running }: { done: number; running: number },
): FleetSegment {
  return {
    compact: `${running}◐ ${done}✓`,
    family,
    label: `${running} running · ${done} done`,
    name,
    running: running > 0,
  };
}

/**
 * One segment per family this chat delegated to, in the prototype's order;
 * none for an empty fleet, so the chip never renders empty. A live team
 * counts its working members, as the prototype does; a finished one counts
 * like the other families.
 */
export function fleetSegments(fleet: DelegationFleet | undefined): FleetSegment[] {
  if (!fleet) return [];
  const segments: FleetSegment[] = [];
  if (fleet.subagents.length > 0) {
    segments.push(
      countSegment("subagent", "Subagents", tally(fleet.subagents.map((item) => item.state))),
    );
  }
  if (fleet.parallelGroups.length > 0) {
    segments.push(
      countSegment("parallel", "Parallel", tally(fleet.parallelGroups.map((item) => item.state))),
    );
  }
  if (fleet.teams.length > 0) {
    const live = fleet.teams.filter((team) => team.state === "running");
    if (live.length > 0) {
      const members = live.flatMap((team) => team.members);
      const working = members.filter(
        (member) => member.state === "running" && member.disposition === undefined,
      ).length;
      segments.push({
        compact: `${working}/${members.length}`,
        family: "team",
        label: `${working}/${members.length} working`,
        name: "Teams",
        running: true,
      });
    } else {
      segments.push(countSegment("team", "Teams", tally(fleet.teams.map((team) => team.state))));
    }
  }
  return segments;
}

const icons: Record<Family, typeof Bot> = { parallel: GitFork, subagent: Bot, team: Users };

/**
 * The prototype's aggregate glance at every subagent, parallel group, and
 * team this chat ran: one ghost segment per family, each opening the
 * activity panel on that family. Studio's header also carries Activity,
 * Canvas, the run badge, and Stop, so the chip reads the chat header's
 * width (the `chat-header` container): the label shows only in a wide
 * header, the compact glyph form below that, and the chat hides the chip
 * in a narrow header so the title keeps its room. The label stays the
 * segment's accessible name.
 */
export function FleetStatusChip({
  className,
  fleet,
  onOpen,
}: {
  className?: string;
  fleet?: DelegationFleet;
  onOpen: (family: Family, opener: HTMLButtonElement) => void;
}) {
  const segments = fleetSegments(fleet);
  if (segments.length === 0) return null;
  return (
    <ul aria-label="Agents in this chat" className={cn("flex items-center gap-1", className)}>
      {segments.map((segment) => {
        const Icon = icons[segment.family];
        return (
          <li className="flex" key={segment.family}>
            <Button
              aria-label={`${segment.name} · ${segment.label}`}
              className="h-7 gap-1.5 px-2 text-xs font-normal text-muted-foreground"
              data-running={segment.running || undefined}
              onClick={(event) => onOpen(segment.family, event.currentTarget)}
              size="sm"
              title={`Open session activity: ${segment.name}`}
              type="button"
              variant="ghost"
            >
              <Icon aria-hidden="true" />
              {segment.running && (
                <span
                  aria-hidden="true"
                  className="size-1.5 shrink-0 animate-pulse rounded-full bg-brand"
                />
              )}
              <span className="hidden whitespace-nowrap @min-[72rem]/chat-header:inline">
                {segment.label}
              </span>
              <span className="whitespace-nowrap tabular-nums @min-[72rem]/chat-header:hidden">
                {segment.compact}
              </span>
            </Button>
          </li>
        );
      })}
    </ul>
  );
}
