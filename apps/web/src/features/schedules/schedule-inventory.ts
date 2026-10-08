// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import type { SortDirection } from "../../components/ui/sortable-head";
import { describeCron } from "./cron-builder";

type Schedule = ListSchedulesResponse["items"][number];

/**
 * TERM: Schedule inventory order is the user-selected ordering of the visible
 * schedule rows. Avoid: status precedence, which describes how the server
 * chooses one status when several runtime facts apply.
 */
export type ScheduleInventoryOrder =
  | "fires"
  | "lastRun"
  | "name"
  | "nextRun"
  | "schedule"
  | "status";

/**
 * DECISION: Search and ordering derive from the caller-visible inventory returned
 * by the authenticated BFF instead of adding a second browser-side source of
 * schedules. A separate search endpoint was rejected because this bounded
 * inventory is already loaded for the workspace.
 *
 * DECISION: Local search matches visible name, prompt preview, human trigger,
 * raw cron, and timezone. Owner and model were rejected because they are
 * detail-only facts; effective status stays in the segmented filters. Global
 * search retains its separate privacy contract that excludes prompt bodies.
 *
 * DECISION: The frozen desktop and mobile baseline starts in attention order:
 * Running, Claimed, Scheduled, Paused, Completed, then name. This browser-side
 * presentation order reuses BFF status and requires no backend sort operation.
 */
export function deriveScheduleInventory(
  schedules: readonly Schedule[],
  query: string,
  order: ScheduleInventoryOrder,
  direction: SortDirection = "asc",
): Schedule[] {
  const visible = filterScheduleInventory(schedules, query);
  const sign = direction === "asc" ? 1 : -1;
  return visible.sort((left, right) => {
    let comparison: number;
    switch (order) {
      case "fires":
        comparison = left.fireCount - right.fireCount;
        break;
      case "lastRun":
        comparison = dateValue(left.lastFireAt, 0) - dateValue(right.lastFireAt, 0);
        break;
      case "name":
        comparison = compareName(left, right);
        break;
      case "nextRun":
        comparison = nextRunValue(left) - nextRunValue(right);
        break;
      case "schedule":
        comparison = scheduleTriggerLabel(left).localeCompare(scheduleTriggerLabel(right));
        break;
      case "status":
        comparison = statusRank(left.status) - statusRank(right.status);
        break;
    }
    return comparison * sign || compareName(left, right);
  });
}

/** Mobile has no sortable headers and always keeps live work first. */
export function deriveMobileScheduleInventory(
  schedules: readonly Schedule[],
  query: string,
): Schedule[] {
  return filterScheduleInventory(schedules, query).sort(
    (left, right) => statusRank(left.status) - statusRank(right.status) || compareName(left, right),
  );
}

export function scheduleTriggerLabel(schedule: Schedule) {
  return schedule.trigger.kind === "cron"
    ? describeCron(schedule.trigger.expression)
    : `Once ${formatDate(schedule.trigger.at)}`;
}

function filterScheduleInventory(schedules: readonly Schedule[], query: string) {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return [...schedules];
  return schedules.filter((schedule) =>
    [schedule.name, schedule.prompt, triggerSearchText(schedule)]
      .join("\n")
      .toLocaleLowerCase()
      .includes(needle),
  );
}

function triggerSearchText(schedule: Schedule) {
  return schedule.trigger.kind === "cron"
    ? `${scheduleTriggerLabel(schedule)} ${schedule.trigger.expression} ${schedule.trigger.timezone || "UTC"}`
    : `${scheduleTriggerLabel(schedule)} ${schedule.trigger.at}`;
}

function statusRank(status: Schedule["status"]) {
  return {
    running: 0,
    claimed: 1,
    scheduled: 2,
    paused: 3,
    completed: 4,
  }[status];
}

function compareName(left: Schedule, right: Schedule) {
  return left.name.localeCompare(right.name);
}

function nextRunValue(schedule: Schedule) {
  return schedule.enabled
    ? dateValue(schedule.nextFireAt, Number.MAX_SAFE_INTEGER)
    : Number.MAX_SAFE_INTEGER;
}

function dateValue(value: string | null, fallback: number) {
  if (!value) return fallback;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? fallback : parsed;
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(
    new Date(value),
  );
}
