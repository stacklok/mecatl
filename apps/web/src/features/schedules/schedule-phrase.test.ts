// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  compileSchedulePhrase,
  instantFromLocalInput,
  looksLikeCron,
  oneShotInstant,
  toLocalDateTimeInput,
} from "./schedule-phrase";

// A Wednesday, 10:00 local.
const now = new Date(2026, 8, 16, 10, 0);

function local(year: number, monthIndex: number, day: number, hour: number, minute = 0): number {
  return new Date(year, monthIndex, day, hour, minute, 0, 0).getTime();
}

describe("compileSchedulePhrase recurring rows", () => {
  it.each([
    ["every 30 minutes", "*/30 * * * *"],
    ["every 1 minute", "*/1 * * * *"],
    ["every 2 hours", "0 */2 * * *"],
    ["every 1 hour", "0 */1 * * *"],
    ["every hour", "0 * * * *"],
    ["daily at 9am", "0 9 * * *"],
    ["daily at 9 am", "0 9 * * *"],
    ["daily at 17:30", "30 17 * * *"],
    ["daily at 9:15pm", "15 21 * * *"],
    ["daily at noon", "0 12 * * *"],
    ["daily at midnight", "0 0 * * *"],
    ["every weekday at 9:15pm", "15 21 * * 1-5"],
    ["every weekday at 9am", "0 9 * * 1-5"],
    ["every monday at 9am", "0 9 * * 1"],
    ["every friday at 8am", "0 8 * * 5"],
    ["every saturday at 8am", "0 8 * * 6"],
    // Sunday is 0, not 7, so the builder/describer recognise it.
    ["every sunday at 6am", "0 6 * * 0"],
  ])("%j → %j", (phrase, cron) => {
    expect(compileSchedulePhrase(phrase, now)).toEqual({ cron, kind: "cron" });
  });

  it("normalises the 12-hour clock: 12am is 0h, 12pm is 12h", () => {
    expect(compileSchedulePhrase("daily at 12am", now)).toEqual({
      cron: "0 0 * * *",
      kind: "cron",
    });
    expect(compileSchedulePhrase("daily at 12pm", now)).toEqual({
      cron: "0 12 * * *",
      kind: "cron",
    });
    expect(compileSchedulePhrase("daily at 12:30am", now)).toEqual({
      cron: "30 0 * * *",
      kind: "cron",
    });
  });

  it("ignores case and surrounding whitespace", () => {
    expect(compileSchedulePhrase("  EVERY 5 Minutes ", now)).toEqual({
      cron: "*/5 * * * *",
      kind: "cron",
    });
    expect(compileSchedulePhrase("Daily At 9AM", now)).toEqual({ cron: "0 9 * * *", kind: "cron" });
    expect(compileSchedulePhrase("Every SUNDAY at Noon", now)).toEqual({
      cron: "0 12 * * 0",
      kind: "cron",
    });
  });
});

describe("compileSchedulePhrase one-shot rows", () => {
  it("next <dow> rolls a week forward when today's time has passed", () => {
    // 09:00 today (Wednesday) is already behind a 10:00 now → next Wednesday.
    expect(compileSchedulePhrase("next wednesday 9am", now)).toEqual({
      at: local(2026, 8, 23, 9),
      kind: "one-shot",
    });
  });

  it("next <dow> keeps today when the time is still ahead", () => {
    expect(compileSchedulePhrase("next wednesday 11am", now)).toEqual({
      at: local(2026, 8, 23 - 7, 11),
      kind: "one-shot",
    });
  });

  it("next <dow> at exactly now rolls a week forward (not strictly in the future)", () => {
    expect(compileSchedulePhrase("next wednesday 10:00", now)).toEqual({
      at: local(2026, 8, 23, 10),
      kind: "one-shot",
    });
  });

  it("next <dow> lands on the following occurrence of another weekday", () => {
    expect(compileSchedulePhrase("next monday 3pm", now)).toEqual({
      at: local(2026, 8, 21, 15),
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("next sunday 6:30am", now)).toEqual({
      at: local(2026, 8, 20, 6, 30),
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("next tuesday noon", now)).toEqual({
      at: local(2026, 8, 22, 12),
      kind: "one-shot",
    });
  });

  it("in N units adds the duration to now", () => {
    const t = now.getTime();
    expect(compileSchedulePhrase("in 2 hours", now)).toEqual({
      at: t + 2 * 3_600_000,
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("in 45 minutes", now)).toEqual({
      at: t + 45 * 60_000,
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("in 1 minute", now)).toEqual({
      at: t + 60_000,
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("in 3 days", now)).toEqual({
      at: t + 3 * 86_400_000,
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("in 1 week", now)).toEqual({
      at: t + 7 * 86_400_000,
      kind: "one-shot",
    });
  });

  it("tomorrow at lands on tomorrow's local date at that time", () => {
    expect(compileSchedulePhrase("tomorrow at 8am", now)).toEqual({
      at: local(2026, 8, 17, 8),
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("tomorrow at noon", now)).toEqual({
      at: local(2026, 8, 17, 12),
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("tomorrow at midnight", now)).toEqual({
      at: local(2026, 8, 17, 0),
      kind: "one-shot",
    });
    expect(compileSchedulePhrase("tomorrow at 17:45", now)).toEqual({
      at: local(2026, 8, 17, 17, 45),
      kind: "one-shot",
    });
  });

  it("tomorrow crosses a month boundary by calendar day", () => {
    const endOfMonth = new Date(2026, 8, 30, 22, 0);
    expect(compileSchedulePhrase("tomorrow at 8am", endOfMonth)).toEqual({
      at: local(2026, 9, 1, 8),
      kind: "one-shot",
    });
  });

  it("defaults now to the current instant", () => {
    const before = Date.now();
    const result = compileSchedulePhrase("in 1 hour");
    const after = Date.now();
    expect(result?.kind).toBe("one-shot");
    if (result?.kind !== "one-shot") throw new Error("expected one-shot");
    expect(result.at).toBeGreaterThanOrEqual(before + 3_600_000);
    expect(result.at).toBeLessThanOrEqual(after + 3_600_000);
  });
});

describe("compileSchedulePhrase rejections", () => {
  it.each([
    "",
    "   ",
    "gibberish",
    "every 0 minutes", // below the range floor
    "every 60 minutes", // a step of 60 is not a minute cadence
    "every 24 hours", // hours are 1–23
    "every 0 hours",
    "daily at 25", // no such hour
    "daily at 9:75", // no such minute
    "daily at 13pm", // 13 on a 12-hour clock
    "every funday at 9am", // not a weekday name
    "next someday 9am",
    "in 0 hours", // a zero delay is not a schedule
    "in 2 fortnights", // unknown unit
    "tomorrow at 24:00",
    "0 9 * * *", // raw cron is the caller's fallback, not a phrase
  ])("%j → null", (phrase) => {
    expect(compileSchedulePhrase(phrase, now)).toBeNull();
  });

  it("never throws on odd input", () => {
    for (const odd of ["every", "at", "next", "in", "tomorrow", " "]) {
      expect(() => compileSchedulePhrase(odd, now)).not.toThrow();
      expect(compileSchedulePhrase(odd, now)).toBeNull();
    }
  });
});

describe("looksLikeCron", () => {
  it("accepts five cron-shaped fields", () => {
    expect(looksLikeCron("0 9 * * 1,3,5")).toBe(true);
    expect(looksLikeCron("*/15 * * * *")).toBe(true);
    expect(looksLikeCron("  30 17 * * 1-5 ")).toBe(true);
    expect(looksLikeCron("0 9 * * MON-FRI")).toBe(true);
    expect(looksLikeCron("0 0 1 JAN *")).toBe(true);
  });

  it("rejects other field counts and prose", () => {
    expect(looksLikeCron("0 9 * *")).toBe(false);
    expect(looksLikeCron("0 9 * * * *")).toBe(false);
    expect(looksLikeCron("")).toBe(false);
    expect(looksLikeCron("every 30 minutes")).toBe(false);
    // Five words are not a cron: minute/hour never carry letters.
    expect(looksLikeCron("run it at nine daily")).toBe(false);
    expect(looksLikeCron("please run at 9 daily")).toBe(false);
  });
});

describe("toLocalDateTimeInput", () => {
  it("renders the browser-local datetime-local value", () => {
    expect(toLocalDateTimeInput(local(2026, 8, 17, 8))).toBe("2026-09-17T08:00");
    expect(toLocalDateTimeInput(local(2026, 0, 5, 23, 7))).toBe("2026-01-05T23:07");
  });

  it("round-trips the compiler's one-shot instant to the minute", () => {
    const result = compileSchedulePhrase("tomorrow at 8am", now);
    if (result?.kind !== "one-shot") throw new Error("expected one-shot");
    expect(toLocalDateTimeInput(result.at)).toMatch(/^\d{4}-\d{2}-\d{2}T08:00$/);
  });
});

/**
 * A `datetime-local` value carries no offset, so a wall clock is ambiguous at
 * a fall-back transition and impossible at a spring-forward gap. These pin the
 * resolution rules in a fixed zone: Europe/Rome falls back on 2026-10-25
 * (02:30 happens twice) and springs forward on 2026-03-29 (02:30 never
 * happens).
 */
describe("one-shot instant resolution across daylight saving", () => {
  const original = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = "Europe/Rome";
  });
  afterAll(() => {
    process.env.TZ = original;
  });

  it("keeps the original instant when the displayed local value is unedited", () => {
    // 01:30Z is the SECOND occurrence of 02:30 local; its wall clock alone
    // cannot say so, and re-resolving it would move the fire an hour earlier.
    const second = "2026-10-25T01:30:00.000Z";
    const shown = toLocalDateTimeInput(new Date(second).getTime());
    expect(shown).toBe("2026-10-25T02:30");
    expect(oneShotInstant(shown, second)).toBe(second);
    // Without the original, the same wall clock resolves to the first occurrence.
    expect(oneShotInstant(shown)).toBe("2026-10-25T00:30:00.000Z");
    // The first occurrence round-trips either way.
    const first = "2026-10-25T00:30:00.000Z";
    expect(oneShotInstant(toLocalDateTimeInput(new Date(first).getTime()), first)).toBe(first);
    // An edit away from the original is resolved, not preserved.
    expect(oneShotInstant("2026-10-26T02:30", second)).toBe("2026-10-26T01:30:00.000Z");
  });

  it("resolves an ambiguous fall-back local time to the earlier occurrence", () => {
    // Both instants render the same wall clock; the earlier one is chosen.
    expect(instantFromLocalInput("2026-10-25T02:30")).toBe("2026-10-25T00:30:00.000Z");
    expect(toLocalDateTimeInput(new Date("2026-10-25T00:30:00.000Z").getTime())).toBe(
      "2026-10-25T02:30",
    );
    expect(toLocalDateTimeInput(new Date("2026-10-25T01:30:00.000Z").getTime())).toBe(
      "2026-10-25T02:30",
    );
    // An unambiguous time either side is unaffected.
    expect(instantFromLocalInput("2026-10-25T04:00")).toBe("2026-10-25T03:00:00.000Z");
  });

  it("canonicalises an edited later-occurrence instant to the earlier one", () => {
    // The documented rule, stated as its own proof so nobody reads AC4.5's
    // preservation as a general round-trip: a schedule stored at the SECOND
    // occurrence of an ambiguous wall clock moves to the FIRST once its wall
    // clock is edited or reconfirmed, because the wall clock alone cannot
    // name the later one.
    const later = "2026-10-25T01:30:00.000Z";
    const earlier = "2026-10-25T00:30:00.000Z";
    const shown = toLocalDateTimeInput(new Date(later).getTime());
    expect(shown).toBe("2026-10-25T02:30");

    // Unedited: preserved (AC4.5).
    expect(oneShotInstant(shown, later)).toBe(later);

    // Edited to the same wall clock, or resolved without the original:
    // canonicalised to the earlier occurrence, never restored to the later.
    expect(instantFromLocalInput(shown)).toBe(earlier);
    expect(oneShotInstant(shown, undefined)).toBe(earlier);
    // Editing away and back lands on the canonical instant, not the original.
    const elsewhere = oneShotInstant("2026-10-26T02:30", later);
    expect(elsewhere).toBe("2026-10-26T01:30:00.000Z");
    expect(oneShotInstant(shown, elsewhere as string)).toBe(earlier);
  });

  it("resolves a nonexistent spring-forward local time to the first instant after the gap", () => {
    // 02:30 does not exist on 2026-03-29: the clock jumps 02:00 to 03:00.
    const resolved = instantFromLocalInput("2026-03-29T02:30");
    expect(resolved).toBe("2026-03-29T01:30:00.000Z");
    // It lands after the gap, at 03:30 local.
    expect(toLocalDateTimeInput(new Date(resolved as string).getTime())).toBe("2026-03-29T03:30");
    // A malformed or empty value resolves to nothing rather than a wrong instant.
    expect(instantFromLocalInput("")).toBeNull();
    expect(instantFromLocalInput("2026-03-29")).toBeNull();
    expect(instantFromLocalInput("not-a-time")).toBeNull();
  });
});

describe("weekday phrases accept the optional at token", () => {
  it("parses next <weekday> at <time> as well as next <weekday> <time>", () => {
    const at = compileSchedulePhrase("next monday at 3pm", new Date("2026-09-22T08:00:00.000Z"));
    const bare = compileSchedulePhrase("next monday 3pm", new Date("2026-09-22T08:00:00.000Z"));
    expect(at).not.toBeNull();
    expect(at).toEqual(bare);
    // Case and spacing are as forgiving as the bare form.
    expect(
      compileSchedulePhrase("NEXT Monday   at   3 pm", new Date("2026-09-22T08:00:00.000Z")),
    ).toEqual(bare);
  });
});
