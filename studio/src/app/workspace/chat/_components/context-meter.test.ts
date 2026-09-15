import { describe, expect, it } from "vitest";
import { contextUtilisation } from "./context-meter";

/**
 * Pins the context-meter math (B1.1): counted input+output tokens over the
 * resolved model's context window, clamped, and honestly null when the window
 * is unknown — the meter must never render against a made-up denominator.
 */
describe("contextUtilisation", () => {
  it("computes the fraction of the window the counted tokens occupy", () => {
    expect(contextUtilisation(30_000, 10_000, 400_000)).toBeCloseTo(0.1);
    expect(contextUtilisation(0, 0, 400_000)).toBe(0);
  });

  it("clamps overshoot to 1 (the daemon compacts before the client's approximation catches up)", () => {
    expect(contextUtilisation(500_000, 100_000, 400_000)).toBe(1);
  });

  it("returns null when the window is unknown", () => {
    expect(contextUtilisation(1_000, 1_000, 0)).toBeNull();
    expect(contextUtilisation(1_000, 1_000, -1)).toBeNull();
    expect(contextUtilisation(1_000, 1_000, Number.NaN)).toBeNull();
  });

  it("ignores negative token figures rather than going below zero", () => {
    expect(contextUtilisation(-5, 100, 1_000)).toBeCloseTo(0.1);
  });
});
