import { describe, expect, it } from "vitest";
import {
  authMode,
  normalizeStaticToken,
  resolveProxyAuthorization,
} from "./resolve-authorization";

describe("normalizeStaticToken", () => {
  it("applies the proxy's historical MECATL_AUTH_TOKEN normalization", () => {
    expect(normalizeStaticToken("  abc  ")).toBe("abc");
    expect(normalizeStaticToken("Bearer abc")).toBe("abc");
    expect(normalizeStaticToken("bearer   abc")).toBe("abc");
    expect(normalizeStaticToken(undefined)).toBe("");
    expect(normalizeStaticToken("   ")).toBe("");
  });
});

describe("resolveProxyAuthorization ordering", () => {
  it("without OIDC config: static token injected, unset means no header (pre-H3 behavior)", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: false,
        oidc: null,
        staticToken: "Bearer static-1",
      }),
    ).toEqual({ kind: "bearer", token: "static-1" });
    expect(
      resolveProxyAuthorization({
        oidcConfigured: false,
        oidc: null,
        staticToken: undefined,
      }),
    ).toEqual({ kind: "none" });
  });

  it("with OIDC config: the OIDC token wins and the static token is never a fallback", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "bearer", token: "oidc-1" },
        staticToken: "static-1",
      }),
    ).toEqual({ kind: "bearer", token: "oidc-1" });

    // Signed out: 401 with actionable copy — NOT a silent static downgrade.
    const signedOut = resolveProxyAuthorization({
      oidcConfigured: true,
      oidc: { kind: "login-required", reason: "signed-out" },
      staticToken: "static-1",
    });
    expect(signedOut).toMatchObject({
      kind: "unauthorized",
      status: 401,
      code: "oidc_login_required",
    });
  });

  it("surfaces an expired session as 401 with 'sign in again' copy", () => {
    const expired = resolveProxyAuthorization({
      oidcConfigured: true,
      oidc: { kind: "login-required", reason: "expired" },
      staticToken: undefined,
    });
    expect(expired).toMatchObject({
      kind: "unauthorized",
      status: 401,
      code: "oidc_session_expired",
    });
    if (expired.kind === "unauthorized") {
      expect(expired.error.toLowerCase()).toContain("sign in again");
    }
  });

  it("maps a transient refresh failure to 502, not a sign-in prompt", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "refresh-failed" },
        staticToken: "static-1",
      }),
    ).toMatchObject({
      kind: "unavailable",
      status: 502,
      // The stable code the proxy relays so the client can tell an
      // identity-provider outage from a daemon outage (both are 5xx).
      code: "oidc_idp_unavailable",
    });
  });
});

describe("resolveProxyAuthorization knobs (mecatui's credential ordering)", () => {
  it("preferStatic: a non-empty static token outranks a signed-in OIDC enrolment", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "bearer", token: "oidc-1" },
        staticToken: "Bearer static-1",
        preferStatic: true,
      }),
    ).toEqual({ kind: "bearer", token: "static-1" });
    // ...and over a signed-out one (no 401 — the static credential decides).
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "login-required", reason: "signed-out" },
        staticToken: "static-1",
        preferStatic: true,
      }),
    ).toEqual({ kind: "bearer", token: "static-1" });
  });

  it("preferStatic with an empty static token falls through to the OIDC decision", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "login-required", reason: "signed-out" },
        staticToken: "   ",
        preferStatic: true,
      }),
    ).toMatchObject({ kind: "unauthorized", code: "oidc_login_required" });
  });

  it("anonymous: never injects a bearer, even with OIDC configured or a static token", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "bearer", token: "oidc-1" },
        staticToken: "static-1",
        anonymous: true,
      }),
    ).toEqual({ kind: "none" });
    expect(
      resolveProxyAuthorization({
        oidcConfigured: false,
        oidc: null,
        staticToken: "static-1",
        anonymous: true,
        preferStatic: true,
      }),
    ).toEqual({ kind: "none" });
  });

  it("defaults (both knobs unset) keep the H3 ordering byte-identical", () => {
    expect(
      resolveProxyAuthorization({
        oidcConfigured: true,
        oidc: { kind: "login-required", reason: "signed-out" },
        staticToken: "static-1",
        preferStatic: false,
        anonymous: false,
      }),
    ).toMatchObject({ kind: "unauthorized", code: "oidc_login_required" });
  });
});

describe("authMode", () => {
  it("names the credential the proxy injects from configuration alone", () => {
    expect(authMode({ oidcConfigured: true, staticToken: "s" })).toBe("oidc");
    expect(
      authMode({ oidcConfigured: true, staticToken: "s", preferStatic: true }),
    ).toBe("static");
    expect(
      authMode({ oidcConfigured: true, staticToken: "", preferStatic: true }),
    ).toBe("oidc");
    expect(authMode({ oidcConfigured: false, staticToken: "Bearer s" })).toBe(
      "static",
    );
    expect(authMode({ oidcConfigured: false, staticToken: undefined })).toBe(
      "anonymous",
    );
    expect(
      authMode({ oidcConfigured: true, staticToken: "s", anonymous: true }),
    ).toBe("anonymous");
  });
});
