// SPDX-License-Identifier: Apache-2.0

import cronstrue from "cronstrue";

const MONTH_NAMES = [
  "jan",
  "feb",
  "mar",
  "apr",
  "may",
  "jun",
  "jul",
  "aug",
  "sep",
  "oct",
  "nov",
  "dec",
];
const DAY_NAMES = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

interface CronField {
  hi: number;
  lo: number;
  names?: readonly string[];
}

/**
 * The five fields the daemon accepts, in order. The daemon parses with robfig's
 * `ParseStandard` (`engine/adapter/cronparse`): weekdays are 0–6, and there is
 * no `L`, `W` or `#`. cronstrue accepts all of those, so without this guard an
 * expression the daemon rejects would preview as a valid schedule.
 */
const FIELDS: readonly CronField[] = [
  { hi: 59, lo: 0 },
  { hi: 23, lo: 0 },
  { hi: 31, lo: 1 },
  { hi: 12, lo: 1, names: MONTH_NAMES },
  { hi: 6, lo: 0, names: DAY_NAMES },
];

function fieldValue(text: string, field: CronField): number | null {
  if (/^\d+$/.test(text)) {
    const value = Number(text);
    return value >= field.lo && value <= field.hi ? value : null;
  }
  const index = field.names?.indexOf(text.toLowerCase()) ?? -1;
  return index < 0 ? null : index + field.lo;
}

/**
 * Whether one field is valid for the daemon. A step wider than the span it
 * steps over (a step of 90 on the minute field) is also rejected: the daemon
 * accepts it but fires once per span, so describing it as "every 90 minutes"
 * would be wrong.
 */
function isValidField(text: string, field: CronField): boolean {
  return text.split(",").every((item) => {
    const [range = "", step, extra] = item.split("/");
    if (extra !== undefined) return false;
    let start: number | null;
    let end: number | null;
    if (range === "*" || range === "?") {
      start = field.lo;
      end = field.hi;
    } else {
      const [first = "", last, more] = range.split("-");
      if (more !== undefined) return false;
      start = fieldValue(first, field);
      if (start === null) return false;
      // A bare "N/step" steps from N to the end of the field.
      end = last === undefined ? (step === undefined ? start : field.hi) : fieldValue(last, field);
      if (end === null || end < start) return false;
    }
    if (step === undefined) return true;
    const size = Number(step);
    return /^\d+$/.test(step) && size > 0 && size <= end - start;
  });
}

const described = new Map<string, string>();

/**
 * Render a five-field cron expression in plain English. Anything the daemon
 * would not accept, or cronstrue cannot describe, comes back as the raw
 * expression, so a caller can tell that nothing was described by comparing the
 * result with its input.
 */
export function describeCron(expr: string): string {
  const cached = described.get(expr);
  if (cached !== undefined) return cached;
  const result = describe(expr);
  described.set(expr, result);
  return result;
}

function describe(expr: string): string {
  const parts = expr.trim().split(/\s+/);
  const field = (index: number) => FIELDS[index] as CronField;
  if (parts.length !== 5 || !parts.every((part, index) => isValidField(part, field(index)))) {
    return expr;
  }
  try {
    return cronstrue.toString(parts.join(" "), { use24HourTimeFormat: false });
  } catch {
    return expr;
  }
}
