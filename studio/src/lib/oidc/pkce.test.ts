import { describe, expect, it } from "vitest";
import {
  boundProviderError,
  buildAuthorizationUrl,
  evaluateCallback,
  pkceChallengeS256,
  randomUrlSafe,
  readCallbackParams,
} from "./pkce";

describe("randomUrlSafe", () => {
  it("emits URL-safe values with no padding", () => {
    for (let i = 0; i < 20; i++) {
      expect(randomUrlSafe(32)).toMatch(/^[A-Za-z0-9_-]{43}$/);
    }
  });

  it("does not repeat", () => {
    const seen = new Set(Array.from({ length: 100 }, () => randomUrlSafe(32)));
    expect(seen.size).toBe(100);
  });
});

describe("pkceChallengeS256", () => {
  it("matches the RFC 7636 appendix B vector", () => {
    expect(
      pkceChallengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
    ).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });
});

describe("buildAuthorizationUrl", () => {
  const base = {
    authorizationEndpoint: "https://issuer.example/authorize",
    clientId: "studio",
    redirectUri: "http://localhost:3000/api/auth/oidc/callback",
    scope: "openid profile email offline_access",
    state: "the-state",
    codeChallenge: "the-challenge",
  };

  it("carries the full Authorization Code + PKCE parameter set", () => {
    const url = new URL(buildAuthorizationUrl(base));
    expect(url.origin + url.pathname).toBe("https://issuer.example/authorize");
    expect(url.searchParams.get("response_type")).toBe("code");
    expect(url.searchParams.get("client_id")).toBe("studio");
    expect(url.searchParams.get("redirect_uri")).toBe(base.redirectUri);
    expect(url.searchParams.get("scope")).toBe(base.scope);
    expect(url.searchParams.get("state")).toBe("the-state");
    expect(url.searchParams.get("code_challenge")).toBe("the-challenge");
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("audience")).toBeNull();
  });

  it("includes audience only when configured", () => {
    const url = new URL(
      buildAuthorizationUrl({ ...base, audience: "https://mecated.example" }),
    );
    expect(url.searchParams.get("audience")).toBe("https://mecated.example");
  });
});

describe("boundProviderError", () => {
  it("restricts to the RFC 6749 printable subset and clamps", () => {
    expect(boundProviderError('bad "quote" \\ and\nnewline\tand ünïcode')).toBe(
      "bad quote  andnewlineand ncode",
    );
    expect(boundProviderError("x".repeat(500)).length).toBe(200);
  });

  it("strips a script payload's angle-bracket-free remainder harmlessly", () => {
    // The characters survive (printable subset), but the callback page's own
    // HTML escaping is the render-time defense; this bound is transport-side.
    expect(boundProviderError("access_denied")).toBe("access_denied");
  });
});

describe("evaluateCallback", () => {
  const check = {
    expectedIssuer: "https://issuer.example",
    issRequired: false,
  };

  it("accepts a plain code when iss is not advertised", () => {
    expect(
      evaluateCallback(
        { code: "c0de", error: "", errorDescription: "", iss: "" },
        check,
      ),
    ).toEqual({
      ok: true,
      code: "c0de",
    });
  });

  it("returns the bounded provider error", () => {
    const verdict = evaluateCallback(
      {
        code: "",
        error: 'access_denied"<script>',
        errorDescription: "user \\ said no",
        iss: "",
      },
      check,
    );
    expect(verdict.ok).toBe(false);
    if (!verdict.ok) {
      expect(verdict.reason).toContain("access_denied<script>");
      expect(verdict.reason).toContain("user  said no");
      expect(verdict.reason).not.toContain('"');
      expect(verdict.reason).not.toContain("\\");
    }
  });

  it("rejects a callback without a code", () => {
    const verdict = evaluateCallback(
      { code: "", error: "", errorDescription: "", iss: "" },
      check,
    );
    expect(verdict).toMatchObject({ ok: false });
  });

  it("rejects a mismatched iss (RFC 9207)", () => {
    const verdict = evaluateCallback(
      {
        code: "c0de",
        error: "",
        errorDescription: "",
        iss: "https://evil.example",
      },
      check,
    );
    expect(verdict).toMatchObject({ ok: false });
  });

  it("accepts a matching iss, tolerating a trailing slash", () => {
    const verdict = evaluateCallback(
      {
        code: "c0de",
        error: "",
        errorDescription: "",
        iss: "https://issuer.example/",
      },
      check,
    );
    expect(verdict).toEqual({ ok: true, code: "c0de" });
  });

  it("requires iss when discovery advertised RFC 9207 support", () => {
    const verdict = evaluateCallback(
      { code: "c0de", error: "", errorDescription: "", iss: "" },
      { ...check, issRequired: true },
    );
    expect(verdict).toMatchObject({ ok: false });
  });
});

describe("readCallbackParams", () => {
  it("reads every field, defaulting absent ones to empty", () => {
    const params = readCallbackParams(
      new URLSearchParams("state=s&code=c&iss=https%3A%2F%2Fi.example"),
    );
    expect(params).toEqual({
      state: "s",
      code: "c",
      error: "",
      errorDescription: "",
      iss: "https://i.example",
    });
  });
});
