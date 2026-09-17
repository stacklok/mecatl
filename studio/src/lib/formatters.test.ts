import { describe, expect, it } from "vitest";
import {
  describeCron,
  formatBytes,
  formatContextWindow,
  formatDurationMs,
} from "./formatters";

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

describe("describeCron", () => {
  it("reads the shapes the schedule builder emits", () => {
    expect(describeCron("0 9 * * *")).toBe("Daily at 9:00 AM");
    expect(describeCron("30 17 * * 1-5")).toBe("Weekdays at 5:30 PM");
    expect(describeCron("15 8 * * 5")).toBe("Weekly on Friday at 8:15 AM");
    expect(describeCron("0 0 * * 0")).toBe("Weekly on Sunday at 12:00 AM");
    expect(describeCron("0 12 1 * *")).toBe("Monthly on the 1st at 12:00 PM");
    expect(describeCron("5 7 22 * *")).toBe("Monthly on the 22nd at 7:05 AM");
    expect(describeCron("0 9 13 * *")).toBe("Monthly on the 13th at 9:00 AM");
  });

  it("keeps the interval shapes", () => {
    expect(describeCron("*/5 * * * *")).toBe("Every 5 minutes");
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
