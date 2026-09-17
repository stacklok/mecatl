import { clampTitle } from "@/lib/document-title";
import type { ChatPhase } from "./chat-phase";

/**
 * The "while you were away" notice — the web analogue of mecatui's resume
 * notice (cmd/mecatui/ui/update.go `resumeNotice`): one explicit line on
 * return saying the engine kept running while the user was gone and naming
 * the phase that was in flight when they left. The terminal half (Ctrl+Z
 * suspends the process) has no browser analogue; a tab is backgrounded by
 * the OS or the browser, so the trigger is the Page Visibility API and the
 * line is a transient toast (`useAwayNotice`).
 *
 * The daemon is authoritative and the transcript catches up live on return
 * (the SSE watch stays attached in the background), so this notice never
 * carries state of its own: it is composed from the phase at LEAVING and
 * the phase at RETURN, both read off the same folds the tab title uses.
 */

/**
 * Absences shorter than this get no notice: a quick alt-tab is not "away",
 * and a toast on every switch back would train the user to ignore it.
 */
export const MIN_AWAY_MS = 20_000;

/** The toast id — de-duplicates rapid hide/show flips into one line. */
export const AWAY_NOTICE_TOAST_ID = "away-notice";

/** How long the line stays: long enough to read, short enough to not nag. */
export const AWAY_NOTICE_DURATION_MS = 8_000;

export interface AwayNoticeInput {
  /** The chat's phase when the tab went hidden. */
  leftPhase: ChatPhase;
  /** The chat's phase on return, read AFTER the daemon state was refreshed. */
  returnedPhase: ChatPhase;
  /** The chat's title as it was when the tab went hidden. */
  chatTitle: string;
  /** How long the tab was hidden, in milliseconds. */
  awayMs: number;
  /** The daemon is reachable on return (post-refresh). */
  connected: boolean;
  /**
   * The daemon was reachable when the tab went hidden. Defaults to true. An
   * absence that started AND ended offline says nothing new — the offline
   * banner has been up the whole time — so it yields no notice.
   */
  leftConnected?: boolean;
  /** Override for the absence cutoff (tests); defaults to MIN_AWAY_MS. */
  minAwayMs?: number;
}

/**
 * Composes the notice, or null when there is nothing worth a line: a short
 * absence, an idle chat that stayed idle, or a daemon that was already
 * offline when the user left. The phase pair decides the wording — every
 * message names the chat and says what happened during the absence, so a
 * returning user does not have to infer it from the sidebar's pulsing dot.
 */
export function composeAwayNotice(input: AwayNoticeInput): string | null {
  const {
    leftPhase,
    returnedPhase,
    chatTitle,
    awayMs,
    connected,
    leftConnected = true,
    minAwayMs = MIN_AWAY_MS,
  } = input;
  if (awayMs < minAwayMs) return null;
  if (!connected) {
    return leftConnected ? "Mecatl went offline while you were away." : null;
  }
  const name = subject(chatTitle);
  // The name at the head of a sentence ("this chat" → "This chat").
  const Name = name.charAt(0).toUpperCase() + name.slice(1);
  switch (leftPhase) {
    case "idle":
      switch (returnedPhase) {
        case "running":
          return `${Name} started working while you were away.`;
        case "awaiting":
          return `${Name} is waiting for your approval.`;
        default:
          return null;
      }
    case "running":
      switch (returnedPhase) {
        case "idle":
          return `While you were away, ${name} kept running and finished.`;
        case "running":
          return `${Name} is still working — it kept running while you were away.`;
        case "awaiting":
          return `While you were away, ${name} kept running and is now waiting for your approval.`;
      }
      break;
    case "awaiting":
      switch (returnedPhase) {
        case "idle":
          return `The approval on ${name} was resolved while you were away.`;
        case "running":
          return `The approval on ${name} was resolved while you were away; it is working again.`;
        case "awaiting":
          return `${Name} is still waiting for your approval.`;
      }
      break;
  }
  return null;
}

/**
 * The chat's name as it reads inside a sentence: the clamped title in curly
 * quotes (the same 40-character clamp as the tab title, so the toast and the
 * tab agree), or "this chat" when the title is blank or control-only.
 */
function subject(chatTitle: string): string {
  const title = clampTitle(chatTitle);
  return title === "" ? "this chat" : `“${title}”`;
}
