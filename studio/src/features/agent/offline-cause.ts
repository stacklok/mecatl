/**
 * Names WHY the daemon is unreachable, from the liveness probe's typed
 * failure (`HarnessStatus.status` / `.code`, `inventory.ts`). Pure: the
 * runtime-status provider derives one `OfflineCause` per offline state and
 * renders the auth-recovery banner for everything that is not plain
 * connectivity, instead of misframing a refused credential as "the daemon is
 * down" (the TUI's `/connect` recovery overlay names the cause class the
 * same way).
 *
 * The codes come from two tiers. Studio's own same-origin proxy answers a
 * signed-out / expired OIDC session with 401 `oidc_login_required` /
 * `oidc_session_expired` and a failed token refresh with 502
 * `oidc_idp_unavailable` (`src/lib/oidc/resolve-authorization.ts`) — it
 * refuses to dial the deployment anonymously. The daemon itself answers a
 * rejected bearer with 401 `unauthenticated` (RFC 9457) or a 403.
 */

import { HarnessApiError } from "@/lib/harness/errors";

export const OIDC_LOGIN_REQUIRED = "oidc_login_required";
export const OIDC_SESSION_EXPIRED = "oidc_session_expired";
export const OIDC_IDP_UNAVAILABLE = "oidc_idp_unavailable";

export type OfflineCauseKind =
  | "connectivity"
  | "login-required"
  | "session-expired"
  | "credential-rejected"
  | "idp-unavailable";

export interface OfflineCause {
  kind: OfflineCauseKind;
  /** The banner heading: "Sign-in required", "Sign-in expired", … */
  title: string;
  /** What the user (or the operator) can do about it. */
  remedy: string;
  /** The server's own words, verbatim, for the detail line. */
  detail: string;
  /** Which sign-in action fits the cause; null when signing in cannot help
   *  (a rejected static token, an unreachable identity provider). */
  signIn: "sign-in" | "sign-in-again" | null;
}

export interface OfflineProbe {
  /** HTTP status of the failed probe; 0 when nothing answered. */
  status: number;
  /** Stable machine code from the problem body; "" when absent. */
  code: string;
  /** The translated error message. */
  detail: string;
}

/** The copy the plain-connectivity banner has always shown. */
export const CONNECTIVITY_TITLE = "Mecatl is unreachable.";
export const CONNECTIVITY_REMEDY =
  "Run `task build`, then `task studio:dev` to start it.";

export function classifyOffline(probe: OfflineProbe): OfflineCause {
  const detail = probe.detail ?? "";
  if (probe.code === OIDC_LOGIN_REQUIRED) {
    return {
      kind: "login-required",
      title: "Sign-in required",
      remedy:
        "Sign-in is required for this agent. Sign in to reconnect — the open chat picks up where it left off.",
      detail,
      signIn: "sign-in",
    };
  }
  if (probe.code === OIDC_SESSION_EXPIRED) {
    return {
      kind: "session-expired",
      title: "Sign-in expired",
      remedy:
        "Your sign-in ended. Sign in again to reconnect — the open chat picks up where it left off.",
      detail,
      signIn: "sign-in-again",
    };
  }
  if (
    probe.code === OIDC_IDP_UNAVAILABLE ||
    (probe.status === 502 && /\bOIDC\b|identity provider/i.test(detail))
  ) {
    return {
      kind: "idp-unavailable",
      title: "Identity provider unreachable",
      remedy:
        "Studio could not refresh your sign-in. Check the sign-in service, then retry.",
      detail,
      signIn: null,
    };
  }
  if (probe.status === 401 || probe.status === 403) {
    return {
      kind: "credential-rejected",
      title: "Credential rejected",
      remedy:
        "The agent rejected Studio's credential. Ask the person who set up the agent to check its token or sign-in settings.",
      detail,
      signIn: null,
    };
  }
  return {
    kind: "connectivity",
    title: CONNECTIVITY_TITLE,
    remedy: CONNECTIVITY_REMEDY,
    detail,
    signIn: null,
  };
}

/**
 * The chat-level twin of the probe classifier: a send or retry that failed
 * because Studio's credential was refused (a 401, or any `oidc_*` code from
 * the proxy) gets the same named cause the banner shows. Null for every
 * other failure, so callers keep their own wording.
 */
export function authFailureCause(error: unknown): OfflineCause | null {
  if (!(error instanceof HarnessApiError)) return null;
  if (error.status !== 401 && !error.code.startsWith("oidc_")) return null;
  return classifyOffline({
    status: error.status,
    code: error.code,
    detail: error.message,
  });
}

/** `authFailureCause` rendered as one error-strip line: title — remedy. */
export function authFailureMessage(error: unknown): string | null {
  const cause = authFailureCause(error);
  return cause ? `${cause.title} — ${cause.remedy}` : null;
}
