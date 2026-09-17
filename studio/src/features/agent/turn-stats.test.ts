import { describe, expect, it } from "vitest";
import {
  accumulateTurnStats,
  cacheHitRate,
  contextBand,
  formatPercent,
  formatTurnStat,
  isTrivialTurn,
} from "./turn-stats";
import type { TurnStats } from "./types";

/**
 * Pins the per-turn cost helpers against the TUI footer's rules
 * (cmd/mecatui/ui/footer.go): the fold of `turn.end` frames onto a message,
 * the cacheRead/input hit rate, the trivial-turn suppression, the stat line's
 * facet rules, and the ok/warn/danger context bands.
 */

const turn = (
  partial: Partial<{
    durationMs: number;
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens: number;
    cacheWriteTokens: number;
  }> = {},
) => ({
  durationMs: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  ...partial,
});

const stats = (partial: Partial<TurnStats> = {}): TurnStats => ({
  turns: 1,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  durationMs: 0,
  lastInputTokens: 0,
  ...partial,
});

describe("accumulateTurnStats", () => {
  it("sums tokens and duration across turns and ASSIGNS lastInputTokens", () => {
    const first = accumulateTurnStats(
      undefined,
      turn({
        durationMs: 1200,
        inputTokens: 1000,
        outputTokens: 200,
        cacheReadTokens: 400,
        cacheWriteTokens: 50,
      }),
    );
    expect(first).toEqual({
      turns: 1,
      inputTokens: 1000,
      outputTokens: 200,
      cacheReadTokens: 400,
      cacheWriteTokens: 50,
      durationMs: 1200,
      lastInputTokens: 1000,
    });
    const second = accumulateTurnStats(
      first,
      turn({ durationMs: 800, inputTokens: 1500, outputTokens: 100 }),
    );
    expect(second).toEqual({
      turns: 2,
      inputTokens: 2500,
      outputTokens: 300,
      cacheReadTokens: 400,
      cacheWriteTokens: 50,
      durationMs: 2000,
      // The latest turn's input is the context occupancy — never a sum.
      lastInputTokens: 1500,
    });
  });

  it("treats negative or non-finite figures as zero", () => {
    expect(
      accumulateTurnStats(
        undefined,
        turn({ inputTokens: -5, outputTokens: Number.NaN, durationMs: -1 }),
      ),
    ).toEqual(stats({ turns: 1 }));
  });
});

describe("cacheHitRate", () => {
  it("is cacheRead over input (cache reads are a subset of input)", () => {
    expect(cacheHitRate({ inputTokens: 1000, cacheReadTokens: 350 })).toBe(
      0.35,
    );
    // A fully cached turn reads 100%, not 50%.
    expect(cacheHitRate({ inputTokens: 800, cacheReadTokens: 800 })).toBe(1);
  });

  it("guards a zero input and clamps an over-reporting adapter to 1", () => {
    expect(cacheHitRate({ inputTokens: 0, cacheReadTokens: 10 })).toBe(0);
    expect(cacheHitRate({ inputTokens: 10 })).toBe(0);
    expect(cacheHitRate({ inputTokens: 10, cacheReadTokens: 20 })).toBe(1);
  });
});

describe("formatPercent", () => {
  it("rounds to an integer percentage and clamps to [0, 100]", () => {
    expect(formatPercent(0.881)).toBe("88%");
    expect(formatPercent(0.345)).toBe("35%");
    expect(formatPercent(-0.2)).toBe("0%");
    expect(formatPercent(1.7)).toBe("100%");
  });
});

describe("isTrivialTurn", () => {
  it.each([
    [stats({ inputTokens: 50, outputTokens: 50, durationMs: 999 }), true],
    [stats({ inputTokens: 51, outputTokens: 0, durationMs: 0 }), false],
    [stats({ inputTokens: 0, outputTokens: 51, durationMs: 0 }), false],
    [stats({ inputTokens: 10, outputTokens: 10, durationMs: 1000 }), false],
    [stats(), true],
  ])("classifies %o as trivial=%s", (value, trivial) => {
    expect(isTrivialTurn(value)).toBe(trivial);
  });
});

describe("formatTurnStat", () => {
  it("leads with cost, then duration, then a material cache facet", () => {
    expect(
      formatTurnStat(
        stats({
          inputTokens: 1200,
          outputTokens: 340,
          durationMs: 4100,
          cacheReadTokens: 420,
        }),
      ),
    ).toBe("↑1.2k ↓340 · 4.1s · 35% cached");
  });

  it("omits the duration when the daemon reported no clock", () => {
    expect(formatTurnStat(stats({ inputTokens: 900, outputTokens: 20 }))).toBe(
      "↑900 ↓20",
    );
  });

  it("omits the cache facet under the 10% floor and keeps it at the floor", () => {
    expect(
      formatTurnStat(
        stats({ inputTokens: 1000, outputTokens: 10, cacheReadTokens: 99 }),
      ),
    ).toBe("↑1.0k ↓10");
    expect(
      formatTurnStat(
        stats({ inputTokens: 1000, outputTokens: 10, cacheReadTokens: 100 }),
      ),
    ).toBe("↑1.0k ↓10 · 10% cached");
  });

  it("renders sub-second durations in milliseconds", () => {
    expect(
      formatTurnStat(
        stats({ inputTokens: 100, outputTokens: 60, durationMs: 840 }),
      ),
    ).toBe("↑100 ↓60 · 840ms");
  });
});

describe("contextBand", () => {
  it.each([
    [0, "ok"],
    [0.599, "ok"],
    [0.6, "warn"],
    [0.849, "warn"],
    [0.85, "danger"],
    [1, "danger"],
  ] as const)("maps %d to %s", (fraction, band) => {
    expect(contextBand(fraction)).toBe(band);
  });
});
