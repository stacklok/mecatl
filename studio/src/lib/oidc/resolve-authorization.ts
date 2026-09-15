/**
 * The external-mode Authorization ordering for `server-proxy.ts` (requirement
 * H3.2), pure so the fallback ordering is unit-testable:
 *
 * - OIDC NOT configured → the static `MECATL_AUTH_TOKEN` path, byte-identical
 *   to the pre-H3 behavior (normalized, or no header at all when unset).
 * - OIDC configured → the OIDC outcome DECIDES; the static token is never a
 *   fallback (a static token cannot satisfy an OIDC-protected daemon, and
 *   silently downgrading would mask an expired session).
 */
import type { BearerOutcome } from "./token-store";

export type ProxyAuthDecision =
  | { kind: "none" }
  | { kind: "bearer"; token: string }
  | {
      kind: "unauthorized";
      status: 401;
      code: "oidc_login_required" | "oidc_session_expired";
      error: string;
    }
  | { kind: "unavailable"; status: 502; error: string };

/** The exact normalization the proxy has always applied to MECATL_AUTH_TOKEN. */
export function normalizeStaticToken(raw: string | undefined): string {
  return (raw ?? "").trim().replace(/^Bearer\s+/i, "");
}

export function resolveProxyAuthorization(options: {
  oidcConfigured: boolean;
  /** The token store's outcome; only read when oidcConfigured. */
  oidc: BearerOutcome | null;
  staticToken: string | undefined;
}): ProxyAuthDecision {
  if (!options.oidcConfigured) {
    const token = normalizeStaticToken(options.staticToken);
    return token ? { kind: "bearer", token } : { kind: "none" };
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
    error:
      "Could not refresh the OIDC access token — the identity provider may be unreachable. Try again shortly.",
  };
}
