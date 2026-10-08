// SPDX-License-Identifier: Apache-2.0

/**
 * The structured cron editor's derivation layer. The schedule form keeps
 * carrying the cron STRING on the wire (the daemon owns cron grammar
 * validation); this module is purely a nicer editor for that string.
 * `builderToCron` derives the string from the builder's controls, and
 * `cronToBuilder` is its best-effort inverse: it recognises exactly the
 * shapes the builder can produce and files everything else (steps, lists,
 * exotic ranges) under `custom`, where the raw string is edited directly
 * and preserved byte-for-byte.
 */

export type CronRepeat = "daily" | "weekdays" | "weekly" | "monthly" | "interval" | "custom";

/** The unit an `interval` repeat counts in. */
export type CronIntervalUnit = "minutes" | "hours";

/** The builder's parsed view of a cron string. */
export interface CronBuilder {
  /** Day of month for `monthly` (1–28 — the days every month has). */
  monthday: number;
  repeat: CronRepeat;
  /** "HH:MM", 24-hour — the `<input type="time">` value. */
  time: string;
  /** Unit for `interval`. */
  unit: CronIntervalUnit;
  /** Step for `interval` (1–59 minutes or 1–23 hours). */
  every: number;
  /** Cron day-of-week for `weekly` (0 = Sunday … 6 = Saturday). */
  weekday: number;
}

/** What `builderToCron` needs; `custom` never derives (the raw string wins). */
export interface CronBuilderShape {
  every?: number;
  monthday?: number;
  repeat: Exclude<CronRepeat, "custom">;
  time: string;
  unit?: CronIntervalUnit;
  weekday?: number;
}

/** The builder can never derive an empty cron: a blank time falls back here. */
const DEFAULT_HOUR = 9;
const DEFAULT_MINUTE = 0;

function parseTime(time: string): { hour: number; minute: number } {
  const match = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
  if (!match) return { hour: DEFAULT_HOUR, minute: DEFAULT_MINUTE };
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59) return { hour: DEFAULT_HOUR, minute: DEFAULT_MINUTE };
  return { hour, minute };
}

function clampInt(n: number, min: number, max: number): number {
  if (!Number.isFinite(n)) return min;
  return Math.min(max, Math.max(min, Math.trunc(n)));
}

/**
 * Derive the cron string for a builder state. Total by construction: an
 * invalid/cleared time falls back to 09:00 and out-of-range day picks are
 * clamped, so the derived cron is never empty and never malformed.
 */
export function builderToCron(shape: CronBuilderShape): string {
  const { hour, minute } = parseTime(shape.time);
  const at = `${minute} ${hour}`;
  switch (shape.repeat) {
    case "daily":
      return `${at} * * *`;
    case "weekdays":
      return `${at} * * 1-5`;
    case "weekly": {
      const weekday = clampInt(shape.weekday ?? 1, 0, 6);
      return `${at} * * ${weekday}`;
    }
    case "monthly": {
      const monthday = clampInt(shape.monthday ?? 1, 1, 28);
      return `${at} ${monthday} * *`;
    }
    case "interval": {
      // The "every N" shapes ignore the time control entirely.
      if ((shape.unit ?? "minutes") === "hours") {
        const every = clampInt(shape.every ?? 1, 1, 23);
        return every === 1 ? "0 * * * *" : `0 */${every} * * *`;
      }
      return `*/${clampInt(shape.every ?? 30, 1, 59)} * * * *`;
    }
    default:
      return `${at} * * *`;
  }
}

const CUSTOM: CronBuilder = {
  every: 30,
  monthday: 1,
  repeat: "custom",
  time: "09:00",
  unit: "minutes",
  weekday: 1,
};

/**
 * Best-effort inverse of `builderToCron`, recognising exactly the shapes it
 * emits: the interval steps (`*\/N * * * *`, `0 *\/N * * *`, `0 * * * *`) and
 * the single numeric minute+hour shapes (month always `*`). Anything else —
 * other steps, lists, ranges beyond `1-5`, multiple fields — maps to `custom`
 * with neutral defaults for the unused controls; the caller keeps the raw
 * string.
 */
export function cronToBuilder(cron: string): CronBuilder {
  const parts = cron.trim().split(/\s+/);
  if (parts.length !== 5) return CUSTOM;
  const [min = "", hour = "", dom = "", mon = "", dow = ""] = parts;
  if (mon !== "*") return CUSTOM;

  // Interval shapes come first: their minute/hour fields are steps, not
  // numerics, so the fixed-time parse below would file them under custom.
  if (dom === "*" && dow === "*") {
    const minuteStep = /^\*\/(\d{1,2})$/.exec(min);
    if (minuteStep && hour === "*") {
      const every = Number(minuteStep[1]);
      if (every >= 1 && every <= 59)
        return { ...CUSTOM, every, repeat: "interval", unit: "minutes" };
      return CUSTOM;
    }
    if (min === "0") {
      if (hour === "*") return { ...CUSTOM, every: 1, repeat: "interval", unit: "hours" };
      const hourStep = /^\*\/(\d{1,2})$/.exec(hour);
      if (hourStep) {
        const every = Number(hourStep[1]);
        if (every >= 1 && every <= 23)
          return { ...CUSTOM, every, repeat: "interval", unit: "hours" };
        return CUSTOM;
      }
    }
  }

  if (!/^\d{1,2}$/.test(min) || !/^\d{1,2}$/.test(hour)) return CUSTOM;
  const minute = Number(min);
  const hourNum = Number(hour);
  if (minute > 59 || hourNum > 23) return CUSTOM;
  const time = `${String(hourNum).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
  const base = { ...CUSTOM, time };

  if (dom === "*") {
    if (dow === "*") return { ...base, repeat: "daily" };
    if (dow === "1-5") return { ...base, repeat: "weekdays" };
    if (/^[0-6]$/.test(dow)) return { ...base, repeat: "weekly", weekday: Number(dow) };
    return CUSTOM;
  }
  if (dow === "*" && /^\d{1,2}$/.test(dom)) {
    const monthday = Number(dom);
    if (monthday >= 1 && monthday <= 28) return { ...base, monthday, repeat: "monthly" };
  }
  return CUSTOM;
}

const CRON_DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** "1st", "2nd", "3rd", "4th", … */
export function ordinal(n: number): string {
  const rem10 = n % 10;
  const rem100 = n % 100;
  if (rem10 === 1 && rem100 !== 11) return `${n}st`;
  if (rem10 === 2 && rem100 !== 12) return `${n}nd`;
  if (rem10 === 3 && rem100 !== 13) return `${n}rd`;
  return `${n}th`;
}

function formatClock(hour: number, minute: number): string {
  const period = hour < 12 ? "AM" : "PM";
  const h12 = hour % 12 === 0 ? 12 : hour % 12;
  return `${h12}:${String(minute).padStart(2, "0")} ${period}`;
}

/**
 * Render a standard 5-field cron expression in plain English for the common
 * shapes scheduled tasks use (daily/weekly at a time, every N hours or
 * minutes). Anything it doesn't recognise falls back to the raw expression,
 * so the exact cron stays visible.
 */
export function describeCron(expr: string): string {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return expr;
  const [min = "", hour = "", dom = "", mon = "", dow = ""] = parts;
  const everyHour = /^\*\/(\d+)$/.exec(hour);
  const everyMin = /^\*\/(\d+)$/.exec(min);
  const numMin = /^\d+$/.test(min);
  const numHour = /^\d+$/.test(hour);

  if (everyMin && hour === "*" && dom === "*" && mon === "*" && dow === "*") {
    return Number(everyMin[1]) === 1 ? "Every minute" : `Every ${everyMin[1]} minutes`;
  }
  if (min === "0" && hour === "*" && dom === "*" && mon === "*" && dow === "*") {
    return "Every hour";
  }
  if (everyHour && numMin && dom === "*" && mon === "*" && dow === "*") {
    return Number(everyHour[1]) === 1 ? "Every hour" : `Every ${everyHour[1]} hours`;
  }
  if (numMin && numHour && mon === "*") {
    const time = formatClock(Number(hour), Number(min));
    if (dom === "*") {
      if (dow === "*") return `Daily at ${time}`;
      if (dow === "1-5") return `Weekdays at ${time}`;
      if (/^[0-6]$/.test(dow)) return `Weekly on ${CRON_DAYS[Number(dow)]} at ${time}`;
    } else if (dow === "*" && /^\d{1,2}$/.test(dom)) {
      const day = Number(dom);
      if (day >= 1 && day <= 31) return `Monthly on the ${ordinal(day)} at ${time}`;
    }
  }
  return expr;
}
