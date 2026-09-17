/**
 * The external-mode Authorization ordering for `server-proxy.ts` (requirement
 * H3.2), pure so the fallback ordering is unit-testable:
 *
 * - OIDC NOT configured → the static `MECATL_AUTH_TOKEN` path, byte-identical
 *   to the pre-H3 behavior (normalized, or no header at all when unset).
 * - OIDC configured → the OIDC outcome DECIDES; the static token is never a
 *   fallback (a static token cannot satisfy an OIDC-protected daemon, and
 *   silently downgrading would mask an expired session).
 *
 * Two explicit deployment knobs, both default-off so the above stays
 * byte-identical (the `mecatui connect` credential ordering):
 *
 * - `preferStatic` (MECATL_AUTH_PREFER_STATIC=1) — a non-empty static token
 *   OUTRANKS the OIDC enrolment, mecatui's "static bearer is the
 *   highest-priority credential source"; an empty one falls through to OIDC.
 * - `anonymous` (MECATL_AUTH_ANONYMOUS=1) — no bearer is ever injected, even
 *   with OIDC configured or a static token set (`--anonymous`). The daemon's
 *   own 401 then reaches the client verbatim.
 */
import type { BearerOutcome } from "./token-store";

export type AuthMode = "oidc" | "static" | "anonymous";

/** Which credential the proxy is injecting for the settings card's
 * "Authentication" row — computed from configuration alone, never from a
 * token value. */
export function authMode(options: {
  oidcConfigured: boolean;
  staticToken: string | undefined;
  preferStatic?: boolean;
  anonymous?: boolean;
}): AuthMode {
  if (options.anonymous) return "anonymous";
  const token = normalizeStaticToken(options.staticToken);
  if (options.preferStatic && token) return "static";
  if (options.oidcConfigured) return "oidc";
  return token ? "static" : "anonymous";
}

export type ProxyAuthDecision =
  | { kind: "none" }
  | { kind: "bearer"; token: string }
  | {
      kind: "unauthorized";
      status: 401;
      code: "oidc_login_required" | "oidc_session_expired";
      error: string;
    }
  | {
      kind: "unavailable";
      status: 502;
      /** Stable machine code so the client can tell an identity-provider
       *  outage from a daemon outage (both would otherwise read as 5xx). */
      code: "oidc_idp_unavailable";
      error: string;
    };

/** The exact normalization the proxy has always applied to MECATL_AUTH_TOKEN. */
export function normalizeStaticToken(raw: string | undefined): string {
  return (raw ?? "").trim().replace(/^Bearer\s+/i, "");
}

export function resolveProxyAuthorization(options: {
  oidcConfigured: boolean;
  /** The token store's outcome; only read when oidcConfigured. */
  oidc: BearerOutcome | null;
  staticToken: string | undefined;
  /** MECATL_AUTH_PREFER_STATIC=1 — see the module comment. */
  preferStatic?: boolean;
  /** MECATL_AUTH_ANONYMOUS=1 — see the module comment. */
  anonymous?: boolean;
}): ProxyAuthDecision {
  if (options.anonymous) return { kind: "none" };
  const staticToken = normalizeStaticToken(options.staticToken);
  if (!options.oidcConfigured || (options.preferStatic && staticToken)) {
    return staticToken
      ? { kind: "bearer", token: staticToken }
      : { kind: "none" };
  }
  const outcome = options.oidc;
  if (outcome?.kind === "bearer") {
    return { kind: "bearer", token: outcome.token };
  }
  if (outcome?.kind === "login-required") {
    return outcome.reason === "expired"
      ? {
          kind: "unauthorized",
          status: 401,
          code: "oidc_session_expired",
          error:
            "The OIDC session expired — sign in again from Settings to keep using this deployment.",
        }
      : {
          kind: "unauthorized",
          status: 401,
          code: "oidc_login_required",
          error:
            "This deployment requires OIDC sign-in — open Settings and sign in to continue.",
        };
  }
  return {
    kind: "unavailable",
    status: 502,
    code: "oidc_idp_unavailable",
    error:
      "Could not refresh the OIDC access token — the identity provider may be unreachable. Try again shortly.",
  };
}
