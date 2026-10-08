// SPDX-License-Identifier: Apache-2.0

/**
 * Natural-language → trigger compiler for the schedule form's "Describe the
 * schedule" input.
 *
 * It is a small ORDERED pattern table, not an NLP engine: anchored,
 * case-insensitive regexes tried in the same order as the table, first
 * match wins. A phrase that matches no row — or matches one but carries an
 * out-of-range value ("every 60 minutes") — returns `null`; it never throws.
 * The caller then falls back to treating five-field input as a raw cron
 * expression (`looksLikeCron`).
 *
 * The compiled value is an authoring aid only. The wire still carries the
 * cron string / one-shot instant (the daemon owns cron grammar and
 * cadence-floor validation); nothing here talks to the daemon.
 */

export type SchedulePhraseResult =
  | { cron: string; kind: "cron" }
  | { at: number; kind: "one-shot" };

/**
 * Cron day-of-week numbers. Sunday is 0 here (not 7) so a compiled Sunday
 * schedule lands on the `cronToBuilder`/`describeCron` Weekly/Sunday shape
 * (`0 6 * * 0`) instead of falling through to Custom, which only accepts
 * `[0-6]`.
 */
const WEEKDAY_NUMBER: Record<string, number> = {
  friday: 5,
  monday: 1,
  saturday: 6,
  sunday: 0,
  thursday: 4,
  tuesday: 2,
  wednesday: 3,
};

const DOW = "(monday|tuesday|wednesday|thursday|friday|saturday|sunday)";

/**
 * One capture group for a clock: `H`, `H:MM`, with an optional `am`/`pm`
 * (whitespace allowed before it), or the words `noon`/`midnight`.
 */
const CLOCK = String.raw`(\d{1,2}(?::\d{2})?\s*(?:am|pm)?|noon|midnight)`;

/** Parse a `CLOCK` capture into 24-hour hour/minute; null when out of range. */
function parseClock(text: string): { hour: number; minute: number } | null {
  const word = text.trim().toLowerCase();
  if (word === "noon") return { hour: 12, minute: 0 };
  if (word === "midnight") return { hour: 0, minute: 0 };
  const match = /^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$/i.exec(word);
  if (!match) return null;
  let hour = Number(match[1]);
  const minute = match[2] === undefined ? 0 : Number(match[2]);
  const meridian = match[3]?.toLowerCase();
  if (meridian === "am") {
    // 12-hour clock: 12am is midnight.
    if (hour === 12) hour = 0;
  } else if (meridian === "pm") {
    if (hour !== 12) hour += 12;
  }
  if (hour < 0 || hour > 23 || minute < 0 || minute > 59) return null;
  return { hour, minute };
}

/**
 * The next local instant (>= now) on weekday `dow` (cron numbering, 0 =
 * Sunday) at hour:minute. Today counts only while the time is still strictly
 * in the future — "next wednesday 9am" said on a Wednesday at 10:00 means
 * next week, as does saying it at exactly 09:00. Day arithmetic goes through
 * `setDate` so a DST change between now and the target keeps the wall-clock
 * time.
 */
function nextWeekdayTime(now: Date, dow: number, hour: number, minute: number): Date {
  const target = new Date(now.getFullYear(), now.getMonth(), now.getDate(), hour, minute, 0, 0);
  let days = (dow - target.getDay() + 7) % 7;
  if (days === 0 && target.getTime() <= now.getTime()) days = 7;
  target.setDate(target.getDate() + days);
  return target;
}

interface Row {
  build: (now: Date, m: RegExpExecArray) => SchedulePhraseResult | null;
  re: RegExp;
}

/** Build the anchored, case-insensitive regex for one table row. */
function pattern(src: string): RegExp {
  return new RegExp(String.raw`^\s*${src}\s*$`, "i");
}

function inRange(n: number, min: number, max: number): boolean {
  return Number.isInteger(n) && n >= min && n <= max;
}

const UNIT_MS: Record<string, number> = {
  day: 86_400_000,
  hour: 3_600_000,
  minute: 60_000,
  week: 7 * 86_400_000,
};

/** The ordered table — first match wins. */
const TABLE: Row[] = [
  // every N minutes → */N * * * *
  {
    build: (_now, m) => {
      const n = Number(m[1]);
      return inRange(n, 1, 59) ? { cron: `*/${n} * * * *`, kind: "cron" } : null;
    },
    re: pattern(String.raw`every (\d+) minutes?`),
  },
  // every N hours → 0 */N * * *
  {
    build: (_now, m) => {
      const n = Number(m[1]);
      return inRange(n, 1, 23) ? { cron: `0 */${n} * * *`, kind: "cron" } : null;
    },
    re: pattern(String.raw`every (\d+) hours?`),
  },
  // every hour → 0 * * * *
  { build: () => ({ cron: "0 * * * *", kind: "cron" }), re: pattern("every hour") },
  // daily at H[:MM] [am|pm] → M H * * *
  {
    build: (_now, m) => {
      const clock = parseClock(m[1] ?? "");
      return clock ? { cron: `${clock.minute} ${clock.hour} * * *`, kind: "cron" } : null;
    },
    re: pattern(`daily at ${CLOCK}`),
  },
  // every weekday at H[:MM] [am|pm] → M H * * 1-5
  {
    build: (_now, m) => {
      const clock = parseClock(m[1] ?? "");
      return clock ? { cron: `${clock.minute} ${clock.hour} * * 1-5`, kind: "cron" } : null;
    },
    re: pattern(`every weekday at ${CLOCK}`),
  },
  // every <dow> at H[:MM] [am|pm] → M H * * D
  {
    build: (_now, m) => {
      const dow = WEEKDAY_NUMBER[(m[1] ?? "").toLowerCase()];
      const clock = parseClock(m[2] ?? "");
      if (dow === undefined || !clock) return null;
      return { cron: `${clock.minute} ${clock.hour} * * ${dow}`, kind: "cron" };
    },
    re: pattern(`every ${DOW} at ${CLOCK}`),
  },
  // next <dow> H[:MM] [am|pm] → one-shot
  {
    build: (now, m) => {
      const dow = WEEKDAY_NUMBER[(m[1] ?? "").toLowerCase()];
      const clock = parseClock(m[2] ?? "");
      if (dow === undefined || !clock) return null;
      return {
        at: nextWeekdayTime(now, dow, clock.hour, clock.minute).getTime(),
        kind: "one-shot",
      };
    },
    re: pattern(String.raw`next ${DOW}\s+(?:at\s+)?${CLOCK}`),
  },
  // in N (minute|hour|day|week)s → one-shot, now + duration
  {
    build: (now, m) => {
      const n = Number(m[1]);
      if (!Number.isInteger(n) || n <= 0) return null;
      const ms = UNIT_MS[(m[2] ?? "").toLowerCase()];
      if (ms === undefined) return null;
      return { at: now.getTime() + n * ms, kind: "one-shot" };
    },
    re: pattern(String.raw`in (\d+) (minute|hour|day|week)s?`),
  },
  // tomorrow at H[:MM] [am|pm] → one-shot, tomorrow at that local time
  {
    build: (now, m) => {
      const clock = parseClock(m[1] ?? "");
      if (!clock) return null;
      const at = new Date(
        now.getFullYear(),
        now.getMonth(),
        now.getDate() + 1,
        clock.hour,
        clock.minute,
        0,
        0,
      );
      return { at: at.getTime(), kind: "one-shot" };
    },
    re: pattern(`tomorrow at ${CLOCK}`),
  },
];

/**
 * Compile a scheduling phrase into a trigger value, or `null` when no row
 * matches or a matched row's value is out of range. `now` anchors the
 * one-shot rows ("next monday 3pm", "in 2 hours", "tomorrow at 8am"); the
 * recurring rows ignore it.
 */
export function compileSchedulePhrase(
  input: string,
  now: Date = new Date(),
): SchedulePhraseResult | null {
  const text = input.trim();
  if (!text) return null;
  for (const row of TABLE) {
    const m = row.re.exec(text);
    if (m) return row.build(now, m);
  }
  return null;
}

/**
 * The raw-cron fallback test: five whitespace-separated fields. The minute
 * and hour fields must be digits/cron symbols only (a real cron never
 * spells those with letters) so a five-word sentence is not mistaken for a
 * cron; the day/month fields may carry names (`MON-FRI`, `JAN`). Grammar
 * beyond that stays the daemon's call.
 */
export function looksLikeCron(input: string): boolean {
  const fields = input.trim().split(/\s+/);
  if (fields.length !== 5 || fields.some((f) => f === "")) return false;
  const numeric = /^[\d*,\-/]+$/;
  const named = /^[\dA-Za-z*,\-/?#]+$/;
  return (
    numeric.test(fields[0] ?? "") &&
    numeric.test(fields[1] ?? "") &&
    fields.slice(2).every((f) => named.test(f))
  );
}

/**
 * A ms epoch → the browser-local `datetime-local` input value
 * ("YYYY-MM-DDTHH:MM"). Shared by the schedule form's edit seeding and the
 * phrase compiler's one-shot hand-off, so both agree on the local rendering.
 */
export function toLocalDateTimeInput(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * A `datetime-local` value holds a wall clock with no offset, so at a
 * daylight-saving fall-back transition it names two different instants and at
 * a spring-forward gap it names none. These helpers make the choice explicit
 * and keep it out of the API, which only ever carries an absolute instant.
 *
 * `instantFromLocalInput` resolves a wall clock the way the platform does:
 * the earlier occurrence when a local time happens twice, and the first valid
 * instant after the gap when it does not exist.
 */
export function instantFromLocalInput(local: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/u.test(local)) return null;
  const resolved = new Date(local);
  return Number.isNaN(resolved.getTime()) ? null : resolved.toISOString();
}

/**
 * The instant to save for a one-shot trigger.
 *
 * While the displayed wall clock still renders the schedule's original
 * instant, that original is returned unchanged. Without this, editing an
 * unrelated field on a schedule whose instant is the SECOND occurrence of an
 * ambiguous local time would silently move the fire an hour earlier, because
 * the wall clock alone cannot say which occurrence it meant.
 */
export function oneShotInstant(local: string, original?: string): string | null {
  if (
    original !== undefined &&
    original !== "" &&
    toLocalDateTimeInput(new Date(original).getTime()) === local
  ) {
    return original;
  }
  return instantFromLocalInput(local);
}
