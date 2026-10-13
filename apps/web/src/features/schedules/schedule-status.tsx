// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { Badge } from "../../components/ui/badge";

type ScheduleStatus = ListSchedulesResponse["items"][number]["status"];

/**
 * DECISION: One status vocabulary for the inventory and the detail page.
 * Completed is hollow and outlined so it cannot be mistaken for the live
 * Scheduled state, which shares the info tint.
 */
const STATUS_STYLE: Record<
  ScheduleStatus,
  { badge: "info" | "muted" | "outline" | "success" | "warning"; dot: string }
> = {
  claimed: { badge: "warning", dot: "bg-warning" },
  completed: { badge: "outline", dot: "border border-muted-foreground" },
  paused: { badge: "muted", dot: "bg-muted-foreground" },
  running: { badge: "success", dot: "bg-success" },
  scheduled: { badge: "info", dot: "bg-info" },
};

export function ScheduleStatusBadge({ status }: { status: ScheduleStatus }) {
  return <Badge variant={STATUS_STYLE[status].badge}>{status}</Badge>;
}

export function ScheduleStatusDot({ status }: { status: ScheduleStatus }) {
  return (
    <span className="mt-1.5 flex size-3 shrink-0 items-center justify-center">
      <span aria-hidden="true" className={`size-2 rounded-full ${STATUS_STYLE[status].dot}`} />
      <span className="sr-only">{status}</span>
    </span>
  );
}
