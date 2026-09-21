// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { reflectionSummary } from "./learning-review";

describe("reflection summary", () => {
  it("summarizes materialized reflection counts", () => {
    expect(
      reflectionSummary({
        abstained: false,
        conflicted: 1,
        disposition: "completed",
        message: "",
        promoted: 2,
        queued: 3,
        reason: "",
        reflectionId: "reflection-1",
        staged: 4,
      }),
    ).toBe("Reflection completed: 4 staged · 2 promoted · 1 conflicted · 3 queued.");
  });

  it("prefers the daemon-authored abstention message", () => {
    expect(
      reflectionSummary({
        abstained: true,
        conflicted: 0,
        disposition: "abstained",
        message: "No durable learning signal was found.",
        promoted: 0,
        queued: 0,
        reason: "no_signal",
        reflectionId: "reflection-2",
        staged: 0,
      }),
    ).toBe("No durable learning signal was found.");
  });
});
