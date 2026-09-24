// SPDX-License-Identifier: Apache-2.0

import type { ChatMessage } from "./chat-state";

interface InventoryRow {
  id: string;
  updatedAt: string;
}

export interface DeliveryCheckFacts {
  connected: boolean;
  idle: boolean;
  next?: InventoryRow;
  previous?: InventoryRow;
  sessionId?: string;
  visible: boolean;
}

/**
 * An inventory change is only a reason to fetch the authoritative transcript.
 * It is not evidence by itself that a scheduled delivery occurred.
 */
export function shouldCheckDeliveryAfterInventory(facts: DeliveryCheckFacts): boolean {
  const { connected, idle, next, previous, sessionId, visible } = facts;
  if (
    !connected ||
    !visible ||
    !idle ||
    !sessionId ||
    !previous ||
    !next ||
    previous.id !== sessionId ||
    next.id !== sessionId
  ) {
    return false;
  }
  const before = Date.parse(previous.updatedAt);
  const after = Date.parse(next.updatedAt);
  return Number.isFinite(before) && Number.isFinite(after) && after > before;
}

export interface RecordedDeliveryMessage extends ChatMessage {
  delivery?: {
    fireId: string;
    kind: "started" | "completed";
    scheduleName?: string;
    stop?: string;
  };
}

function deliveryKey(message: RecordedDeliveryMessage): string | undefined {
  const note = message.delivery;
  return note ? JSON.stringify([note.fireId, note.kind]) : undefined;
}

/**
 * Add only notes proved by the saved transcript. Keep the existing row for an
 * already-seen delivery: it may carry richer live content, reasoning, or tools.
 */
export function mergeRecordedDeliveryMessages<T extends RecordedDeliveryMessage>(
  current: T[],
  authoritative: readonly T[],
): T[] {
  const seen = new Set(current.map(deliveryKey).filter((key): key is string => key !== undefined));
  let merged: T[] | undefined;
  for (const message of authoritative) {
    const key = deliveryKey(message);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    if (!merged) merged = [...current];
    merged.push(message);
  }
  return merged ?? current;
}
