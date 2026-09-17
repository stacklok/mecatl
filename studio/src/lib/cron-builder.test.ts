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

  it("derives the interval shapes, ignoring the time control", () => {
    expect(
      builderToCron({
        repeat: "interval",
        time: "17:30",
        every: 15,
        unit: "minutes",
      }),
    ).toBe("*/15 * * * *");
    expect(
      builderToCron({ repeat: "interval", time: "", every: 2, unit: "hours" }),
    ).toBe("0 */2 * * *");
    // One hour is the canonical `0 * * * *`, not a `*/1` step.
    expect(
      builderToCron({ repeat: "interval", time: "", every: 1, unit: "hours" }),
    ).toBe("0 * * * *");
    // A one-minute step keeps the step form so it reads back as an interval.
    expect(
      builderToCron({
        repeat: "interval",
        time: "",
        every: 1,
        unit: "minutes",
      }),
    ).toBe("*/1 * * * *");
  });

  it("defaults a missing interval to every 30 minutes", () => {
    expect(builderToCron({ repeat: "interval", time: "09:00" })).toBe(
      "*/30 * * * *",
    );
    expect(
      builderToCron({ repeat: "interval", time: "09:00", unit: "hours" }),
    ).toBe("0 * * * *");
  });

  it("clamps the interval step to the unit's range", () => {
    expect(
      builderToCron({
        repeat: "interval",
        time: "",
        every: 0,
        unit: "minutes",
      }),
    ).toBe("*/1 * * * *");
    expect(
      builderToCron({
        repeat: "interval",
        time: "",
        every: 90,
        unit: "minutes",
      }),
    ).toBe("*/59 * * * *");
    expect(
      builderToCron({ repeat: "interval", time: "", every: 30, unit: "hours" }),
    ).toBe("0 */23 * * *");
    expect(
      builderToCron({ repeat: "interval", time: "", every: 0, unit: "hours" }),
    ).toBe("0 * * * *");
    expect(
      builderToCron({
        repeat: "interval",
        time: "",
        every: Number.NaN,
        unit: "minutes",
      }),
    ).toBe("*/1 * * * *");
  });
});

describe("cronToBuilder", () => {
  it("recognises the fixed-time builder shapes", () => {
    expect(cronToBuilder("0 9 * * *")).toEqual({
      repeat: "daily",
      time: "09:00",
      weekday: 1,
      monthday: 1,
      every: 30,
      unit: "minutes",
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

  it("recognises the interval shapes", () => {
    expect(cronToBuilder("*/15 * * * *")).toMatchObject({
      repeat: "interval",
      every: 15,
      unit: "minutes",
    });
    expect(cronToBuilder("*/1 * * * *")).toMatchObject({
      repeat: "interval",
      every: 1,
      unit: "minutes",
    });
    expect(cronToBuilder("0 */2 * * *")).toMatchObject({
      repeat: "interval",
      every: 2,
      unit: "hours",
    });
    expect(cronToBuilder("0 * * * *")).toMatchObject({
      repeat: "interval",
      every: 1,
      unit: "hours",
    });
    expect(cronToBuilder("0 */1 * * *")).toMatchObject({
      repeat: "interval",
      every: 1,
      unit: "hours",
    });
  });

  const exotic = [
    "*/15 * * * 1-5", // step minute pinned to weekdays
    "*/5 9 * * *", // step minute within a fixed hour
    "*/60 * * * *", // step beyond the minute range
    "*/0 * * * *", // zero step
    "0 */24 * * *", // step beyond the hour range
    "30 * * * *", // fixed minute every hour is not a builder shape
    "* * * * *", // every minute spelled without a step
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
    expect(cronToBuilder("0 9 * * 1,3,5")).toEqual({
      repeat: "custom",
      time: "09:00",
      weekday: 1,
      monthday: 1,
      every: 30,
      unit: "minutes",
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
    ...[1, 5, 15, 30, 59].map((every) => ({
      repeat: "interval" as const,
      time: "09:00",
      every,
      unit: "minutes" as const,
    })),
    ...[1, 2, 6, 12, 23].map((every) => ({
      repeat: "interval" as const,
      time: "09:00",
      every,
      unit: "hours" as const,
    })),
  ];

  it.each(shapes)("builder → cron → builder is lossless for %j", (shape) => {
    const cron = builderToCron(shape);
    const back = cronToBuilder(cron);
    expect(back.repeat).toBe(shape.repeat);
    expect(back.time).toBe(shape.time);
    if (shape.repeat === "weekly") expect(back.weekday).toBe(shape.weekday);
    if (shape.repeat === "monthly") expect(back.monthday).toBe(shape.monthday);
    if (shape.repeat === "interval") {
      expect(back.every).toBe(shape.every);
      expect(back.unit).toBe(shape.unit);
    }
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
      "*/15 * * * *",
      "0 */2 * * *",
      "0 * * * *",
    ]) {
      const parsed = cronToBuilder(cron);
      if (parsed.repeat === "custom")
        throw new Error(`unexpected custom for ${cron}`);
      expect(builderToCron({ ...parsed, repeat: parsed.repeat })).toBe(cron);
    }
  });
});
