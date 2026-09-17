/**
 * Browser-local persistence for an UNSENT composer draft, so a reload or an
 * in-app navigation never loses what was typed (the TUI's draft survives
 * because the process does; a browser tab has no such luck).
 *
 * sessionStorage, deliberately: it survives a reload and in-app routing but
 * dies with the tab, which is the right lifetime for text the user has not
 * sent yet — a draft is not a preference. Keys are per session
 * (`draftStorageKey`), so switching chats does not cross drafts over.
 *
 * Privacy note (studio/CLAUDE.md, the spirit of rule 8): the draft is
 * whatever was typed or pasted into the composer and may hold a secret. It is
 * same-origin readable by any script on the origin, exactly like Studio's
 * other browser-local state, and it never leaves the browser — nothing here
 * syncs a draft to the daemon or into turn-0 context, and this module must
 * not become the precedent for doing so. Every access is try/catch-guarded
 * (storage disabled, full, or the SSR pass) and degrades to "no draft".
 */

const DRAFT_KEY_PREFIX = "mecatl-studio.draft.";

/** The storage key for one composer: a session id, or `"new"` for the draft
 *  view whose daemon session is minted on the first send. */
export function draftStorageKey(sessionId: string): string {
  return `${DRAFT_KEY_PREFIX}${sessionId}`;
}

function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage ?? null;
  } catch {
    return null;
  }
}

/** The stored draft for `key`, or "" when there is none (or storage is
 *  unavailable). */
export function readDraft(key: string): string {
  try {
    return storage()?.getItem(key) ?? "";
  } catch {
    return "";
  }
}

/** Stores `text` under `key`; a blank draft removes the entry instead, so
 *  storage never fills with empty composers. */
export function writeDraft(key: string, text: string): void {
  if (text.trim() === "") {
    clearDraft(key);
    return;
  }
  try {
    storage()?.setItem(key, text);
  } catch {
    // Storage disabled or full — the draft just does not persist.
  }
}

/** Removes the stored draft for `key` (after a send, a queue, a steer, or an
 *  explicit clear). */
export function clearDraft(key: string): void {
  try {
    storage()?.removeItem(key);
  } catch {
    // Nothing to remove, or storage is unavailable.
  }
}
