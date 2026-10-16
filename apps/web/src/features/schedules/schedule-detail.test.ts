// SPDX-License-Identifier: Apache-2.0

import type { ListScheduleFiresResponse } from "@mecatl-studio/contracts/generated";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  canRunNow,
  canTogglePause,
  describeLastRun,
  describeNextRun,
  describeRuns,
  fireOutcomeLabel,
  fireOutcomeVariant,
  scheduleDetailPollInterval,
  scheduleDetailPolling,
  scheduleFirePollInterval,
  sortScheduleFires,
  timezoneLabel,
} from "./schedule-detail";
import { canEditSchedule } from "./schedule-form";

type Fire = ListScheduleFiresResponse["items"][number];

function fire(overrides: Partial<Fire> & Pick<Fire, "id">): Fire {
  return {
    deadline: "",
    error: "",
    firedAt: "",
    inFlight: false,
    outcome: "completed",
    progressAt: "",
    scheduleName: "daily",
    sessionId: "",
    startedAt: "",
    stop: "completed",
    ...overrides,
  };
}

describe("schedule fire history", () => {
  it("sorts by outcome label with the newest fire as the tie break", () => {
    const items = [
      fire({ firedAt: "2026-01-01T00:00:00.000Z", id: "old-done", outcome: "completed" }),
      fire({ firedAt: "2026-01-02T00:00:00.000Z", id: "failed", outcome: "failed" }),
      fire({ firedAt: "2026-01-03T00:00:00.000Z", id: "new-done", outcome: "completed" }),
    ];

    expect(sortScheduleFires(items, "outcome", "asc").map((item) => item.id)).toEqual([
      "new-done",
      "old-done",
      "failed",
    ]);
  });

  it("labels and colors every outcome the BFF reports", () => {
    const cases: Array<[Fire["outcome"], string, string]> = [
      ["claimed", "Claimed", "success"],
      ["in_flight", "In flight", "success"],
      ["completed", "Completed", "muted"],
      ["failed", "Failed", "destructive"],
      ["cancelled", "Cancelled", "muted"],
      ["timed_out", "Timed out", "warning"],
      ["limit_reached", "Limit reached", "warning"],
      ["stopped", "Stopped", "muted"],
    ];

    for (const [outcome, label, variant] of cases) {
      const item = fire({ id: outcome, outcome });
      expect(fireOutcomeLabel(item), outcome).toBe(label);
      expect(fireOutcomeVariant(item), outcome).toBe(variant);
    }
  });

  it("reads an outcome this build does not know, or a missing one, as stopped", () => {
    const future = fire({ id: "future", outcome: "something_new" as Fire["outcome"] });
    expect(fireOutcomeLabel(future)).toBe("Stopped");
    expect(fireOutcomeVariant(future)).toBe("muted");
    const missing = fire({ id: "missing", outcome: undefined as unknown as Fire["outcome"] });
    expect(fireOutcomeLabel(missing)).toBe("Stopped");
  });

  it("polls only while the selected schedule or a fire is live", () => {
    expect(scheduleDetailPolling.refetchIntervalInBackground).toBe(false);
    const stable = [{ name: "daily", status: "scheduled" as const }];
    expect(scheduleDetailPollInterval(stable, "daily")).toBe(false);
    expect(scheduleDetailPollInterval([{ name: "daily", status: "running" }], "daily")).toBe(5_000);
    expect(scheduleDetailPollInterval([{ name: "daily", status: "claimed" }], "daily")).toBe(5_000);
    expect(scheduleDetailPollInterval(stable, "missing")).toBe(false);

    expect(scheduleFirePollInterval("scheduled", [fire({ id: "done" })])).toBe(false);
    expect(scheduleFirePollInterval("scheduled", [fire({ id: "live", inFlight: true })])).toBe(
      5_000,
    );
    expect(scheduleFirePollInterval("running", [])).toBe(5_000);
    expect(scheduleFirePollInterval("claimed", undefined)).toBe(5_000);
  });
});

describe("schedule actions and labels", () => {
  it("offers Run now only for a schedule with work left to run", () => {
    for (const status of ["scheduled", "claimed"] as const) {
      expect(canRunNow({ status }), status).toBe(true);
    }
    for (const status of ["running", "paused", "completed"] as const) {
      expect(canRunNow({ status }), status).toBe(false);
    }
  });

  it("offers neither Pause nor Resume for a completed schedule", () => {
    expect(canTogglePause({ status: "completed" })).toBe(false);
    for (const status of ["scheduled", "claimed", "running", "paused"] as const) {
      expect(canTogglePause({ status }), status).toBe(true);
    }
  });

  it("keeps completed one-shots inspect-only while completed crons retain metadata editing", () => {
    expect(
      canEditSchedule({
        status: "completed",
        trigger: { at: "2026-01-01T00:00:00Z", kind: "once" },
      }),
    ).toBe(false);
    expect(
      canEditSchedule({
        status: "completed",
        trigger: { expression: "0 9 * * *", kind: "cron", timezone: "" },
      }),
    ).toBe(true);
  });

  it("blocks editing any one-shot whose time has passed, whatever its status", () => {
    const now = Date.parse("2026-10-08T12:00:00Z");
    for (const status of ["scheduled", "paused", "claimed", "running"] as const) {
      expect(
        canEditSchedule({ status, trigger: { at: "2026-10-08T11:00:00Z", kind: "once" } }, now),
        `${status} past`,
      ).toBe(false);
      expect(
        canEditSchedule({ status, trigger: { at: "2026-10-08T13:00:00Z", kind: "once" } }, now),
        `${status} future`,
      ).toBe(true);
    }
  });

  it("labels an empty cron time zone as UTC", () => {
    expect(timezoneLabel("")).toBe("UTC");
    expect(timezoneLabel("Europe/Rome")).toBe("Europe/Rome");
  });
});

describe("schedule frequency facts", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime("2026-10-08T12:00:00Z");
  });
  afterEach(() => vi.useRealTimers());

  it("shows the next run as time remaining, and nothing for a schedule that will not fire", () => {
    expect(describeNextRun({ enabled: true, nextFireAt: "2026-10-08T12:30:00Z" })).toBe("in 30m");
    expect(describeNextRun({ enabled: true, nextFireAt: "2026-10-08T17:00:00Z" })).toBe("in 5h");
    expect(describeNextRun({ enabled: true, nextFireAt: "2026-10-08T11:00:00Z" })).toBe("in <1m");
    expect(describeNextRun({ enabled: false, nextFireAt: "2026-10-09T12:00:00Z" })).toBe("—");
    expect(describeNextRun({ enabled: true, nextFireAt: "" })).toBe("—");
  });

  it("shows the last run as time elapsed", () => {
    expect(describeLastRun({ lastFireAt: "2026-10-08T09:00:00Z" })).toBe("3h ago");
    expect(describeLastRun({ lastFireAt: "2026-10-05T12:00:00Z" })).toBe("3d ago");
    expect(describeLastRun({ lastFireAt: "2026-10-08T11:59:30Z" })).toBe("<1m ago");
    expect(describeLastRun({ lastFireAt: "" })).toBe("Never");
  });

  it("folds the run limit into the run count", () => {
    expect(describeRuns({ fireCount: 0, maxFires: 0 })).toBe("None yet");
    expect(describeRuns({ fireCount: 0, maxFires: 5 })).toBe("None yet · up to 5");
    expect(describeRuns({ fireCount: 3, maxFires: 0 })).toBe("3");
    expect(describeRuns({ fireCount: 3, maxFires: 10 })).toBe("3 of 10");
    expect(describeRuns({ fireCount: 10, maxFires: 10 })).toBe("10 of 10 — limit reached");
  });
});
