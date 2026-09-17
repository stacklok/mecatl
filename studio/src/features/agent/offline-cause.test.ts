import { describe, expect, it } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import {
  authFailureCause,
  authFailureMessage,
  CONNECTIVITY_TITLE,
  classifyOffline,
} from "./offline-cause";

/**
 * The offline-cause matrix: the proxy's OIDC codes name sign-in states, a
 * bare 401/403 is a rejected credential (never "the daemon is down"), the
 * identity-provider outage is its own class, and everything else — a 503,
 * a refused connection — keeps the plain connectivity framing.
 */

describe("classifyOffline", () => {
  it("oidc_login_required → sign-in required, Sign in", () => {
    const cause = classifyOffline({
      status: 401,
      code: "oidc_login_required",
      detail:
        "This deployment requires OIDC sign-in — open Settings and sign in to continue.",
    });
    expect(cause.kind).toBe("login-required");
    expect(cause.title).toBe("Sign-in required");
    expect(cause.signIn).toBe("sign-in");
    expect(cause.remedy).toMatch(/sign in/i);
    expect(cause.detail).toMatch(/requires OIDC sign-in/);
  });

  it("oidc_session_expired → session expired, Sign in again", () => {
    const cause = classifyOffline({
      status: 401,
      code: "oidc_session_expired",
      detail:
        "The OIDC session expired — sign in again from Settings to keep using this deployment.",
    });
    expect(cause.kind).toBe("session-expired");
    expect(cause.title).toBe("Sign-in expired");
    expect(cause.signIn).toBe("sign-in-again");
  });

  it("a daemon 401 `unauthenticated` is a rejected credential with env remediation", () => {
    const cause = classifyOffline({
      status: 401,
      code: "unauthenticated",
      detail: "missing or invalid bearer token",
    });
    expect(cause.kind).toBe("credential-rejected");
    expect(cause.title).toBe("Credential rejected");
    expect(cause.remedy).toMatch(/check its token or sign-in settings/);
    expect(cause.remedy).toMatch(/sign-in settings/);
    expect(cause.remedy).toMatch(/sign-in settings/);
    expect(cause.signIn).toBeNull();
    expect(cause.detail).toBe("missing or invalid bearer token");
  });

  it("a 401 with no code at all is still a rejected credential (pre-problem-details daemon)", () => {
    expect(
      classifyOffline({
        status: 401,
        code: "",
        detail: "Authentication failed",
      }).kind,
    ).toBe("credential-rejected");
  });

  it("a 403 is a rejected credential too", () => {
    expect(
      classifyOffline({ status: 403, code: "forbidden", detail: "forbidden" })
        .kind,
    ).toBe("credential-rejected");
  });

  it("502 oidc_idp_unavailable → identity provider unreachable, no sign-in action", () => {
    const cause = classifyOffline({
      status: 502,
      code: "oidc_idp_unavailable",
      detail:
        "Could not refresh the OIDC access token — the identity provider may be unreachable. Try again shortly.",
    });
    expect(cause.kind).toBe("idp-unavailable");
    expect(cause.title).toBe("Identity provider unreachable");
    expect(cause.signIn).toBeNull();
  });

  it("a code-less 502 whose detail names the OIDC refresh still classifies as idp-unavailable", () => {
    expect(
      classifyOffline({
        status: 502,
        code: "",
        detail: "Could not refresh the OIDC access token.",
      }).kind,
    ).toBe("idp-unavailable");
    // A plain upstream 502 stays connectivity: no OIDC vocabulary in it.
    expect(
      classifyOffline({ status: 502, code: "", detail: "bad gateway" }).kind,
    ).toBe("connectivity");
  });

  it("a 503 (draining) and a refused connection (status 0) are plain connectivity", () => {
    const draining = classifyOffline({
      status: 503,
      code: "draining",
      detail: "The daemon is restarting — try again in a moment.",
    });
    expect(draining.kind).toBe("connectivity");
    expect(draining.title).toBe(CONNECTIVITY_TITLE);
    expect(draining.detail).toMatch(/restarting/);
    expect(
      classifyOffline({ status: 0, code: "transport", detail: "fetch failed" })
        .kind,
    ).toBe("connectivity");
  });
});

describe("authFailureCause / authFailureMessage", () => {
  it("names a chat-level 401 and an oidc_* proxy refusal; ignores everything else", () => {
    expect(
      authFailureCause(
        new HarnessApiError(401, "oidc_session_expired", "expired"),
      )?.kind,
    ).toBe("session-expired");
    expect(
      authFailureCause(new HarnessApiError(401, "unauthenticated", "nope"))
        ?.kind,
    ).toBe("credential-rejected");
    expect(
      authFailureCause(
        new HarnessApiError(502, "oidc_idp_unavailable", "idp down"),
      )?.kind,
    ).toBe("idp-unavailable");
    // A 409 busy, a 404, a transport fault, a non-harness error: not auth.
    expect(
      authFailureCause(new HarnessApiError(409, "stale_run_control", "x")),
    ).toBeNull();
    expect(authFailureCause(new HarnessApiError(404, "", "x"))).toBeNull();
    expect(
      authFailureCause(new HarnessApiError(0, "transport", "x")),
    ).toBeNull();
    expect(authFailureCause(new Error("boom"))).toBeNull();
    expect(authFailureCause(undefined)).toBeNull();
  });

  it("renders title — remedy as one strip line", () => {
    const message = authFailureMessage(
      new HarnessApiError(401, "oidc_login_required", "sign in"),
    );
    expect(message).toMatch(/^Sign-in required — /);
    expect(message).toMatch(/Sign in to reconnect/);
    expect(authFailureMessage(new Error("boom"))).toBeNull();
  });
});
