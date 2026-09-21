// SPDX-License-Identifier: Apache-2.0

import type { SessionUsageResponse } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import {
  cacheHitRate,
  formatDurationMs,
  formatPercent,
  formatTokens,
  formatTurnStat,
  isTrivialTurn,
  usageMenuLines,
} from "./turn-stats";

function usage(overrides: Partial<SessionUsageResponse> = {}): SessionUsageResponse {
  return {
    cacheReadTokens: "0",
    cacheWriteTokens: "0",
    inputTokens: "0",
    outputTokens: "0",
    reasoningTokens: "0",
    ...overrides,
  };
}

describe("formatTokens", () => {
  it("keeps small counts exact and abbreviates large ones", () => {
    expect(formatTokens("42")).toBe("42");
    expect(formatTokens("1200")).toBe("1.2k");
    expect(formatTokens("3450000")).toBe("3.5M");
  });
});

describe("formatDurationMs", () => {
  it("shows sub-second durations in ms and larger ones in seconds", () => {
    expect(formatDurationMs(480)).toBe("480ms");
    expect(formatDurationMs(4100)).toBe("4.1s");
    expect(formatDurationMs(4000)).toBe("4s");
  });
});

describe("cacheHitRate / formatPercent", () => {
  it("computes the cache-read share of input tokens", () => {
    expect(cacheHitRate(usage({ cacheReadTokens: "350", inputTokens: "1000" }))).toBeCloseTo(0.35);
    expect(cacheHitRate(usage({ inputTokens: "0" }))).toBe(0);
  });

  it("formats a fraction as a clamped percentage", () => {
    expect(formatPercent(0.354)).toBe("35%");
    expect(formatPercent(1.5)).toBe("100%");
  });
});

describe("isTrivialTurn / formatTurnStat", () => {
  it("suppresses the stat line for a near-empty turn", () => {
    expect(isTrivialTurn(usage(), 0)).toBe(true);
    expect(formatTurnStat(usage(), 0)).toBeNull();
  });

  it("reports a real turn's tokens and duration", () => {
    const stat = formatTurnStat(usage({ inputTokens: "1200", outputTokens: "340" }), 4100);
    expect(stat).toBe("↑1.2k ↓340 · 4.1s");
  });

  it("appends the cache-hit facet only above the floor", () => {
    const cached = formatTurnStat(
      usage({ cacheReadTokens: "350", inputTokens: "1000", outputTokens: "200" }),
      4100,
    );
    expect(cached).toBe("↑1.0k ↓200 · 4.1s · 35% cached");

    const barelyCached = formatTurnStat(
      usage({ cacheReadTokens: "5", inputTokens: "1000", outputTokens: "200" }),
      4100,
    );
    expect(barelyCached).toBe("↑1.0k ↓200 · 4.1s");
  });
});

describe("usageMenuLines", () => {
  it("is empty for a session that has spent nothing yet", () => {
    expect(usageMenuLines(usage())).toEqual([]);
  });

  it("lists only the non-zero facets, in a fixed order", () => {
    expect(
      usageMenuLines(usage({ inputTokens: "48800", outputTokens: "155", reasoningTokens: "128" })),
    ).toEqual(["48.8k input", "155 output", "128 reasoning"]);
  });

  it("appends the cache-hit-rate line whenever any cache was read, with no floor", () => {
    expect(
      usageMenuLines(usage({ cacheReadTokens: "5", inputTokens: "1000", outputTokens: "200" })),
    ).toEqual(["1.0k input", "200 output", "5 cache read", "1% cache hit rate"]);
  });

  it("includes cache write alongside cache read", () => {
    expect(
      usageMenuLines(usage({ cacheWriteTokens: "300", inputTokens: "1000", outputTokens: "200" })),
    ).toEqual(["1.0k input", "200 output", "300 cache write"]);
  });
});
