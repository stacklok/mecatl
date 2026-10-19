// SPDX-License-Identifier: Apache-2.0

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { describe, expect, it } from "vitest";
import {
  deriveMobileScheduleInventory,
  deriveScheduleInventory,
  filterScheduleInventory,
} from "./schedule-inventory";

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

    expect(
      deriveScheduleInventory(filterScheduleInventory(source, "STALE"), "name").map(
        (item) => item.name,
      ),
    ).toEqual(["Cleanup"]);
    expect(deriveScheduleInventory(filterScheduleInventory(source, "Ada"), "name")).toEqual([]);
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
      deriveScheduleInventory(filterScheduleInventory(source, "Europe/Rome"), "status").map(
        (item) => item.name,
      ),
    ).toEqual(["Rome report"]);
    expect(
      deriveScheduleInventory(filterScheduleInventory(source, "weekdays"), "status").map(
        (item) => item.name,
      ),
    ).toEqual(["Rome report"]);
    expect(deriveScheduleInventory(source, "status").map((item) => item.name)).toEqual([
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

    expect(deriveScheduleInventory(source, "name", "desc").map((item) => item.name)).toEqual([
      "Zulu",
      "Beta",
      "Alpha",
    ]);
    expect(deriveMobileScheduleInventory(source).map((item) => item.name)).toEqual([
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
        deriveScheduleInventory(source, key, "asc").map((item) => item.name),
        key,
      ).toEqual(ascending);
      expect(
        deriveScheduleInventory(source, key, "desc").map((item) => item.name),
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

    expect(deriveScheduleInventory(source, "nextRun").map((item) => item.name)).toEqual([
      "Sooner",
      "Later",
      "Invalid",
      "None",
      "Paused",
    ]);
  });
});

describe("schedule column ordering and cadence search", () => {
  const cron = (name: string, expression: string) =>
    schedule({ name, trigger: { expression, kind: "cron", timezone: "" } });
  const names = (items: Schedule[]) => items.map((item) => item.name);

  it("orders the Schedule column by cadence and time of day, not by the English label", () => {
    const source = [
      schedule({ name: "once-later", trigger: { at: "2026-12-01T09:00:00Z", kind: "once" } }),
      cron("monthly", "0 9 1 * *"),
      cron("weekly-fri", "0 9 * * 5"),
      cron("weekly-mon", "0 9 * * 1"),
      cron("weekdays", "30 17 * * 1-5"),
      cron("daily-1pm", "0 13 * * *"),
      cron("daily-9am", "0 9 * * *"),
      cron("daily-midnight", "0 0 * * *"),
      cron("daily-11pm", "0 23 * * *"),
      cron("every-hour", "0 * * * *"),
      cron("every-15m", "*/15 * * * *"),
      schedule({ name: "once-sooner", trigger: { at: "2026-11-01T09:00:00Z", kind: "once" } }),
      cron("custom", "0 9 1 1 *"),
    ];

    expect(names(deriveScheduleInventory(source, "schedule", "asc"))).toEqual([
      "every-15m",
      "every-hour",
      "daily-midnight",
      "daily-9am",
      "daily-1pm",
      "daily-11pm",
      "weekdays",
      "weekly-mon",
      "weekly-fri",
      "monthly",
      "custom",
      "once-sooner",
      "once-later",
    ]);
  });

  it("finds a schedule by its cadence word as well as its description", () => {
    const source = [
      cron("a-daily", "0 9 * * *"),
      cron("b-weekdays", "30 17 * * 1-5"),
      cron("c-weekly", "15 8 * * 5"),
      cron("d-monthly", "0 12 1 * *"),
      cron("e-interval", "*/15 * * * *"),
    ];

    for (const [word, expected] of [
      ["daily", "a-daily"],
      ["weekdays", "b-weekdays"],
      ["weekly", "c-weekly"],
      ["monthly", "d-monthly"],
      ["interval", "e-interval"],
    ] as const) {
      expect(
        names(deriveScheduleInventory(filterScheduleInventory(source, word), "name")),
        word,
      ).toContain(expected);
    }
    expect(
      names(deriveScheduleInventory(filterScheduleInventory(source, "weekly"), "name")),
    ).not.toContain("a-daily");
    // A list of times is still the same cadence.
    expect(
      names(
        deriveScheduleInventory(
          filterScheduleInventory([cron("lists", "15 9,13,17 * * 1-5")], "weekdays"),
          "name",
        ),
      ),
    ).toEqual(["lists"]);
    // The described wording still matches.
    expect(
      names(
        deriveScheduleInventory(filterScheduleInventory(source, "monday through friday"), "name"),
      ),
    ).toEqual(["b-weekdays"]);
  });
});
