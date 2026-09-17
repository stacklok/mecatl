import { afterEach, describe, expect, it, vi } from "vitest";
import { diagnosticToken } from "./harness/diagnostics-report";
import {
  browserFamily,
  studioBuild,
  studioPlatform,
  UNKNOWN_BUILD,
} from "./studio-build";

/**
 * Studio's client identity tokens for bug reports: the build stamp reads
 * the inlined NEXT_PUBLIC value or "dev", and the platform token is a
 * coarse OS/browser-family pair that always survives the diagnostics
 * report's sanitizer (so it never reads "unavailable" for a real browser).
 */

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("studioBuild", () => {
  it("reads the inlined build stamp", () => {
    vi.stubEnv("NEXT_PUBLIC_STUDIO_BUILD", "0.1.0+abc1234");
    expect(studioBuild()).toBe("0.1.0+abc1234");
    // The stamp is a report token: the sanitizer must pass it through.
    expect(diagnosticToken(studioBuild())).toBe("0.1.0+abc1234");
  });

  it("falls back to dev when the build set nothing", () => {
    vi.stubEnv("NEXT_PUBLIC_STUDIO_BUILD", "");
    expect(studioBuild()).toBe(UNKNOWN_BUILD);
    vi.stubEnv("NEXT_PUBLIC_STUDIO_BUILD", "   ");
    expect(studioBuild()).toBe(UNKNOWN_BUILD);
  });
});

describe("browserFamily", () => {
  const chrome =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.6613.84 Safari/537.36";
  const edge = `${chrome} Edg/128.0.2739.42`;
  const firefox =
    "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0";
  const safari =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15";

  it("names the family with its major version only", () => {
    expect(browserFamily(chrome)).toBe("Chrome-128");
    expect(browserFamily(edge)).toBe("Edge-128");
    expect(browserFamily(firefox)).toBe("Firefox-130");
    expect(browserFamily(safari)).toBe("Safari-17");
  });

  it("reads browser for an unrecognised agent", () => {
    expect(browserFamily("")).toBe("browser");
    expect(browserFamily("curl/8.7.1")).toBe("browser");
  });
});

describe("studioPlatform", () => {
  it("prefers the coarse userAgentData platform", () => {
    expect(
      studioPlatform({
        userAgentData: { platform: "macOS" },
        platform: "MacIntel",
        userAgent: "… Chrome/128.0.0.0 Safari/537.36",
      }),
    ).toBe("macOS/Chrome-128");
  });

  it("makes the legacy platform report-safe", () => {
    const token = studioPlatform({
      platform: "Linux x86_64",
      userAgent: "… Firefox/130.0",
    });
    expect(token).toBe("Linux-x86_64/Firefox-130");
    expect(diagnosticToken(token)).toBe(token);
  });

  it("reads browser when nothing is known", () => {
    expect(studioPlatform(undefined)).toBe("browser");
    expect(studioPlatform({})).toBe("browser");
  });
});
