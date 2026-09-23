// SPDX-License-Identifier: Apache-2.0

import type { ListScheduleFiresResponse } from "@mecatl-studio/contracts/generated";
import { describe, expect, it } from "vitest";
import {
  canRunNow,
  canTogglePause,
  fireDurationMs,
  sortScheduleFires,
  timezoneLabel,
} from "./schedule-detail";

type Fire = ListScheduleFiresResponse["items"][number];

function fire(overrides: Partial<Fire> & Pick<Fire, "id">): Fire {
  return {
    deadline: "",
    error: "",
    firedAt: "",
    inFlight: false,
    progressAt: "",
    scheduleName: "daily",
    sessionId: "",
    startedAt: "",
    stop: "completed",
    ...overrides,
  };
}

describe("schedule fire history", () => {
  it("sorts by duration with newest fire as the tie break", () => {
    const items = [
      fire({
        firedAt: "2026-01-02T00:00:00.000Z",
        id: "newer",
        progressAt: "2026-01-02T00:00:02.000Z",
        startedAt: "2026-01-02T00:00:00.000Z",
      }),
      fire({
        firedAt: "2026-01-01T00:00:00.000Z",
        id: "older",
        progressAt: "2026-01-01T00:00:02.000Z",
        startedAt: "2026-01-01T00:00:00.000Z",
      }),
      fire({
        firedAt: "2026-01-03T00:00:00.000Z",
        id: "longest",
        progressAt: "2026-01-03T00:00:03.000Z",
        startedAt: "2026-01-03T00:00:00.000Z",
      }),
    ];

    expect(sortScheduleFires(items, "duration", "asc").map((item) => item.id)).toEqual([
      "newer",
      "older",
      "longest",
    ]);
  });

  it("uses the current time for an in-flight duration", () => {
    expect(
      fireDurationMs(
        fire({ id: "live", inFlight: true, startedAt: "2026-01-01T00:00:00.000Z" }),
        Date.parse("2026-01-01T00:00:04.500Z"),
      ),
    ).toBe(4_500);
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

  it("labels an empty cron time zone as UTC", () => {
    expect(timezoneLabel("")).toBe("UTC");
    expect(timezoneLabel("Europe/Rome")).toBe("Europe/Rome");
  });
});
