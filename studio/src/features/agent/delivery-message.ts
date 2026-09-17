/**
 * The message-model side of scheduled-task delivery notes: turning one
 * recorded user-role text into an `AgentMessage` (a tagged delivery card when
 * the text is a note, a plain user message otherwise) and the fire-keyed
 * dedup the transcript rebuild, the watch reducer, and the live path share.
 *
 * Pure helpers — no React, no daemon calls — so every consumer folds the same
 * way and the tests pin one behaviour.
 */

import {
  type DeliveryNoteInfo,
  parseDeliveryNote,
} from "@/lib/protocol/delivery-note";
import type { SessionTranscript } from "@/lib/protocol/sessions";
import type { AgentMessage } from "./types";

/**
 * The dedup key: the daemon documents a bounded double-record of one fire's
 * note (replay + live, or the durable log's own repeat), and a fire's start
 * note and terminal note are DISTINCT entries that both render.
 */
export function deliveryKey(info: DeliveryNoteInfo): string {
  return `${info.kind}:${info.fireId}`;
}

/** Whether `messages` already carries the note `info` describes. */
export function hasDeliveryNote(
  messages: readonly AgentMessage[],
  info: DeliveryNoteInfo,
): boolean {
  return messages.some(
    (message) =>
      message.delivery !== undefined &&
      message.delivery.fireId === info.fireId &&
      message.delivery.kind === info.kind,
  );
}

/**
 * Builds the user-role message for one recorded text. A delivery note yields
 * `{ role: "user", delivery, content: <stripped body> }` — the fence and the
 * provenance header are not kept, the card carries the attribution. Anything
 * else is the plain user message it always was.
 */
export function deliveryMessage(
  id: string,
  text: string,
  timestamp: number,
): AgentMessage {
  const note = parseDeliveryNote(text);
  if (!note) return { id, role: "user", content: text, timestamp };
  const { body, ...delivery } = note;
  return { id, role: "user", content: body, timestamp, delivery };
}

/**
 * Whether the daemon's transcript carries a delivery note the local message
 * list has not rendered yet — the trigger for re-reading the transcript when
 * a fire completed into an idle chat between two inventory polls.
 */
export function transcriptHasUnseenDelivery(
  transcript: Pick<SessionTranscript, "messages">,
  messages: readonly AgentMessage[],
): boolean {
  for (const entry of transcript.messages) {
    if (entry.role !== "user") continue;
    const note = parseDeliveryNote(entry.text);
    if (note && !hasDeliveryNote(messages, note)) return true;
  }
  return false;
}
