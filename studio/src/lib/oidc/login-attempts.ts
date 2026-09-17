/**
 * Pending Authorization Code + PKCE attempts for Studio's remote OIDC login:
 * the `state` → `code_verifier` table one sign-in click opens and the
 * callback consumes exactly once. Pure (injected clock) so the TTL — the
 * `mecatui login --callback-timeout` analogue, `MECATL_OIDC_CALLBACK_TIMEOUT`
 * in seconds — is unit-testable.
 */

export const DEFAULT_CALLBACK_TIMEOUT_SECONDS = 600;
/** A callback window shorter than this cannot be completed by a human. */
export const MIN_CALLBACK_TIMEOUT_SECONDS = 30;
export const MAX_CALLBACK_TIMEOUT_SECONDS = 24 * 3600;
/** Bounded table: overflow evicts the oldest attempt (Map order). */
const MAX_PENDING_ATTEMPTS = 32;

/** Parse MECATL_OIDC_CALLBACK_TIMEOUT (whole seconds). Unset, blank or
 * malformed → the 600-second default; a valid value is clamped to
 * [30 s, 24 h]. */
export function callbackTimeoutSeconds(raw: string | undefined): number {
  const trimmed = (raw ?? "").trim();
  if (!/^\d+$/.test(trimmed)) return DEFAULT_CALLBACK_TIMEOUT_SECONDS;
  const seconds = Number(trimmed);
  if (!Number.isFinite(seconds) || seconds <= 0)
    return DEFAULT_CALLBACK_TIMEOUT_SECONDS;
  return Math.min(
    MAX_CALLBACK_TIMEOUT_SECONDS,
    Math.max(MIN_CALLBACK_TIMEOUT_SECONDS, seconds),
  );
}

export type LoginAttempt = { verifier: string; expires: number };

export class LoginAttempts {
  private readonly attempts = new Map<string, LoginAttempt>();

  constructor(
    private readonly options: {
      /** Read per attempt so an env-driven timeout is honoured live. */
      ttlMs: () => number;
      now?: () => number;
      max?: number;
    },
  ) {}

  private now(): number {
    return (this.options.now ?? Date.now)();
  }

  private prune(now: number): void {
    for (const [state, attempt] of this.attempts) {
      if (attempt.expires < now) this.attempts.delete(state);
    }
    const max = this.options.max ?? MAX_PENDING_ATTEMPTS;
    while (this.attempts.size >= max) {
      const oldest = this.attempts.keys().next().value;
      if (oldest === undefined) break;
      this.attempts.delete(oldest);
    }
  }

  /** Register one attempt; returns its absolute expiry (epoch ms). */
  begin(state: string, verifier: string): LoginAttempt {
    const now = this.now();
    this.prune(now);
    const attempt = { verifier, expires: now + this.options.ttlMs() };
    this.attempts.set(state, attempt);
    return attempt;
  }

  /** Consume the attempt for `state` — deleted whether or not it is still
   * valid, so a callback can never be replayed. Null when unknown or
   * expired. */
  take(state: string): LoginAttempt | null {
    const attempt = this.attempts.get(state);
    if (state) this.attempts.delete(state);
    if (!attempt || attempt.expires < this.now()) return null;
    return attempt;
  }

  get size(): number {
    return this.attempts.size;
  }
}
