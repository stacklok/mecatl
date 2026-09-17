import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  ACKNOWLEDGE_MAIN_MESSAGE,
  isRetentionDuration,
  mainRetentionEnabled,
  normalizeRetentionSettings,
  RETENTION_FAMILIES,
  retentionArgs,
  retentionDurationSeconds,
} from "./retention-settings.mjs";

/**
 * The controller's retention grammar: what a saved document or POST
 * /retention body normalises to, the duration grammar (exactly Go
 * time.ParseDuration — no `d`), the destructive-main acknowledgement gate
 * mecated enforces at startup (mirrored so a save can never spawn a daemon
 * that refuses to start), and exactly which mecated flags a document
 * becomes.
 */

const thrown = (
  fn: () => unknown,
): { message: string; statusCode?: number } => {
  try {
    fn();
  } catch (error) {
    return error as { message: string; statusCode?: number };
  }
  throw new Error("expected a throw");
};

const unset = { maxAge: null, maxCount: null };

describe("isRetentionDuration / retentionDurationSeconds", () => {
  it("accepts Go ParseDuration grammar and the lone 0", () => {
    for (const [raw, seconds] of [
      ["0", 0],
      ["168h", 604_800],
      ["1.5h", 5400],
      ["1h30m", 5400],
      [" 30m ", 1800],
      ["45s", 45],
      ["500ms", 0.5],
      ["0h", 0],
      [".5h", 1800],
      ["1µs", 1e-6],
    ] as const) {
      expect(isRetentionDuration(raw), raw).toBe(true);
      expect(retentionDurationSeconds(raw), raw).toBeCloseTo(seconds, 9);
    }
  });

  it("rejects days, bare numbers, negatives, blanks and non-strings", () => {
    for (const raw of [
      "7d",
      "1w",
      "168",
      "-1h",
      "",
      "  ",
      "h",
      "1h ago",
      "1 h",
    ]) {
      expect(isRetentionDuration(raw), raw).toBe(false);
      expect(retentionDurationSeconds(raw), raw).toBeNaN();
    }
    expect(isRetentionDuration(168)).toBe(false);
    expect(isRetentionDuration(null)).toBe(false);
  });
});

describe("normalizeRetentionSettings", () => {
  it("treats every absent, null or blank field as 'leave mecated's default'", () => {
    expect(RETENTION_FAMILIES).toEqual(["main", "child", "scheduled"]);
    const empty = {
      main: unset,
      child: unset,
      scheduled: unset,
      sweepCadence: null,
      acknowledgeMainDeletion: false,
    };
    for (const input of [undefined, null, {}, [], "x", 42]) {
      expect(normalizeRetentionSettings(input)).toEqual(empty);
    }
    expect(
      normalizeRetentionSettings({
        main: { maxAge: "", maxCount: "" },
        child: { maxAge: "  ", maxCount: null },
        scheduled: null,
        sweepCadence: "",
      }),
    ).toEqual(empty);
  });

  it("trims durations and accepts counts as numbers or digit strings", () => {
    expect(
      normalizeRetentionSettings({
        child: { maxAge: " 72h ", maxCount: "250" },
        scheduled: { maxAge: "0", maxCount: 1000 },
        sweepCadence: "30m",
      }),
    ).toEqual({
      main: unset,
      child: { maxAge: "72h", maxCount: 250 },
      scheduled: { maxAge: "0", maxCount: 1000 },
      sweepCadence: "30m",
      acknowledgeMainDeletion: false,
    });
  });

  it("rejects a bad duration with a 400 that names the field and rules out days", () => {
    const error = thrown(() =>
      normalizeRetentionSettings({ child: { maxAge: "7d" } }),
    );
    expect(error.statusCode).toBe(400);
    expect(error.message).toMatch(/^Child max age/);
    expect(error.message).toMatch(/days are not a unit/);
    expect(
      thrown(() => normalizeRetentionSettings({ sweepCadence: "hourly" }))
        .message,
    ).toMatch(/^Sweep cadence/);
    expect(
      thrown(() => normalizeRetentionSettings({ main: { maxAge: 168 } }))
        .message,
    ).toMatch(/^Main max age must be a duration/);
  });

  it("rejects negative, fractional or non-numeric counts", () => {
    for (const maxCount of [-1, 1.5, "12x", Number.NaN, "-3", {}]) {
      const error = thrown(() =>
        normalizeRetentionSettings({ scheduled: { maxCount } }),
      );
      expect(error.statusCode).toBe(400);
      expect(error.message).toMatch(
        /^Scheduled max count must be a whole number/,
      );
    }
  });

  it("requires the acknowledgement when main retention is switched on", () => {
    for (const main of [
      { maxAge: "720h" },
      { maxCount: 50 },
      { maxAge: "0", maxCount: 1 },
    ]) {
      const error = thrown(() => normalizeRetentionSettings({ main }));
      expect(error.statusCode).toBe(400);
      expect(error.message).toBe(ACKNOWLEDGE_MAIN_MESSAGE);
    }
    expect(
      normalizeRetentionSettings({
        main: { maxAge: "720h", maxCount: 50 },
        acknowledgeMainDeletion: true,
      }),
    ).toMatchObject({
      main: { maxAge: "720h", maxCount: 50 },
      acknowledgeMainDeletion: true,
    });
  });

  it("does not require the acknowledgement for off main limits or for other families", () => {
    expect(
      normalizeRetentionSettings({ main: { maxAge: "0", maxCount: 0 } }).main,
    ).toEqual({ maxAge: "0", maxCount: 0 });
    expect(
      normalizeRetentionSettings({
        child: { maxAge: "24h", maxCount: 10 },
        scheduled: { maxAge: "48h", maxCount: 20 },
      }).acknowledgeMainDeletion,
    ).toBe(false);
    // Only a literal true counts — no "yes", no 1.
    expect(
      normalizeRetentionSettings({ acknowledgeMainDeletion: "yes" })
        .acknowledgeMainDeletion,
    ).toBe(false);
  });
});

describe("mainRetentionEnabled", () => {
  it("is on for a non-zero main age or a positive main count only", () => {
    const base = normalizeRetentionSettings({});
    expect(mainRetentionEnabled(base)).toBe(false);
    expect(
      mainRetentionEnabled({ ...base, main: { maxAge: "0h", maxCount: 0 } }),
    ).toBe(false);
    expect(
      mainRetentionEnabled({ ...base, main: { maxAge: "1h", maxCount: null } }),
    ).toBe(true);
    expect(
      mainRetentionEnabled({ ...base, main: { maxAge: null, maxCount: 1 } }),
    ).toBe(true);
  });
});

describe("retentionArgs", () => {
  it("emits nothing for an empty document, so mecated's own defaults stand", () => {
    expect(retentionArgs(normalizeRetentionSettings({}))).toEqual([]);
  });

  it("emits one flag per set field, the cadence as --child-gc-interval, and the ack flag", () => {
    expect(
      retentionArgs(
        normalizeRetentionSettings({
          main: { maxAge: "720h", maxCount: 100 },
          child: { maxAge: "72h" },
          scheduled: { maxCount: 2000 },
          sweepCadence: "2h",
          acknowledgeMainDeletion: true,
        }),
      ),
    ).toEqual([
      "--main-retention",
      "720h",
      "--main-retention-max-total",
      "100",
      "--child-retention",
      "72h",
      "--schedule-fire-retention-max-total",
      "2000",
      "--child-gc-interval",
      "2h",
      "--acknowledge-main-retention",
    ]);
  });

  it("passes an explicit 0 through — that is how a default-on family is switched off", () => {
    expect(
      retentionArgs(
        normalizeRetentionSettings({
          child: { maxAge: "0", maxCount: 0 },
          scheduled: { maxAge: "0" },
        }),
      ),
    ).toEqual([
      "--child-retention",
      "0",
      "--child-retention-max-per-family",
      "0",
      "--schedule-fire-retention",
      "0",
    ]);
  });

  it("omits the ack flag unless acknowledged", () => {
    expect(
      retentionArgs(normalizeRetentionSettings({ child: { maxAge: "1h" } })),
    ).not.toContain("--acknowledge-main-retention");
  });
});

describe("local-controller spawn", () => {
  const source = readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../scripts/local-controller.mjs",
    ),
    "utf8",
  );

  it("spawns mecated through retentionArgs and never hard-codes a retention flag", () => {
    expect(source).toMatch(/retentionArgs\(/);
    for (const flag of [
      "--main-retention",
      "--child-retention",
      "--schedule-fire-retention",
      "--child-gc-interval",
      "--acknowledge-main-retention",
    ]) {
      expect(source, flag).not.toMatch(new RegExp(`["'\`]${flag}["'\`]`));
    }
  });

  it("only passes the flags when no imported operator settings file is active", () => {
    expect(source).toMatch(
      /if \(!operatorSettingsActive\)\s*\n?\s*args\.push\(\.\.\.retentionArgs\(/,
    );
  });
});
