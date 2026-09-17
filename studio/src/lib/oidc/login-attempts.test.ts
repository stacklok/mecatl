import { describe, expect, it } from "vitest";
import {
  callbackTimeoutSeconds,
  DEFAULT_CALLBACK_TIMEOUT_SECONDS,
  LoginAttempts,
  MAX_CALLBACK_TIMEOUT_SECONDS,
  MIN_CALLBACK_TIMEOUT_SECONDS,
} from "./login-attempts";

/** The pending-attempt table honours MECATL_OIDC_CALLBACK_TIMEOUT (the
 * `--callback-timeout` analogue), consumes each state exactly once, and
 * stays bounded. */

describe("callbackTimeoutSeconds", () => {
  it("defaults to 600 s and clamps a valid value to [30 s, 24 h]", () => {
    expect(callbackTimeoutSeconds(undefined)).toBe(
      DEFAULT_CALLBACK_TIMEOUT_SECONDS,
    );
    expect(callbackTimeoutSeconds("")).toBe(DEFAULT_CALLBACK_TIMEOUT_SECONDS);
    expect(callbackTimeoutSeconds("ten")).toBe(
      DEFAULT_CALLBACK_TIMEOUT_SECONDS,
    );
    expect(callbackTimeoutSeconds("-5")).toBe(DEFAULT_CALLBACK_TIMEOUT_SECONDS);
    expect(callbackTimeoutSeconds("0")).toBe(DEFAULT_CALLBACK_TIMEOUT_SECONDS);
    expect(callbackTimeoutSeconds(" 120 ")).toBe(120);
    expect(callbackTimeoutSeconds("5")).toBe(MIN_CALLBACK_TIMEOUT_SECONDS);
    expect(callbackTimeoutSeconds("999999")).toBe(MAX_CALLBACK_TIMEOUT_SECONDS);
  });
});

describe("LoginAttempts", () => {
  const NOW = 1_700_000_000_000;

  it("expires an attempt after the injected timeout and consumes it once", () => {
    let now = NOW;
    let ttl = 120_000;
    const attempts = new LoginAttempts({ ttlMs: () => ttl, now: () => now });
    const first = attempts.begin("s1", "v1");
    expect(first.expires).toBe(NOW + 120_000);
    // The TTL is read per attempt (an env change takes effect live).
    ttl = 30_000;
    expect(attempts.begin("s2", "v2").expires).toBe(NOW + 30_000);

    now = NOW + 119_999;
    expect(attempts.take("s1")).toEqual({
      verifier: "v1",
      expires: NOW + 120_000,
    });
    // Consumed: a replayed callback finds nothing.
    expect(attempts.take("s1")).toBeNull();

    now = NOW + 30_001;
    expect(attempts.take("s2")).toBeNull();
    expect(attempts.take("unknown")).toBeNull();
    expect(attempts.take("")).toBeNull();
  });

  it("prunes expired attempts and evicts the oldest past the bound", () => {
    let now = NOW;
    const attempts = new LoginAttempts({
      ttlMs: () => 10_000,
      now: () => now,
      max: 3,
    });
    attempts.begin("a", "va");
    attempts.begin("b", "vb");
    attempts.begin("c", "vc");
    attempts.begin("d", "vd"); // overflow: "a" is evicted
    expect(attempts.size).toBe(3);
    expect(attempts.take("a")).toBeNull();
    expect(attempts.take("d")).not.toBeNull();

    now = NOW + 20_000;
    attempts.begin("e", "ve"); // prune drops b and c
    expect(attempts.size).toBe(1);
  });
});
