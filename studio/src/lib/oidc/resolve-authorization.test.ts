import { describe, expect, it } from "vitest";
import {
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
    ).toMatchObject({ kind: "unavailable", status: 502 });
  });
});
