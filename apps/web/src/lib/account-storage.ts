// SPDX-License-Identifier: Apache-2.0

/**
 * Everything Studio keeps in `localStorage` for a user — chat folders, queued
 * prompts, canvases, profile names and avatars — lives under this prefix. It
 * belongs to one account: a sign-out or a different account clears it, so the
 * next person on this browser never sees the previous one's data. The theme
 * is a device preference stored under its own key and survives.
 */
export const userScopedPrefix = "studio.";
const accountKey = `${userScopedPrefix}account`;

type Store = Pick<Storage, "getItem" | "key" | "length" | "removeItem" | "setItem">;

function browserStorage(): Store | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

export function clearUserScopedStorage(store: Store | undefined = browserStorage()) {
  if (store === undefined) return;
  const doomed: string[] = [];
  for (let index = 0; index < store.length; index += 1) {
    const key = store.key(index);
    if (key?.startsWith(userScopedPrefix)) doomed.push(key);
  }
  for (const key of doomed) store.removeItem(key);
}

/**
 * Records the signed-in account and, when it differs from the one this
 * browser last saw, clears the previous account's data first. Returns whether
 * anything was cleared. Idempotent, so it is safe to call on every render.
 */
export function reconcileAccount(
  account: string | undefined,
  store: Store | undefined = browserStorage(),
): boolean {
  if (store === undefined || account === undefined || account === "") return false;
  const previous = store.getItem(accountKey);
  if (previous === account) return false;
  const cleared = previous !== null;
  if (cleared) clearUserScopedStorage(store);
  store.setItem(accountKey, account);
  return cleared;
}
