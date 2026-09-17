/**
 * The mid-run MCP browser-authorization phase, as pure helpers over the chat
 * hook's state: how an `authorization` / `authorization_resolved` stream event
 * folds into the pending request, the transcript notices the phase leaves,
 * and the pop-up-blocker-safe way to open the sign-in page.
 *
 * The run is PARKED while a request is pending: the daemon closed the prompt
 * stream without a result and only the authorization controls (recheck /
 * cancel) move it again — never the run's own cancel.
 */

import {
  authorizationStatusLabel,
  isPendingAuthorizationStatus,
} from "@/lib/protocol";
import type { AuthorizationRequest, StreamEvent } from "./types";

/** The window name every sign-in opens into, so a second click reuses it. */
export const AUTHORIZATION_WINDOW_NAME = "mecatl-mcp-authorization";

/**
 * Folds one stream event into the pending authorization. A pending
 * `authorization` sets (or refreshes) the request; a terminal status — on
 * either kind — clears it when the ids match and leaves an unrelated pending
 * request alone. Every other event is a no-op.
 */
export function reduceAuthorizationEvent(
  pending: AuthorizationRequest | null,
  event: StreamEvent,
  sessionId: string,
): AuthorizationRequest | null {
  switch (event.type) {
    case "authorization": {
      if (!isPendingAuthorizationStatus(event.status)) {
        return pending?.authorizationId === event.authorizationId
          ? null
          : pending;
      }
      if (pending?.authorizationId === event.authorizationId) {
        // A recheck that found the sign-in still incomplete re-observes the
        // same request: keep it (and its notices), refresh the expiry.
        return { ...pending, expiresAt: event.expiresAt ?? pending.expiresAt };
      }
      return {
        authorizationId: event.authorizationId,
        sessionId,
        callId: event.callId,
        displayName: event.displayName,
        expiresAt: event.expiresAt,
      };
    }
    case "authorization_resolved":
      if (isPendingAuthorizationStatus(event.status)) return pending;
      return pending?.authorizationId === event.authorizationId
        ? null
        : pending;
    default:
      return pending;
  }
}

/** The MCP server's name as the notices say it (never an empty gap). */
export function authorizationServerName(displayName: string): string {
  return displayName.trim() || "an MCP server";
}

/** The transcript notice a newly parked authorization leaves on the turn. */
export function authorizationRequiredNotice(displayName: string): string {
  return `Waiting for browser authorization: ${authorizationServerName(displayName)}`;
}

/** The transcript notice a resolved authorization leaves on the turn. */
export function authorizationResolvedNotice(
  displayName: string,
  status: string,
): string {
  return `Browser authorization for ${authorizationServerName(displayName)}: ${authorizationStatusLabel(status)}`;
}

/** The panel line a still-pending recheck shows. */
export const AUTHORIZATION_STILL_PENDING_NOTICE =
  "Not signed in yet. Finish the sign-in in your browser, then re-check.";

/** The panel line a copied link shows. */
export const AUTHORIZATION_LINK_COPIED_NOTICE =
  "Sign-in link copied. Open it in any browser, then re-check.";

/** The panel line a blocked pop-up shows. */
export const AUTHORIZATION_POPUP_BLOCKED_NOTICE =
  "Your browser blocked the pop-up. Use Copy link and open it yourself.";

/** The panel line a manual re-check shows while another check is in flight. */
export const AUTHORIZATION_CHECK_IN_FLIGHT_NOTICE =
  "A check is already in progress; its result lands in a moment.";

/**
 * How often Studio re-checks a pending authorization once the sign-in page
 * was opened or its link copied — the TUI's cadence
 * (cmd/mecatui/ui/mcp_authorization.go `mcpAuthorizationPollInterval`).
 */
export const AUTHORIZATION_POLL_INTERVAL_MS = 3_000;

/**
 * Opens the sign-in page without tripping pop-up blockers: the blank window
 * is opened SYNCHRONOUSLY inside the click's task (so the browser attributes
 * it to the gesture), the URL is fetched afterwards, and the window is
 * navigated to it. The page must never reach back into Studio, so the
 * window's opener is severed before it navigates.
 *
 * Returns "blocked" when the browser refused the window; a thrown fetch
 * failure closes the blank window so no empty tab is left behind.
 */
export async function openAuthorizationWindow(
  fetchUrl: () => Promise<string>,
  open: typeof window.open = (...args) => window.open(...args),
): Promise<"opened" | "blocked"> {
  const popup = open("", AUTHORIZATION_WINDOW_NAME);
  let url: string;
  try {
    url = await fetchUrl();
  } catch (error) {
    popup?.close();
    throw error;
  }
  if (!popup || popup.closed) return "blocked";
  popup.opener = null;
  popup.location.href = url;
  return "opened";
}
