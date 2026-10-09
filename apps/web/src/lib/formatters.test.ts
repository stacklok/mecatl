// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  describeCron,
  formatBytes,
  formatContextWindow,
  formatDate,
  formatDurationMs,
  formatMessageTime,
  formatRelativeTime,
  formatTokens,
  formatUntilTime,
  ordinal,
} from "./formatters";

// formatRelativeTime/formatUntilTime both read Date.now() internally, one
// tick after the test captures its own `now`. That's fine for most cases,
// but an exact multiple of a day (2880 minutes, 48 hours) sits precisely on
// every cascaded floor() boundary these functions use, so even a 1ms drift
// between the two Date.now() reads flips the result down by a whole unit.
// Freeze the clock so the internal read matches the test's `now` exactly.
beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("formatRelativeTime", () => {
  it("formats minutes, hours, and days since a timestamp", () => {
    const now = Date.now();
    expect(formatRelativeTime(now - 30_000)).toBe("<1m");
    expect(formatRelativeTime(now - 5 * 60_000)).toBe("5m");
    expect(formatRelativeTime(now - 3 * 3_600_000)).toBe("3h");
    expect(formatRelativeTime(now - 2 * 86_400_000)).toBe("2d");
  });

  it("reads a zero/unset timestamp as empty", () => {
    expect(formatRelativeTime(0)).toBe("");
  });
});

describe("formatUntilTime", () => {
  it("formats a future timestamp as a bare duration", () => {
    const now = Date.now();
    expect(formatUntilTime(now + 5 * 60_000)).toBe("5m");
    expect(formatUntilTime(now + 3 * 3_600_000)).toBe("3h");
    expect(formatUntilTime(now + 2 * 86_400_000)).toBe("2d");
  });

  it("reads a due-or-past instant as <1m rather than negative", () => {
    expect(formatUntilTime(Date.now() - 60_000)).toBe("<1m");
  });
});

describe("formatTokens", () => {
  it("keeps small counts exact and abbreviates thousands/millions", () => {
    expect(formatTokens(42)).toBe("42");
    expect(formatTokens(1_234)).toBe("1.2k");
    expect(formatTokens(2_500_000)).toBe("2.5M");
  });
});

describe("formatContextWindow", () => {
  it("renders whole thousands as k and millions with a trimmed decimal", () => {
    expect(formatContextWindow(200_000)).toBe("200k");
    expect(formatContextWindow(128_000)).toBe("128k");
    expect(formatContextWindow(1_048_576)).toBe("1M");
    expect(formatContextWindow(1_500_000)).toBe("1.5M");
    expect(formatContextWindow(512)).toBe("512");
  });

  it("reads an unknown (zero) or invalid window as empty so callers pick their own placeholder", () => {
    expect(formatContextWindow(0)).toBe("");
    expect(formatContextWindow(-1)).toBe("");
    expect(formatContextWindow(Number.NaN)).toBe("");
  });
});

describe("formatBytes", () => {
  it("renders binary units with one trimmed decimal", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(20 * 1024 * 1024)).toBe("20 MB");
    expect(formatBytes(3.25 * 1024 ** 3)).toBe("3.3 GB");
  });

  it("clamps negatives and non-finite values to 0 B", () => {
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(Number.NaN)).toBe("0 B");
  });
});

describe("formatDurationMs", () => {
  it("renders sub-second spans in ms and longer spans in trimmed seconds", () => {
    expect(formatDurationMs(840)).toBe("840ms");
    expect(formatDurationMs(4100)).toBe("4.1s");
    expect(formatDurationMs(2000)).toBe("2s");
    expect(formatDurationMs(61_500)).toBe("61.5s");
  });

  it("clamps negatives and non-finite values to 0ms", () => {
    expect(formatDurationMs(-5)).toBe("0ms");
    expect(formatDurationMs(Number.NaN)).toBe("0ms");
  });
});

describe("ordinal", () => {
  it("suffixes day-of-month numbers", () => {
    expect(ordinal(1)).toBe("1st");
    expect(ordinal(2)).toBe("2nd");
    expect(ordinal(3)).toBe("3rd");
    expect(ordinal(4)).toBe("4th");
    expect(ordinal(11)).toBe("11th");
    expect(ordinal(12)).toBe("12th");
    expect(ordinal(13)).toBe("13th");
    expect(ordinal(21)).toBe("21st");
    expect(ordinal(28)).toBe("28th");
  });
});

describe("describeCron", () => {
  it("reads the shapes the schedule builder emits", () => {
    expect(describeCron("0 9 * * *")).toBe("Daily at 9:00 AM");
    expect(describeCron("30 17 * * 1-5")).toBe("Weekdays at 5:30 PM");
    expect(describeCron("15 8 * * 5")).toBe("Weekly on Friday at 8:15 AM");
    expect(describeCron("0 0 * * 0")).toBe("Weekly on Sunday at 12:00 AM");
    expect(describeCron("0 12 1 * *")).toBe("Monthly on the 1st at 12:00 PM");
    expect(describeCron("5 7 22 * *")).toBe("Monthly on the 22nd at 7:05 AM");
    expect(describeCron("0 9 13 * *")).toBe("Monthly on the 13th at 9:00 AM");
    expect(describeCron("59 23 28 * *")).toBe("Monthly on the 28th at 11:59 PM");
  });

  it("keeps the interval shapes", () => {
    expect(describeCron("*/5 * * * *")).toBe("Every 5 minutes");
    expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes");
    expect(describeCron("0 */2 * * *")).toBe("Every 2 hours");
  });

  it("reads a step of one as the plain unit", () => {
    expect(describeCron("*/1 * * * *")).toBe("Every minute");
    expect(describeCron("0 * * * *")).toBe("Every hour");
    expect(describeCron("0 */1 * * *")).toBe("Every hour");
    // Only a top-of-the-hour minute reads as hourly; "*" minute is not.
    expect(describeCron("* * * * *")).toBe("* * * * *");
    expect(describeCron("30 * * * *")).toBe("30 * * * *");
  });

  it("falls back to the raw expression for anything it does not recognise", () => {
    for (const raw of [
      "0 9 * * 1,3,5", // day list
      "0 9 * * 7", // out-of-range weekday
      "0 9 32 * *", // out-of-range day of month
      "0 9 1 1 *", // pinned month
      "0 9 * *", // four fields
      "@daily",
    ]) {
      expect(describeCron(raw)).toBe(raw);
    }
  });
});

describe("formatMessageTime", () => {
  it("reads a zero/unset timestamp as empty", () => {
    expect(formatMessageTime(0)).toBe("");
  });

  it("renders a 24-hour clock", () => {
    const ts = new Date("2026-01-01T13:05:00Z").getTime();
    expect(formatMessageTime(ts)).toMatch(/^\d{2}:\d{2}$/);
  });
});

describe("formatDate", () => {
  it("renders a medium date with a short time", () => {
    const value = formatDate("2026-01-01T13:05:00.000Z");
    expect(value).toMatch(/2026/);
    expect(value).toMatch(/\d{1,2}:\d{2}/);
  });
});
