import { describe, expect, it } from "vitest";
import { PAGE_FRACTION, scrollPositionPercent } from "./scroll-position";

describe("scrollPositionPercent", () => {
  it("reads 0 at the top and 100 at the bottom", () => {
    expect(scrollPositionPercent(0, 1000, 400)).toBe(0);
    expect(scrollPositionPercent(600, 1000, 400)).toBe(100);
  });

  it("reads 100 when the content does not overflow (the end is in view)", () => {
    expect(scrollPositionPercent(0, 400, 400)).toBe(100);
    expect(scrollPositionPercent(0, 300, 400)).toBe(100);
  });

  it("rounds to a whole percent", () => {
    // 200 / 600 = 33.33…
    expect(scrollPositionPercent(200, 1000, 400)).toBe(33);
    // 400 / 600 = 66.66…
    expect(scrollPositionPercent(400, 1000, 400)).toBe(67);
    // 3 / 600 = 0.5 → rounds up
    expect(scrollPositionPercent(3, 1000, 400)).toBe(1);
  });

  it("clamps over-scroll (rubber-banding) into 0..100", () => {
    expect(scrollPositionPercent(-40, 1000, 400)).toBe(0);
    expect(scrollPositionPercent(650, 1000, 400)).toBe(100);
  });

  it("never leaks NaN from an unmeasured layout", () => {
    expect(scrollPositionPercent(Number.NaN, Number.NaN, Number.NaN)).toBe(100);
  });
});

describe("PAGE_FRACTION", () => {
  it("moves most of a viewport but keeps a line of context", () => {
    expect(PAGE_FRACTION).toBeGreaterThan(0.5);
    expect(PAGE_FRACTION).toBeLessThan(1);
  });
});
