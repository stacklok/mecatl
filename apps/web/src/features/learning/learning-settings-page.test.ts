// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { reflectionSummary } from "./learning-settings-page";

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
    ).toBe(
      "Done: 4 to review · 2 remembered · 1 clashed with existing memory · 3 still being checked.",
    );
  });

  it("leads with a disposition other than completed", () => {
    expect(
      reflectionSummary({
        abstained: false,
        conflicted: 0,
        disposition: "rate_limited",
        message: "",
        promoted: 0,
        queued: 0,
        reason: "",
        reflectionId: "reflection-3",
        staged: 0,
      }),
    ).toBe("Rate limited: 0 to review.");
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
