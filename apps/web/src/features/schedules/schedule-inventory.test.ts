// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { describe, expect, it } from "vitest";
import { deriveMobileScheduleInventory, deriveScheduleInventory } from "./schedule-inventory";

type Schedule = ListSchedulesResponse["items"][number];

function schedule(overrides: Partial<Schedule> & Pick<Schedule, "name">): Schedule {
  return {
    enabled: true,
    fireCount: 0,
    lastFireAt: "",
    lastFireSessionId: "",
    maxFires: 0,
    mode: "default",
    modelId: "",
    mutating: false,
    nextFireAt: "",
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    owner: "",
    profile: "all",
    prompt: "prompt",
    providerId: "",
    status: "scheduled",
    trigger: { expression: "0 9 * * *", kind: "cron", timezone: "" },
    ...overrides,
  };
}

describe("schedule inventory", () => {
  it("searches visible row text without changing the source inventory", () => {
    const source = [
      schedule({ name: "Daily report", owner: "Ada" }),
      schedule({ name: "Cleanup", prompt: "Remove stale previews", status: "paused" }),
    ];

    expect(deriveScheduleInventory(source, "STALE", "name").map((item) => item.name)).toEqual([
      "Cleanup",
    ]);
    expect(deriveScheduleInventory(source, "Ada", "name")).toEqual([]);
    expect(source.map((item) => item.name)).toEqual(["Daily report", "Cleanup"]);
  });

  it("matches visible trigger text and orders effective statuses by attention", () => {
    const source = [
      schedule({ name: "Done", status: "completed" }),
      schedule({ name: "Waiting", status: "claimed" }),
      schedule({
        name: "Rome report",
        status: "scheduled",
        trigger: { expression: "30 17 * * 1-5", kind: "cron", timezone: "Europe/Rome" },
      }),
      schedule({ name: "Live", status: "running" }),
      schedule({ name: "Off", status: "paused" }),
    ];

    expect(
      deriveScheduleInventory(source, "Europe/Rome", "status").map((item) => item.name),
    ).toEqual(["Rome report"]);
    expect(
      deriveScheduleInventory(source, "Weekdays at 5:30", "status").map((item) => item.name),
    ).toEqual(["Rome report"]);
    expect(deriveScheduleInventory(source, "", "status").map((item) => item.name)).toEqual([
      "Live",
      "Waiting",
      "Rome report",
      "Off",
      "Done",
    ]);
  });

  it("keeps mobile attention order independent of desktop sorting", () => {
    const source = [
      schedule({ name: "Alpha", status: "paused" }),
      schedule({ name: "Zulu", status: "running" }),
      schedule({ name: "Beta", status: "running" }),
    ];

    expect(deriveScheduleInventory(source, "", "name", "desc").map((item) => item.name)).toEqual([
      "Zulu",
      "Beta",
      "Alpha",
    ]);
    expect(deriveMobileScheduleInventory(source, "").map((item) => item.name)).toEqual([
      "Beta",
      "Zulu",
      "Alpha",
    ]);
  });

  it("sorts every desktop column in both directions", () => {
    const source = [
      schedule({
        fireCount: 2,
        lastFireAt: "2026-10-08T08:00:00Z",
        name: "Alpha",
        nextFireAt: "2026-10-08T10:00:00Z",
        status: "paused",
        trigger: { expression: "0 9 * * *", kind: "cron", timezone: "" },
      }),
      schedule({
        fireCount: 1,
        lastFireAt: "2026-10-07T08:00:00Z",
        name: "Beta",
        nextFireAt: "2026-10-08T09:00:00Z",
        status: "running",
        trigger: { expression: "30 17 * * 1-5", kind: "cron", timezone: "" },
      }),
    ];
    const cases = [
      ["name", ["Alpha", "Beta"], ["Beta", "Alpha"]],
      ["schedule", ["Alpha", "Beta"], ["Beta", "Alpha"]],
      ["nextRun", ["Beta", "Alpha"], ["Alpha", "Beta"]],
      ["lastRun", ["Beta", "Alpha"], ["Alpha", "Beta"]],
      ["fires", ["Beta", "Alpha"], ["Alpha", "Beta"]],
      ["status", ["Beta", "Alpha"], ["Alpha", "Beta"]],
    ] as const;

    for (const [key, ascending, descending] of cases) {
      expect(
        deriveScheduleInventory(source, "", key, "asc").map((item) => item.name),
        key,
      ).toEqual(ascending);
      expect(
        deriveScheduleInventory(source, "", key, "desc").map((item) => item.name),
        `${key} desc`,
      ).toEqual(descending);
    }
  });

  it("orders upcoming fires first and puts schedules without a valid next fire last", () => {
    const source = [
      schedule({ name: "None", nextFireAt: "" }),
      schedule({ name: "Later", nextFireAt: "2026-10-08T09:00:00Z" }),
      schedule({ name: "Sooner", nextFireAt: "2026-10-08T08:00:00Z" }),
      schedule({ enabled: false, name: "Paused", nextFireAt: "2026-10-08T07:00:00Z" }),
      schedule({ name: "Invalid", nextFireAt: "not-a-date" }),
    ];

    expect(deriveScheduleInventory(source, "", "nextRun").map((item) => item.name)).toEqual([
      "Sooner",
      "Later",
      "Invalid",
      "None",
      "Paused",
    ]);
  });
});
