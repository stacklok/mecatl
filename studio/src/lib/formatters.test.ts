import { describe, expect, it } from "vitest";
import { describeCron } from "./formatters";

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
