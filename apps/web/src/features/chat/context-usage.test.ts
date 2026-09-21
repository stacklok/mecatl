// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { contextUtilization } from "./context-usage";

describe("contextUtilization", () => {
  it("compares input and output tokens with the resolved model window", () => {
    expect(
      contextUtilization({ inputTokens: "30000", outputTokens: "10000" }, "400000"),
    ).toBeCloseTo(0.1);
  });

  it("clamps overshoot and stays quiet without a usable window", () => {
    expect(contextUtilization({ inputTokens: "500", outputTokens: "600" }, "1000")).toBe(1);
    expect(contextUtilization({ inputTokens: "500", outputTokens: "600" }, "0")).toBeNull();
  });
});
