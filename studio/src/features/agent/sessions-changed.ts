/**
 * A typed, page-local signal that the daemon's session inventory changed
 * behind the sidebar's back — a bulk clean-up job deleted rows, say — so
 * `useAgentSessions` re-walks the inventory now instead of on its next
 * 20-second poll. Carried on `window` because the producer (the Storage
 * settings page) and the consumer (the sidebar's sessions hook) share no
 * React ancestor that owns session state; the helpers keep the event name
 * and its (empty) payload in ONE place so neither side spells a string.
 */

export const SESSIONS_CHANGED_EVENT = "studio:sessions-changed";

/** Announces that stored sessions were added or removed outside the hook. */
export function notifySessionsChanged(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(SESSIONS_CHANGED_EVENT));
}

/** Subscribes to the signal; returns the unsubscribe for an effect cleanup. */
export function onSessionsChanged(listener: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  window.addEventListener(SESSIONS_CHANGED_EVENT, listener);
  return () => window.removeEventListener(SESSIONS_CHANGED_EVENT, listener);
}
