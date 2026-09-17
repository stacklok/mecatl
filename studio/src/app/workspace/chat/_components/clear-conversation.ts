/**
 * Clear conversation — the web analogue of the TUI's `/clear` built-in
 * (cmd/mecatui/ui/builtins.go `runClear`): the daemon mints a DISTINCT
 * empty-history successor of the current chat that inherits its placement,
 * model, effort, mode and limits (ClearSession, ADR 0291), and the UI moves
 * there only once the daemon has answered. The source chat is left in the
 * list untouched.
 *
 * Pure module: the shared copy and the eligibility gate the header menu,
 * the ⌘⇧X shortcut and the `/clear` built-in all read. The daemon call and
 * the handoff live in use-clear-conversation.ts.
 */

export const CLEAR_CONVERSATION_LABEL = "Clear conversation";

/** Shown in the disabled composer while the handoff is pending. */
export const CLEARING_PLACEHOLDER = "Clearing conversation…";

export const CLEARED_TOAST =
  "Conversation cleared — continuing in a fresh chat with the same settings";

/** A draft has no daemon session, so there is nothing to clear. */
export const NOTHING_TO_CLEAR = "Nothing to clear yet";

export const CLEAR_UNSUPPORTED = "This daemon cannot clear sessions.";

export const CLEAR_SOURCE_BUSY =
  "This chat is still busy — wait for it to settle, then clear.";

/** The disabled-item reason while another client drives the chat. */
export const CLEAR_ACTIVE_ELSEWHERE =
  "Running in another client — stop it there first";

/** The disabled-item reason when the row carries no successor verdict. */
export const CLEAR_ELIGIBILITY_UNKNOWN =
  "The agent did not say whether this chat can be cleared";

/**
 * What the chat's menu shows for Clear conversation: nothing (not a chat),
 * an enabled item, or a disabled item with the plain reason.
 */
export type ClearConversationGate =
  | { readonly kind: "hidden" }
  | { readonly kind: "enabled" }
  | { readonly kind: "disabled"; readonly reason: string };

const HIDDEN: ClearConversationGate = { kind: "hidden" };
const ENABLED: ClearConversationGate = { kind: "enabled" };

/**
 * The inventory row's successor capability (`fork` + `reasons.fork`), read
 * the way the daemon means it — never re-derived from state client-side.
 */
export interface ClearGateRow {
  readonly canFork?: boolean;
  readonly forkReason?: string;
}

/**
 * Decides the Clear conversation affordance from the daemon's row.
 *
 * Clear is fork-shaped (a successor), so the row's `fork` capability is the
 * honest gate — with two deliberate departures, because ClearSession, unlike
 * fork, CANCELS the source itself and waits for it to settle
 * (internal/adapter/server/placement_successor.go): a chat parked on an
 * approval (`awaiting_approval`) is exactly what /clear is for, and a run
 * THIS tab is driving (`active_elsewhere` while `streamingHere`) is one the
 * daemon will stop on our behalf. A run the row reports active that this
 * tab is NOT driving stays disabled — the daemon cannot promise to reach a
 * run leased by another process, and the operator should stop it where it
 * runs. A non-chat kind hides the item; a row with no verdict at all (an
 * older daemon, or a just-minted chat whose row has not landed) disables it
 * with a reason rather than guessing.
 */
export function clearConversationGate(
  row: ClearGateRow | undefined,
  streamingHere: boolean,
): ClearConversationGate {
  if (!row) return HIDDEN;
  if (row.canFork === true) return ENABLED;
  const reason = row.forkReason ?? "";
  switch (reason) {
    case "awaiting_approval":
      return ENABLED;
    case "active_elsewhere":
      return streamingHere
        ? ENABLED
        : { kind: "disabled", reason: CLEAR_ACTIVE_ELSEWHERE };
    case "inspect_only_kind":
      return HIDDEN;
    case "":
      return { kind: "disabled", reason: CLEAR_ELIGIBILITY_UNKNOWN };
    default:
      // A reason Studio does not know yet: show it verbatim rather than
      // hide the item or pretend the chat is clearable.
      return { kind: "disabled", reason };
  }
}
