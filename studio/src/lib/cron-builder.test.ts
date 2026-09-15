import { describe, expect, it } from "vitest";
import {
  builderToCron,
  type CronBuilderShape,
  cronToBuilder,
} from "./cron-builder";

describe("builderToCron", () => {
  it("derives each repeat shape", () => {
    expect(builderToCron({ repeat: "daily", time: "09:00" })).toBe("0 9 * * *");
    expect(builderToCron({ repeat: "weekdays", time: "17:30" })).toBe(
      "30 17 * * 1-5",
    );
    expect(builderToCron({ repeat: "weekly", time: "08:15", weekday: 5 })).toBe(
      "15 8 * * 5",
    );
    expect(builderToCron({ repeat: "weekly", time: "00:00", weekday: 0 })).toBe(
      "0 0 * * 0",
    );
    expect(
      builderToCron({ repeat: "monthly", time: "23:59", monthday: 28 }),
    ).toBe("59 23 28 * *");
  });

  it("defaults a missing weekday/monthday", () => {
    expect(builderToCron({ repeat: "weekly", time: "09:00" })).toBe(
      "0 9 * * 1",
    );
    expect(builderToCron({ repeat: "monthly", time: "09:00" })).toBe(
      "0 9 1 * *",
    );
  });

  it("never derives an empty or malformed cron from a blank/invalid time", () => {
    expect(builderToCron({ repeat: "daily", time: "" })).toBe("0 9 * * *");
    expect(builderToCron({ repeat: "daily", time: "nonsense" })).toBe(
      "0 9 * * *",
    );
    expect(builderToCron({ repeat: "daily", time: "25:00" })).toBe("0 9 * * *");
    expect(builderToCron({ repeat: "daily", time: "12:75" })).toBe("0 9 * * *");
  });

  it("clamps out-of-range day picks instead of emitting them", () => {
    expect(builderToCron({ repeat: "weekly", time: "09:00", weekday: 9 })).toBe(
      "0 9 * * 6",
    );
    expect(
      builderToCron({ repeat: "monthly", time: "09:00", monthday: 31 }),
    ).toBe("0 9 28 * *");
    expect(
      builderToCron({ repeat: "monthly", time: "09:00", monthday: 0 }),
    ).toBe("0 9 1 * *");
  });
});

describe("cronToBuilder", () => {
  it("recognises the four builder shapes", () => {
    expect(cronToBuilder("0 9 * * *")).toEqual({
      repeat: "daily",
      time: "09:00",
      weekday: 1,
      monthday: 1,
    });
    expect(cronToBuilder("30 17 * * 1-5")).toMatchObject({
      repeat: "weekdays",
      time: "17:30",
    });
    expect(cronToBuilder("15 8 * * 5")).toMatchObject({
      repeat: "weekly",
      time: "08:15",
      weekday: 5,
    });
    expect(cronToBuilder("0 0 * * 0")).toMatchObject({
      repeat: "weekly",
      weekday: 0,
    });
    expect(cronToBuilder("59 23 28 * *")).toMatchObject({
      repeat: "monthly",
      time: "23:59",
      monthday: 28,
    });
  });

  it("tolerates padded numerics and extra whitespace", () => {
    expect(cronToBuilder("  00  09  *  *  * ")).toMatchObject({
      repeat: "daily",
      time: "09:00",
    });
  });

  const exotic = [
    "*/5 * * * *", // step minute
    "0 */2 * * *", // step hour
    "0 9 * * 1,3,5", // day list
    "0 9 * * 2-4", // range other than 1-5
    "0 9 * * 7", // out-of-range weekday
    "0 9 1 1 *", // pinned month
    "0 9 29 * *", // monthday beyond 28
    "0 9 1-5 * *", // day-of-month range
    "0 9,17 * * *", // hour list
    "60 9 * * *", // out-of-range minute
    "0 24 * * *", // out-of-range hour
    "0 9 * *", // four fields
    "0 9 * * * *", // six fields
    "", // empty
    "@daily", // macro
  ];
  it.each(exotic)("files %j under custom", (cron) => {
    expect(cronToBuilder(cron).repeat).toBe("custom");
  });

  it("returns neutral control defaults for custom", () => {
    expect(cronToBuilder("*/5 * * * *")).toEqual({
      repeat: "custom",
      time: "09:00",
      weekday: 1,
      monthday: 1,
    });
  });
});

describe("round-trips", () => {
  const shapes: CronBuilderShape[] = [
    { repeat: "daily", time: "09:00" },
    { repeat: "daily", time: "00:00" },
    { repeat: "daily", time: "23:59" },
    { repeat: "weekdays", time: "06:45" },
    ...Array.from({ length: 7 }, (_, weekday) => ({
      repeat: "weekly" as const,
      time: "12:30",
      weekday,
    })),
    ...[1, 2, 14, 27, 28].map((monthday) => ({
      repeat: "monthly" as const,
      time: "07:05",
      monthday,
    })),
  ];

  it.each(shapes)("builder → cron → builder is lossless for %j", (shape) => {
    const cron = builderToCron(shape);
    const back = cronToBuilder(cron);
    expect(back.repeat).toBe(shape.repeat);
    expect(back.time).toBe(shape.time);
    if (shape.repeat === "weekly") expect(back.weekday).toBe(shape.weekday);
    if (shape.repeat === "monthly") expect(back.monthday).toBe(shape.monthday);
    // And the derived string is stable through a second derivation.
    if (back.repeat === "custom") throw new Error("round-trip lost shape");
    expect(builderToCron({ ...back, repeat: back.repeat })).toBe(cron);
  });

  it("cron → builder → cron is stable for builder-shaped strings", () => {
    for (const cron of [
      "0 9 * * *",
      "30 17 * * 1-5",
      "15 8 * * 5",
      "59 23 28 * *",
    ]) {
      const parsed = cronToBuilder(cron);
      if (parsed.repeat === "custom")
        throw new Error(`unexpected custom for ${cron}`);
      expect(builderToCron({ ...parsed, repeat: parsed.repeat })).toBe(cron);
    }
  });
});
