// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  EFFORT_OPTIONS,
  effortChoicesFor,
  effortSentFor,
  NO_EFFORT_LEVELS_NOTE,
  NO_REASONING_SUPPORT_NOTE,
  requestedEffort,
  UNKNOWN_EFFORT_LEVELS_NOTE,
} from "./reasoning-effort";

const values = (choices: ReturnType<typeof effortChoicesFor>) =>
  choices.options.map((option) => option.value);

describe("effortChoicesFor", () => {
  it("offers every tier without a claim for a model the inventory doesn't list", () => {
    expect(effortChoicesFor(undefined, "xhigh")).toEqual({
      levelsUnknown: false,
      options: EFFORT_OPTIONS,
    });
  });

  it("keeps the old caveat on a server without per-model levels", () => {
    expect(effortChoicesFor({ reasoning: false }, "high")).toEqual({
      levelsUnknown: false,
      note: NO_REASONING_SUPPORT_NOTE,
      options: EFFORT_OPTIONS,
    });
    expect(effortChoicesFor({ reasoning: true }, "high").note).toBeUndefined();
  });

  it("offers every tier and says so when the model's levels are unknown", () => {
    // A gateway model reports no reasoning support yet receives the requested level.
    expect(
      effortChoicesFor({ effortLevels: { known: false, levels: [] }, reasoning: false }, "max"),
    ).toEqual({ levelsUnknown: true, note: UNKNOWN_EFFORT_LEVELS_NOTE, options: EFFORT_OPTIONS });
  });

  it("offers Auto alone for a model known to take no effort", () => {
    const choices = effortChoicesFor(
      { effortLevels: { known: true, levels: [] } },
      "high",
      "default",
    );
    expect(values(choices)).toEqual(["default"]);
    expect(choices.note).toBe(NO_EFFORT_LEVELS_NOTE);
  });

  it("offers Auto plus the model's levels, without a note when the request is among them", () => {
    const choices = effortChoicesFor(
      { effortLevels: { known: true, levels: ["low", "medium", "high"] } },
      "medium",
    );
    expect(values(choices)).toEqual(["default", "low", "medium", "high"]);
    expect(choices.note).toBeUndefined();
    expect(choices.levelsUnknown).toBe(false);
  });

  it("names the level a session's requests use from its applied echo", () => {
    const model = { effortLevels: { known: true, levels: ["low" as const, "high" as const] } };
    expect(effortChoicesFor(model, "xhigh", "high").note).toBe(
      "This model doesn't accept Extra high; requests use High.",
    );
    expect(effortChoicesFor(model, "xhigh", "default").note).toBe(
      "This model doesn't accept Extra high; requests use the provider default.",
    );
  });

  it("predicts the level a draft's requests will use", () => {
    const model = { effortLevels: { known: true, levels: ["medium" as const, "max" as const] } };
    expect(effortChoicesFor(model, "xhigh").note).toBe(
      "This model doesn't accept Extra high; requests use Medium.",
    );
    expect(effortChoicesFor(model, "low").note).toBe(
      "This model doesn't accept Low; requests use the provider default.",
    );
  });
});

describe("effortSentFor", () => {
  it("sends the requested level, else the highest listed below it, else none", () => {
    expect(effortSentFor("high", ["low", "high"])).toBe("high");
    expect(effortSentFor("max", ["low", "medium", "high"])).toBe("high");
    expect(effortSentFor("medium", ["low", "high"])).toBe("low");
    expect(effortSentFor("low", ["high"])).toBe("default");
    expect(effortSentFor("default", ["high"])).toBe("default");
  });
});

describe("requestedEffort", () => {
  it("prefers the reported request and falls back to the applied level", () => {
    expect(requestedEffort({ reasoningEffort: "high", requestedReasoningEffort: "xhigh" })).toBe(
      "xhigh",
    );
    expect(requestedEffort({ reasoningEffort: "high", requestedReasoningEffort: "default" })).toBe(
      "default",
    );
    expect(requestedEffort({ reasoningEffort: "high" })).toBe("high");
    expect(requestedEffort(undefined)).toBe("default");
  });
});
