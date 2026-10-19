// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import type { SortDir } from "@/components/sortable-head";
import { formatDate } from "../../lib/formatters";
import { describeCron } from "./describe-cron";

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
  order: ScheduleInventoryOrder,
  direction: SortDir = "asc",
): Schedule[] {
  const sign = direction === "asc" ? 1 : -1;
  return [...schedules].sort((left, right) => {
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
        comparison = compareCadence(left, right);
        break;
      case "status":
        comparison = statusRank(left.status) - statusRank(right.status);
        break;
    }
    return comparison * sign || compareName(left, right);
  });
}

/** Mobile has no sortable headers and always keeps live work first. */
export function deriveMobileScheduleInventory(schedules: readonly Schedule[]): Schedule[] {
  return [...schedules].sort(
    (left, right) => statusRank(left.status) - statusRank(right.status) || compareName(left, right),
  );
}

export function scheduleTriggerLabel(schedule: Schedule) {
  return schedule.trigger.kind === "cron"
    ? describeCron(schedule.trigger.expression)
    : `Once ${formatDate(schedule.trigger.at)}`;
}

/** Callers filter once and derive each layout's order from the same visible list. */
export function filterScheduleInventory(schedules: readonly Schedule[], query: string) {
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
    ? `${scheduleTriggerLabel(schedule)} ${cadenceWords(schedule.trigger.expression)} ${schedule.trigger.expression} ${schedule.trigger.timezone || "UTC"}`
    : `${scheduleTriggerLabel(schedule)} once ${schedule.trigger.at}`;
}

/**
 * DECISION: The Schedule column sorts on the cadence, not on the English label,
 * so "At 01:00 PM" does not sort before "At 09:00 AM" and midnight does not sort
 * after 11 PM. Recurring schedules order by cadence, then by how far into the
 * cadence they fire; one-shots follow, soonest first.
 */
const CADENCES = ["interval", "daily", "weekdays", "weekly", "monthly", "custom"] as const;
type Cadence = (typeof CADENCES)[number];

/** Words a person types to find a cadence; they stay stable if the label wording changes. */
const CADENCE_WORDS: Record<Cadence, string> = {
  custom: "custom",
  daily: "daily every day",
  interval: "every interval",
  monthly: "monthly every month",
  weekdays: "weekdays weekday",
  weekly: "weekly every week",
};

function cadenceWords(expression: string) {
  return CADENCE_WORDS[cronCadence(expression).cadence];
}

/**
 * Classifies a five-field cron by the builder's cadences. `position` is the
 * minutes into the cadence (the period for an interval, minutes since midnight
 * for a daily time, and so on), so an earlier time orders first within a cadence.
 */
function cronCadence(expression: string): { cadence: Cadence; position: number } {
  const [minute = "", hour = "", dayOfMonth = "", month = "", dayOfWeek = ""] = expression
    .trim()
    .split(/\s+/);
  // A list such as "9,13,17" orders by its first value.
  const first = (field: string) => /^\d+/.exec(field)?.[0];
  const time = Number(first(hour) ?? 0) * 60 + Number(first(minute) ?? 0);
  const everyMinutes = /^\*\/(\d+)$/.exec(minute);
  const everyHours = /^\*\/(\d+)$/.exec(hour);
  if (month === "*" && dayOfMonth === "*" && dayOfWeek === "*") {
    if (hour === "*") {
      return { cadence: "interval", position: everyMinutes ? Number(everyMinutes[1]) : 60 };
    }
    if (everyHours) return { cadence: "interval", position: Number(everyHours[1]) * 60 };
    return { cadence: "daily", position: time };
  }
  if (month === "*") {
    if (dayOfMonth === "*" && dayOfWeek === "1-5") return { cadence: "weekdays", position: time };
    if (dayOfMonth === "*" && /^[0-6]$/.test(dayOfWeek)) {
      return { cadence: "weekly", position: Number(dayOfWeek) * 1440 + time };
    }
    if (dayOfWeek === "*" && /^\d+$/.test(dayOfMonth)) {
      return { cadence: "monthly", position: Number(dayOfMonth) * 1440 + time };
    }
  }
  return { cadence: "custom", position: 0 };
}

function compareCadence(left: Schedule, right: Schedule) {
  if (left.trigger.kind === "cron" && right.trigger.kind === "cron") {
    const a = cronCadence(left.trigger.expression);
    const b = cronCadence(right.trigger.expression);
    return (
      CADENCES.indexOf(a.cadence) - CADENCES.indexOf(b.cadence) ||
      a.position - b.position ||
      left.trigger.expression.localeCompare(right.trigger.expression)
    );
  }
  if (left.trigger.kind === "once" && right.trigger.kind === "once") {
    return dateValue(left.trigger.at, 0) - dateValue(right.trigger.at, 0);
  }
  // Recurring schedules first, then one-shots.
  return left.trigger.kind === "cron" ? -1 : 1;
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
