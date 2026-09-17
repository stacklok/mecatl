"use client";

import { useEffect, useRef } from "react";
import type { ChatPhase } from "@/features/agent/chat-phase";
import type { RuntimeConnectionState } from "@/features/agent/runtime-status";

/**
 * The browser tab title — the web analogue of mecatui's terminal window title
 * (cmd/mecatui/ui/wintitle.go). Same shape, same reasons:
 *
 *   <title> — Working · Mecatl Studio      (a turn is running)
 *   <title> — ⚠ Approval · Mecatl Studio   (the run is parked on the user)
 *   <title> — Connecting · Mecatl Studio   (daemon not reached yet)
 *   <title> — Offline · Mecatl Studio      (daemon unreachable)
 *   <title> — Mecatl Studio                (idle)
 *   Working · Mecatl Studio                (no title yet)
 *   Mecatl Studio                          (nothing to say)
 *
 * The title LEADS because tab bars truncate from the right: the chat's
 * identity is the most useful thing to survive a narrow tab, the status word
 * trails, and the app name is last. The status is a STATIC WORD that changes
 * only on a phase transition — never a spinner or a per-token counter, since
 * per-frame title churn trips OS attention heuristics (the dock bounces, the
 * taskbar flashes on every title change). The connection state wins over the
 * phase: a stale phase means nothing when the daemon cannot be reached.
 */

/** The static suffix; matches the root metadata default in app/layout.tsx. */
export const APP_TITLE = "Mecatl Studio";

/**
 * The cap on the title SEGMENT, in code points. 40 is tab-sized: enough for a
 * real prompt's first clause, short enough that the status word and the app
 * name survive a right-truncated tab bar (mirrors mecatui's windowTitleRunes).
 */
export const TITLE_MAX_CHARS = 40;

/**
 * Sanitizes a chat title for a one-line tab title: strips C0 control
 * characters and DEL (an escape or bell in a title is never legitimate),
 * collapses any whitespace run — newlines and tabs included — to a single
 * space, trims, and clamps to TITLE_MAX_CHARS with a trailing ellipsis. Code
 * point aware, so an emoji or CJK title is never cut mid-character. A blank
 * or control-only title yields "" (the caller falls back to the app name).
 */
export function clampTitle(raw: string): string {
  const cleaned = Array.from(raw)
    .filter((char) => {
      const code = char.codePointAt(0) ?? 0;
      // Whitespace controls (\t \n \v \f \r) survive here so the collapse
      // below turns them into a word gap instead of gluing words together.
      return (code >= 0x20 && code !== 0x7f) || /\s/.test(char);
    })
    .join("")
    .replace(/\s+/g, " ")
    .trim();
  if (cleaned === "") return "";
  const chars = Array.from(cleaned);
  if (chars.length <= TITLE_MAX_CHARS) return cleaned;
  return `${chars.slice(0, TITLE_MAX_CHARS - 1).join("")}…`;
}

export interface DocumentTitleInput {
  /** The open chat's title (or the draft's "New chat"); "" for none. */
  chatTitle: string;
  phase: ChatPhase;
  connection: RuntimeConnectionState;
}

/** The status word a phase/connection pair contributes; "" for idle. */
function statusWord(
  phase: ChatPhase,
  connection: RuntimeConnectionState,
): string {
  if (connection === "offline") return "Offline";
  if (connection === "connecting") return "Connecting";
  switch (phase) {
    case "running":
      return "Working";
    case "awaiting":
      return "⚠ Approval";
    default:
      return "";
  }
}

/** Pure: composes the tab title for a chat title, phase and connection. */
export function composeDocumentTitle({
  chatTitle,
  phase,
  connection,
}: DocumentTitleInput): string {
  const title = clampTitle(chatTitle);
  const word = statusWord(phase, connection);
  if (title === "" && word === "") return APP_TITLE;
  if (title === "") return `${word} · ${APP_TITLE}`;
  if (word === "") return `${title} — ${APP_TITLE}`;
  return `${title} — ${word} · ${APP_TITLE}`;
}

/**
 * Projects `title` onto `document.title` while mounted, writing only when the
 * value actually changes (the static-word discipline above holds end to end).
 *
 * Coexists with the App Router's hoisted route `<title>` metadata: React only
 * touches that node when a route's metadata changes, and this effect runs
 * after the commit, so the dynamic title lands on top. On unmount the tab is
 * handed back to the static metadata — but ONLY if it still shows what this
 * hook last wrote. Leaving for a route with its own title (the shortcuts
 * page) has React apply that title in the same commit, before this cleanup
 * runs; restoring blindly would clobber it.
 */
export function useDocumentTitle(title: string): void {
  const lastWritten = useRef<string | null>(null);

  useEffect(() => {
    const previous = document.title;
    return () => {
      if (
        lastWritten.current !== null &&
        document.title === lastWritten.current
      ) {
        document.title = previous;
      }
      lastWritten.current = null;
    };
  }, []);

  useEffect(() => {
    if (document.title !== title) document.title = title;
    lastWritten.current = title;
  }, [title]);
}
