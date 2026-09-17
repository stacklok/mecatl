/**
 * The chat's coarse PHASE, the web analogue of mecatui's `phase` (the TUI's
 * window title and resume notice both key off it): what the open chat is
 * doing from the user's point of view, folded from two independent sources.
 *
 * - `idle`: nothing in flight. A failed turn is `idle` too — the failure is
 *   rendered in-page (retry banner), so the tab has nothing to add; the TUI's
 *   ✗ is a CLIENT fatal, which Studio expresses as the offline connection.
 * - `running`: a turn is streaming, in this tab or driven elsewhere.
 * - `awaiting`: the run is parked on the user — a permission ask, an MCP
 *   browser sign-in, or a clarification question. All three need the user
 *   back in the chat before anything else happens, so they share one phase.
 */
export type ChatPhase = "idle" | "running" | "awaiting";

/**
 * Folds the chat hook's `ChatStatus` (`status`, what THIS tab observes on its
 * own stream) with the inventory row's daemon lifecycle `state`
 * (SessionSummary.state: idle/running/awaiting/completed/failed/cancelled),
 * so a run driven elsewhere — a schedule fire, another tab (ADR 0250) — still
 * reaches the phase before this tab's watch attaches. `awaiting` wins over
 * `running` from either source: a parked run needs the user more than a
 * streaming one. Unknown values fold to `idle`, never throw.
 */
export function deriveChatPhase(
  status: string | undefined,
  sessionState: string | undefined,
): ChatPhase {
  const fromStatus = phaseFromStatus(status);
  const fromState = phaseFromState(sessionState);
  if (fromStatus === "awaiting" || fromState === "awaiting") return "awaiting";
  if (fromStatus === "running" || fromState === "running") return "running";
  return "idle";
}

function phaseFromStatus(status: string | undefined): ChatPhase {
  switch (status) {
    case "streaming":
      return "running";
    case "waiting_approval":
    case "waiting_authorization":
    case "waiting_clarification":
      return "awaiting";
    default:
      // "idle", "error", undefined, or a status this fold has not met.
      return "idle";
  }
}

function phaseFromState(state: string | undefined): ChatPhase {
  switch (state) {
    case "running":
      return "running";
    case "awaiting":
      return "awaiting";
    default:
      return "idle";
  }
}
