// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { describeCron } from "./describe-cron";

describe("describeCron", () => {
  it("describes the shapes the schedule builder emits in 12-hour time", () => {
    expect(describeCron("0 9 * * *")).toBe("At 09:00 AM");
    expect(describeCron("30 17 * * 1-5")).toBe("At 05:30 PM, Monday through Friday");
    expect(describeCron("15 8 * * 5")).toBe("At 08:15 AM, only on Friday");
    expect(describeCron("0 0 * * 0")).toBe("At 12:00 AM, only on Sunday");
    expect(describeCron("0 12 1 * *")).toBe("At 12:00 PM, on day 1 of the month");
  });

  it("describes the interval shapes", () => {
    expect(describeCron("*/5 * * * *")).toBe("Every 5 minutes");
    expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes");
    expect(describeCron("*/1 * * * *")).toBe("Every minute");
    expect(describeCron("0 * * * *")).toBe("Every hour");
    expect(describeCron("0 */1 * * *")).toBe("Every hour");
    expect(describeCron("0 */2 * * *")).toBe("On the hour, every 2 hours");
  });

  it("describes what the daemon accepts beyond the builder's shapes", () => {
    expect(describeCron("0 9 * * MON-FRI")).toBe("At 09:00 AM, Monday through Friday");
    expect(describeCron("0 9 * * 1,3,5")).toBe(
      "At 09:00 AM, only on Monday, Wednesday, and Friday",
    );
    // Tabs separate fields for the daemon too.
    expect(describeCron("0\t9\t*\t*\t*")).toBe("At 09:00 AM");
  });

  it("returns the raw expression for what the daemon rejects but cronstrue accepts", () => {
    for (const raw of [
      "0 9 * * 7", // weekday range is 0-6
      "0 9 L * *", // last day of month
      "0 9 15W * *", // nearest weekday
      "0 12 * * 1#1", // nth weekday
      "0 9 * * L", // last weekday
      "0 9 32 * *", // day of month beyond 31
      "60 9 * * *", // minute beyond 59
      "0 24 * * *", // hour beyond 23
      "0 9 1 13 *", // month beyond 12
      "0 9 * * 5-1", // range that runs backwards
      "0 9 * * 1,,2", // empty list item
      "0 9 * * */0", // zero step
    ]) {
      expect(describeCron(raw), raw).toBe(raw);
    }
  });

  it("returns the raw expression for steps wider than the field", () => {
    for (const raw of ["*/90 * * * *", "0 */25 * * *", "*/60 * * * *"]) {
      expect(describeCron(raw), raw).toBe(raw);
    }
  });

  it("returns the raw expression for anything that is not five fields", () => {
    for (const raw of ["0 9 * *", "0 0 9 * * *", "@daily", ""]) {
      expect(describeCron(raw), raw).toBe(raw);
    }
  });
});
