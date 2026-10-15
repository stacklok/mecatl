// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { Badge } from "../../components/ui/badge";
import { cn } from "../../lib/utils";

type ScheduleStatus = ListSchedulesResponse["items"][number]["status"];

/**
 * DECISION: One status vocabulary for the inventory and the detail page.
 * Completed is hollow and outlined so it cannot be mistaken for the live
 * Scheduled state, which shares the info tint. A live fire (Claimed or
 * Running) pulses, as in the prototype's `schedule-badges.tsx`.
 *
 * DECISION: Each status keeps its own dot colour. The prototype draws
 * Scheduled and Paused in the same grey; Studio keeps them apart so the
 * colour alone still tells them apart, and Completed, which the prototype
 * has no state for, gets the hollow dot.
 */
const STATUS_STYLE: Record<
  ScheduleStatus,
  {
    badge: "info" | "muted" | "outline" | "success" | "warning";
    dot: string;
    label: string;
    live: boolean;
  }
> = {
  claimed: { badge: "warning", dot: "bg-warning", label: "Claimed", live: true },
  completed: {
    badge: "outline",
    dot: "border border-muted-foreground",
    label: "Completed",
    live: false,
  },
  paused: { badge: "muted", dot: "bg-muted-foreground", label: "Paused", live: false },
  running: { badge: "success", dot: "bg-success", label: "Running", live: true },
  scheduled: { badge: "info", dot: "bg-info", label: "Scheduled", live: false },
};

/** The Title-case name of a schedule status, as the inventory and detail page show it. */
export function scheduleStatusLabel(status: ScheduleStatus) {
  return STATUS_STYLE[status].label;
}

function Dot({ status }: { status: ScheduleStatus }) {
  const style = STATUS_STYLE[status];
  return (
    <span
      aria-hidden="true"
      className={cn("size-1.5 shrink-0 rounded-full", style.dot, style.live && "animate-pulse")}
      data-schedule-status-dot={status}
    />
  );
}

export function ScheduleStatusBadge({ status }: { status: ScheduleStatus }) {
  const style = STATUS_STYLE[status];
  return (
    <Badge variant={style.badge}>
      {style.live && (
        <span aria-hidden="true" className="size-1.5 animate-pulse rounded-full bg-current" />
      )}
      {style.label}
    </Badge>
  );
}

/**
 * The quiet dot on its own, where there is no room for the word: the label is
 * kept for screen readers and the pointer's hover.
 */
export function ScheduleStatusDot({ status }: { status: ScheduleStatus }) {
  const label = scheduleStatusLabel(status);
  return (
    <span className="inline-flex shrink-0 items-center" title={label}>
      <Dot status={status} />
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** The dot with its label beside it, for the inventory's Status column. */
export function ScheduleStatusLabel({ status }: { status: ScheduleStatus }) {
  return (
    <span className="inline-flex items-center gap-2">
      <Dot status={status} />
      <span className="text-muted-foreground">{scheduleStatusLabel(status)}</span>
    </span>
  );
}
