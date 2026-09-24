// SPDX-License-Identifier: Apache-2.0

/**
 * Everything Studio keeps in browser storage for a user — chat folders,
 * queued prompts, failed runs, canvases, profile names and avatars — lives
 * under this prefix. A sign-out or account change clears it from both storage
 * areas before the next person's chat renders. The theme is a device
 * preference stored under its own key and survives.
 */
export const userScopedPrefix = "studio.";
export const accountStorageKey = `${userScopedPrefix}account`;
const volatileItems = new Map<string, string>();
const volatileWrites = new Set<string>();
let volatileAccount: string | undefined;

type Store = Pick<Storage, "getItem" | "key" | "length" | "removeItem" | "setItem">;

function browserStorage(): Store | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

function browserSessionStorage(): Store | undefined {
  try {
    return window.sessionStorage;
  } catch {
    return undefined;
  }
}

function clearStore(store: Store | undefined) {
  if (store === undefined) return;
  try {
    const doomed: string[] = [];
    for (let index = 0; index < store.length; index += 1) {
      const key = store.key(index);
      if (key?.startsWith(userScopedPrefix)) doomed.push(key);
    }
    for (const key of doomed) store.removeItem(key);
  } catch {
    // Storage may be disabled after the page loaded. Its contents are then
    // inaccessible; callers continue with their in-memory fallback.
  }
}

export function clearUserScopedStorage(
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
) {
  clearStore(store);
  if (sessionStore !== store) clearStore(sessionStore);
  volatileItems.clear();
  volatileWrites.clear();
  volatileAccount = undefined;
}

/** Keeps account data usable during a storage exception for this open page. */
export function readUserScopedItem(
  key: string,
  store: Store | null | undefined = browserStorage(),
): string | null {
  if (!key.startsWith(userScopedPrefix)) return null;
  if (volatileWrites.has(key)) return volatileItems.get(key) ?? null;
  try {
    if (store != null) {
      const value = store.getItem(key);
      if (value === null) volatileItems.delete(key);
      else volatileItems.set(key, value);
      return value;
    }
  } catch {
    // Fall through to the in-memory copy from this page.
  }
  return volatileItems.get(key) ?? null;
}

export function writeUserScopedItem(
  key: string,
  value: string | null,
  store: Store | null | undefined = browserStorage(),
): void {
  if (!key.startsWith(userScopedPrefix)) return;
  if (value === null) volatileItems.delete(key);
  else volatileItems.set(key, value);
  try {
    if (store == null) {
      volatileWrites.add(key);
    } else {
      if (value === null) store.removeItem(key);
      else store.setItem(key, value);
      volatileWrites.delete(key);
    }
  } catch {
    // A quota error may leave readable but stale storage, so prefer this page's copy.
    volatileWrites.add(key);
  }
}

function storedAccount(store: Store | undefined): string | null {
  try {
    return store?.getItem(accountStorageKey) ?? null;
  } catch {
    return null;
  }
}

function setStoredAccount(store: Store | undefined, account: string) {
  try {
    store?.setItem(accountStorageKey, account);
  } catch {
    // The caller's in-memory state remains usable when storage is blocked.
  }
}

/**
 * Records the signed-in account and, when it differs from the one this
 * browser last saw, clears the previous account's data first. Returns whether
 * anything was cleared. Idempotent, so it is safe to call on every render.
 */
export function reconcileAccount(
  account: string | undefined,
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
): boolean {
  if (account === undefined || account === "") return false;
  const previous = storedAccount(store);
  const previousSession = storedAccount(sessionStore);
  const changed =
    (previous !== null && previous !== account) ||
    (previousSession !== null && previousSession !== account) ||
    (volatileAccount !== undefined && volatileAccount !== account);
  if (changed) clearUserScopedStorage(store, sessionStore);
  setStoredAccount(store, account);
  if (sessionStore !== store) setStoredAccount(sessionStore, account);
  volatileAccount = account;
  return changed;
}
