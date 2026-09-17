/**
 * Live session-title adoption — the web twin of mecatui's `onSessionTitle`
 * reducer (cmd/mecatui/ui/title_ux.go). The daemon publishes a
 * `session.title` event after every durable title lifecycle change: the
 * first-prompt seed, an auto-generated title, a rename from any client. The
 * event is AUTHORITATIVE (the daemon already applied its own provenance
 * rules), so the inventory row adopts its word instead of waiting for the
 * next 20-second poll.
 *
 * The one guard is ORDER: a watch replays the durable log from the start, so
 * an older title can arrive after a newer one. The title lifecycle revision
 * (`title_metadata.revision` on the row, `revision` on the event) settles it
 * — a row only ever moves forward. Pure: the sessions hook wraps this in its
 * setState.
 */

import type { AgentSession } from "./types";

/** One `session.title` event as the stream translator delivers it. */
export interface SessionTitleUpdate {
  title: string;
  /** "operator" | "first-prompt" | "" — the daemon's word, verbatim. */
  provenance: string;
  /** The title lifecycle revision; null when the event carried none. */
  revision: number | null;
}

/**
 * Whether an event at `next` may replace a row at `current`. Unknown on
 * either side (null — an older daemon, a legacy row) adopts: there is
 * nothing to order by, and the event is the daemon's latest word. Known on
 * both sides adopts only a STRICTLY newer revision — an equal revision is a
 * re-delivery of what the row already shows (or of a rename this tab
 * applied optimistically and is awaiting the echo for), never a change.
 */
export function shouldAdoptTitle(
  current: number | null | undefined,
  next: number | null,
): boolean {
  if (next === null) return true;
  if (current === null || current === undefined) return true;
  return next > current;
}

/**
 * Applies one title event to the row it names. Returns the SAME array when
 * nothing changed (an unknown id, a stale revision, a byte-identical
 * re-delivery), so a setState caller commits no render for it.
 *
 * An event with an empty title (generation still pending) advances the
 * revision but never blanks the row: the header keeps its current label
 * until the daemon has a real one.
 */
export function applySessionTitle(
  sessions: AgentSession[],
  id: string,
  update: SessionTitleUpdate,
): AgentSession[] {
  let changed = false;
  const next = sessions.map((session) => {
    if (session.id !== id) return session;
    if (!shouldAdoptTitle(session.titleRevision, update.revision)) {
      return session;
    }
    const title = update.title || session.title;
    // The provenance travels with the title it describes: an empty title
    // keeps the row's own label AND its provenance.
    const titleProvenance = update.title
      ? update.provenance
      : session.titleProvenance;
    const titleRevision = update.revision ?? session.titleRevision;
    if (
      title === session.title &&
      titleProvenance === session.titleProvenance &&
      titleRevision === session.titleRevision
    ) {
      return session;
    }
    changed = true;
    return { ...session, title, titleProvenance, titleRevision };
  });
  return changed ? next : sessions;
}
