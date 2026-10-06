// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { isStaleMemoryPlan, memoryConsolidationSummary } from "./memory-consolidation";

describe("memory consolidation", () => {
  it("summarizes apply receipts", () => {
    expect(
      memoryConsolidationSummary({
        applied: 2,
        conflicted: 1,
        disposition: "apply",
        failed: 0,
        id: "plan-1",
        planned: 4,
        skipped: 1,
        target: "user_model",
      }),
    ).toBe("Applied: 2 merged, 1 conflicted, 1 skipped, 0 failed.");
  });

  it("recognizes stale daemon plan errors", () => {
    expect(isStaleMemoryPlan({ code: "dream_not_found", status: 404 })).toBe(true);
    expect(isStaleMemoryPlan({ code: "internal_error", status: 500 })).toBe(false);
  });
});
