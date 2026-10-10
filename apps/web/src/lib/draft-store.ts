// SPDX-License-Identifier: Apache-2.0

import { readUserScopedItem, writeUserScopedItem } from "./account-storage";

/**
 * Browser-local persistence for an unsent composer draft, so a reload or a
 * switch to another chat and back never loses what was typed. Ported from the
 * prototype (`stack-08`), on Studio's account-scoped storage.
 *
 * - **Session storage:** the draft survives a reload and in-app routing but
 *   ends with the tab. A draft is unsent text, not a preference.
 * - **One key per chat:** a chat's session ID, or `new` for the draft view
 *   whose session is created on the first send. Switching chats never carries
 *   a draft across.
 * - **Account scoped:** keys live under `studio.`, so a sign-out or an account
 *   change clears them before the next person's chat renders
 *   (`reconcileAccount`). One account's draft never reaches another.
 *
 * The draft is whatever was typed or pasted and may hold a secret. It never
 * leaves the browser: nothing here sends it to the BFF. When storage is
 * unavailable, `account-storage` keeps it in this page's memory instead.
 */

type Store = Pick<Storage, "getItem" | "key" | "length" | "removeItem" | "setItem">;

const draftKeyPrefix = "studio.chat.draft.";

function browserSessionStore(): Store | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** The storage key for one composer: a chat's session ID, or `new` for the draft view. */
export function draftStorageKey(chatId: string): string {
  return `${draftKeyPrefix}${encodeURIComponent(chatId)}`;
}

/** The stored draft for `chatId`, or "" when there is none. */
export function readDraft(chatId: string, store: Store | null = browserSessionStore()): string {
  return readUserScopedItem(draftStorageKey(chatId), store) ?? "";
}

/** Stores `text` for `chatId`. A blank draft removes the entry, so storage never fills with empty composers. */
export function writeDraft(
  chatId: string,
  text: string,
  store: Store | null = browserSessionStore(),
): void {
  writeUserScopedItem(draftStorageKey(chatId), text.trim() === "" ? null : text, store);
}

/** Removes the stored draft for `chatId`, after a send, queue, steer, or explicit clear. */
export function clearDraft(chatId: string, store: Store | null = browserSessionStore()): void {
  writeUserScopedItem(draftStorageKey(chatId), null, store);
}
