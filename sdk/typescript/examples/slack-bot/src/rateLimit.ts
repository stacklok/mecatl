/**
 * A simple in-memory sliding-window rate limiter, keyed by an arbitrary
 * string (here: a Slack user ID). Mitigates unbounded spend/abuse from a
 * single reachable user (panel-review finding, #883) — it is NOT a
 * per-thread or global budget; see bridge.ts for that still-open gap.
 */
export class SlidingWindowRateLimiter {
  readonly #max: number;
  readonly #windowMs: number;
  readonly #hits = new Map<string, number[]>();

  constructor(max: number, windowMs: number) {
    this.#max = max;
    this.#windowMs = windowMs;
  }

  /** Records one hit for `key` and returns whether it's within the allowed rate. */
  allow(key: string): boolean {
    const now = Date.now();
    const cutoff = now - this.#windowMs;
    const recent = (this.#hits.get(key) ?? []).filter((ts) => ts > cutoff);
    if (recent.length >= this.#max) {
      this.#hits.set(key, recent);
      return false;
    }
    recent.push(now);
    this.#hits.set(key, recent);
    return true;
  }
}
