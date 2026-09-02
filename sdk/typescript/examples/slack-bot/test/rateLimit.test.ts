import { describe, expect, it, vi } from "vitest";

import { SlidingWindowRateLimiter } from "../src/rateLimit.js";

describe("SlidingWindowRateLimiter", () => {
  it("allows up to max hits within the window, then blocks", () => {
    const limiter = new SlidingWindowRateLimiter(2, 1000);
    expect(limiter.allow("u1")).toBe(true);
    expect(limiter.allow("u1")).toBe(true);
    expect(limiter.allow("u1")).toBe(false);
  });

  it("tracks each key independently", () => {
    const limiter = new SlidingWindowRateLimiter(1, 1000);
    expect(limiter.allow("u1")).toBe(true);
    expect(limiter.allow("u2")).toBe(true);
    expect(limiter.allow("u1")).toBe(false);
  });

  it("allows again once the window elapses", () => {
    vi.useFakeTimers();
    try {
      const limiter = new SlidingWindowRateLimiter(1, 1000);
      expect(limiter.allow("u1")).toBe(true);
      expect(limiter.allow("u1")).toBe(false);
      vi.advanceTimersByTime(1001);
      expect(limiter.allow("u1")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
