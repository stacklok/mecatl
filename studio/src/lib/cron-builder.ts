/**
 * The structured cron editor's derivation layer.
 *
 * The schedule form keeps carrying the cron STRING on the wire (rule 10 —
 * the daemon owns cron grammar validation); this module is purely a nicer
 * editor for that string. `builderToCron` derives the string from the
 * builder's controls, and `cronToBuilder` is its best-effort inverse: it
 * recognises exactly the shapes the builder can produce and files everything
 * else (steps, lists, exotic ranges) under `custom`, where the raw string is
 * edited directly and preserved byte-for-byte.
 */

export type CronRepeat =
  | "daily"
  | "weekdays"
  | "weekly"
  | "monthly"
  | "interval"
  | "custom";

/** The unit an `interval` repeat counts in. */
export type CronIntervalUnit = "minutes" | "hours";

/** The builder's parsed view of a cron string. */
export interface CronBuilder {
  repeat: CronRepeat;
  /** "HH:MM", 24-hour — the `<input type="time">` value. */
  time: string;
  /** Cron day-of-week for `weekly` (0 = Sunday … 6 = Saturday). */
  weekday: number;
  /** Day of month for `monthly` (1–28 — the days every month has). */
  monthday: number;
  /** Step for `interval` (1–59 minutes or 1–23 hours). */
  every: number;
  /** Unit for `interval`. */
  unit: CronIntervalUnit;
}

/** What `builderToCron` needs; `custom` never derives (the raw string wins). */
export interface CronBuilderShape {
  repeat: Exclude<CronRepeat, "custom">;
  time: string;
  weekday?: number;
  monthday?: number;
  every?: number;
  unit?: CronIntervalUnit;
}

/** The builder can never derive an empty cron: a blank time falls back here. */
const DEFAULT_HOUR = 9;
const DEFAULT_MINUTE = 0;

function parseTime(time: string): { hour: number; minute: number } {
  const match = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
  if (!match) return { hour: DEFAULT_HOUR, minute: DEFAULT_MINUTE };
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59)
    return { hour: DEFAULT_HOUR, minute: DEFAULT_MINUTE };
  return { hour, minute };
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
  }
}

function clampInt(n: number, min: number, max: number): number {
  if (!Number.isFinite(n)) return min;
  return Math.min(max, Math.max(min, Math.trunc(n)));
}

const CUSTOM: CronBuilder = {
  repeat: "custom",
  time: "09:00",
  weekday: 1,
  monthday: 1,
  every: 30,
  unit: "minutes",
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
  const [min, hour, dom, mon, dow] = parts;
  if (mon !== "*") return CUSTOM;

  // Interval shapes come first: their minute/hour fields are steps, not
  // numerics, so the fixed-time parse below would file them under custom.
  if (dom === "*" && dow === "*") {
    const minuteStep = /^\*\/(\d{1,2})$/.exec(min);
    if (minuteStep && hour === "*") {
      const every = Number(minuteStep[1]);
      if (every >= 1 && every <= 59)
        return { ...CUSTOM, repeat: "interval", every, unit: "minutes" };
      return CUSTOM;
    }
    if (min === "0") {
      if (hour === "*")
        return { ...CUSTOM, repeat: "interval", every: 1, unit: "hours" };
      const hourStep = /^\*\/(\d{1,2})$/.exec(hour);
      if (hourStep) {
        const every = Number(hourStep[1]);
        if (every >= 1 && every <= 23)
          return { ...CUSTOM, repeat: "interval", every, unit: "hours" };
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
    if (/^[0-6]$/.test(dow))
      return { ...base, repeat: "weekly", weekday: Number(dow) };
    return CUSTOM;
  }
  if (dow === "*" && /^\d{1,2}$/.test(dom)) {
    const monthday = Number(dom);
    if (monthday >= 1 && monthday <= 28)
      return { ...base, repeat: "monthly", monthday };
  }
  return CUSTOM;
}
