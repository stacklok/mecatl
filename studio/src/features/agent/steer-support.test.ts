import { describe, expect, it } from "vitest";
import { resolveSteerSupported } from "./steer-support";

/**
 * The steer gate folds the daemon's live `capabilities.steer` (the operator's
 * decision, always a boolean when present) over the STATIC `http_steer`
 * feature row (the route exists — listed even with steering disabled). The
 * capability decides whenever it is present; the row is only the fallback
 * for an older daemon without the key.
 */
describe("resolveSteerSupported", () => {
  const none = new Set<string>();
  const routeExists = new Set(["http_steer"]);

  it("is on when the daemon reports the capability", () => {
    expect(resolveSteerSupported({ steer: true }, none)).toBe(true);
    expect(resolveSteerSupported({ steer: true }, routeExists)).toBe(true);
  });

  it("is OFF when the daemon reports steer: false, even though the static http_steer row is listed", () => {
    // `mecated --no-steer` / an operator-tier `steer: false`: the daemon
    // still lists the route; the capability is the truth.
    expect(resolveSteerSupported({ steer: false }, routeExists)).toBe(false);
    expect(resolveSteerSupported({ steer: false }, none)).toBe(false);
  });

  it("falls back to the feature row only when the capability key is absent", () => {
    expect(resolveSteerSupported({}, routeExists)).toBe(true);
    expect(resolveSteerSupported({}, none)).toBe(false);
    // A malformed capability value is treated as absent, never as true.
    expect(resolveSteerSupported({ steer: "yes" }, none)).toBe(false);
  });
});
