// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";

export interface QueuedMessage {
  createdAt: number;
  id: string;
  text: string;
}

const queueKeyPrefix = "studio.chat.queue.";
const queueChangedEvent = "studio:chat-queue-changed";

export function parseQueuedMessages(raw: string | null): QueuedMessage[] {
  if (!raw) return [];
  try {
    const value: unknown = JSON.parse(raw);
    if (!Array.isArray(value)) return [];
    return value.flatMap((entry) => {
      if (!entry || typeof entry !== "object") return [];
      const candidate = entry as Partial<QueuedMessage>;
      if (typeof candidate.id !== "string" || typeof candidate.text !== "string") return [];
      const text = candidate.text.trim();
      if (!candidate.id || !text) return [];
      return [
        {
          createdAt: typeof candidate.createdAt === "number" ? candidate.createdAt : 0,
          id: candidate.id,
          text,
        },
      ];
    });
  } catch {
    return [];
  }
}

export function useQueuedMessages(sessionId: string) {
  const [items, setItems] = useState<QueuedMessage[]>(() => readQueue(sessionId));

  useEffect(() => {
    const refresh = () => setItems(readQueue(sessionId));
    refresh();
    const onQueueChanged = (event: Event) => {
      if ((event as CustomEvent<string>).detail === sessionId) refresh();
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === storageKey(sessionId)) refresh();
    };
    window.addEventListener(queueChangedEvent, onQueueChanged);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(queueChangedEvent, onQueueChanged);
      window.removeEventListener("storage", onStorage);
    };
  }, [sessionId]);

  const add = useCallback(
    (text: string) => {
      if (!sessionId || !text.trim()) return;
      writeQueue(sessionId, [
        ...readQueue(sessionId),
        { createdAt: Date.now(), id: crypto.randomUUID(), text: text.trim() },
      ]);
    },
    [sessionId],
  );

  const remove = useCallback(
    (id: string) =>
      writeQueue(
        sessionId,
        readQueue(sessionId).filter((item) => item.id !== id),
      ),
    [sessionId],
  );

  const update = useCallback(
    (id: string, text: string) => {
      const trimmed = text.trim();
      if (!trimmed) return;
      writeQueue(
        sessionId,
        readQueue(sessionId).map((item) => (item.id === id ? { ...item, text: trimmed } : item)),
      );
    },
    [sessionId],
  );

  return { add, items, remove, update };
}

export function takeNextQueuedMessage(sessionId: string) {
  const [next, ...remaining] = readQueue(sessionId);
  if (next) writeQueue(sessionId, remaining);
  return next;
}

function storageKey(sessionId: string) {
  return `${queueKeyPrefix}${encodeURIComponent(sessionId)}`;
}

function readQueue(sessionId: string) {
  if (!sessionId) return [];
  try {
    return parseQueuedMessages(window.localStorage.getItem(storageKey(sessionId)));
  } catch {
    return [];
  }
}

function writeQueue(sessionId: string, items: QueuedMessage[]) {
  if (!sessionId) return;
  try {
    if (items.length > 0) window.localStorage.setItem(storageKey(sessionId), JSON.stringify(items));
    else window.localStorage.removeItem(storageKey(sessionId));
  } catch {
    // Keep running even when browser storage is disabled or full.
  }
  window.dispatchEvent(new CustomEvent(queueChangedEvent, { detail: sessionId }));
}
