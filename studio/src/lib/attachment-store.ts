"use client";

import type { Attachment } from "@/features/agent/types";

/**
 * Browser-side persistence for sent-attachment bytes. The daemon's transcript
 * never echoes attachment content, so without this a refresh strips every
 * chip's thumbnail and preview. IndexedDB (not localStorage) because the
 * values are data: URLs of real images.
 *
 * Shape: one record per session — the ordered list of {content, attachments}
 * the visit sent, exactly what the rehydrate merge consumes. Bounded to the
 * most recent SENDS_KEPT sends per session so the store can't grow without
 * limit; a failed/unsupported IndexedDB degrades to session-only memory.
 */
export interface SentAttachmentRecord {
  /** The text as the daemon recorded it (text attachments inlined), which
   *  is what the rehydrated transcript's user turn is matched against. */
  content: string;
  attachments: Attachment[];
  /** The text the user actually typed, restored onto the matched turn so a
   *  bubble shows the prompt plus chips rather than the inlined blocks.
   *  Absent on records written before text attachments existed. */
  display?: string;
}

const DB_NAME = "mecatl-studio";
const STORE = "sent-attachments";
const SENDS_KEPT = 20;

function openDb(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === "undefined") return Promise.resolve(null);
  return new Promise((resolve) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains(STORE)) {
        request.result.createObjectStore(STORE);
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => resolve(null);
    request.onblocked = () => resolve(null);
  });
}

export async function loadSentAttachments(
  sessionId: string,
): Promise<SentAttachmentRecord[]> {
  const db = await openDb();
  if (!db) return [];
  return new Promise((resolve) => {
    try {
      const request = db
        .transaction(STORE, "readonly")
        .objectStore(STORE)
        .get(sessionId);
      request.onsuccess = () =>
        resolve(
          Array.isArray(request.result)
            ? (request.result as SentAttachmentRecord[])
            : [],
        );
      request.onerror = () => resolve([]);
    } catch {
      resolve([]);
    }
  });
}

export async function saveSentAttachments(
  sessionId: string,
  records: SentAttachmentRecord[],
): Promise<void> {
  const db = await openDb();
  if (!db) return;
  await new Promise<void>((resolve) => {
    try {
      const transaction = db.transaction(STORE, "readwrite");
      transaction.objectStore(STORE).put(records.slice(-SENDS_KEPT), sessionId);
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => resolve();
      transaction.onabort = () => resolve();
    } catch {
      resolve();
    }
  });
}
