// SPDX-License-Identifier: Apache-2.0

/**
 * Everything Studio keeps in browser storage for a user — chat folders,
 * queued prompts, failed runs, canvases, profile names and avatars — lives
 * under this prefix. A sign-out or account change clears it from both storage
 * areas before the next person's chat renders. Theme and palette are device
 * preferences stored under their own keys and survive.
 */
export const userScopedPrefix = "studio.";
export const accountStorageKey = `${userScopedPrefix}account`;
// Account keys are base64url. This value cannot be mistaken for one on reload.
const cleanupPending = "!cleanup-pending";
const volatileItems = new Map<string, string>();
const volatileWrites = new Set<string>();
let volatileAccount: string | undefined;
let quarantined = false;

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

function readableAccount(store: Store | undefined): { ok: boolean; value: string | null } {
  if (store === undefined) return { ok: typeof window === "undefined", value: null };
  try {
    return { ok: true, value: store.getItem(accountStorageKey) };
  } catch {
    return { ok: false, value: null };
  }
}

function scopedKeys(store: Pick<Storage, "key" | "length"> | undefined): string[] | undefined {
  if (store === undefined) return typeof window === "undefined" ? [] : undefined;
  try {
    const keys: string[] = [];
    const length = store.length;
    for (let index = 0; index < length; index += 1) {
      const key = store.key(index);
      if (key === null) return undefined;
      if (key.startsWith(userScopedPrefix)) keys.push(key);
    }
    return keys;
  } catch {
    return undefined;
  }
}

/** Remove payloads first, leaving the old account marker until they are gone. */
function clearStore(store: Store | undefined): boolean {
  const keys = scopedKeys(store);
  if (keys === undefined) return false;
  if (store === undefined) return true;

  for (const key of keys) {
    if (key === accountStorageKey) continue;
    try {
      store.removeItem(key);
    } catch {
      // Verification below decides whether the payload remains readable.
    }
  }
  const remaining = scopedKeys(store);
  if (remaining === undefined || remaining.some((key) => key !== accountStorageKey)) return false;
  if (!remaining.includes(accountStorageKey)) return true;
  try {
    store.removeItem(accountStorageKey);
  } catch {
    return false;
  }
  return scopedKeys(store)?.length === 0;
}

function markCleanupPending(store: Store | undefined, sessionStore: Store | undefined): void {
  for (const candidate of new Set([store, sessionStore])) {
    try {
      candidate?.setItem(accountStorageKey, cleanupPending);
    } catch {
      // Another store or an absent marker can still make the next load retry.
    }
  }
}

function resetVolatileState(): void {
  volatileItems.clear();
  volatileWrites.clear();
  volatileAccount = undefined;
}

export function clearUserScopedStorage(
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
) {
  const localCleared = clearStore(store);
  const sessionCleared = sessionStore === store ? localCleared : clearStore(sessionStore);
  resetVolatileState();
  quarantined = !localCleared || !sessionCleared;
  if (quarantined) markCleanupPending(store, sessionStore);
}

/** Keeps account data usable during a storage exception for this open page. */
export function readUserScopedItem(
  key: string,
  store: Pick<Storage, "getItem"> | null | undefined = browserStorage(),
): string | null {
  if (!key.startsWith(userScopedPrefix)) return null;
  if (quarantined || volatileWrites.has(key)) return volatileItems.get(key) ?? null;
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

/** Enumerate only keys belonging to the current in-memory account view. */
export function listUserScopedKeys(
  prefix: string,
  store: Pick<Storage, "key" | "length"> | null | undefined = browserStorage(),
): string[] {
  if (!prefix.startsWith(userScopedPrefix)) return [];
  const memoryKeys = [...volatileItems.keys()].filter((key) => key.startsWith(prefix));
  if (quarantined) return memoryKeys;
  const keys = scopedKeys(store ?? undefined);
  if (keys === undefined) return memoryKeys;
  const pendingWrites = [...volatileWrites].filter(
    (key) => key.startsWith(prefix) && volatileItems.has(key),
  );
  return [...new Set([...keys.filter((key) => key.startsWith(prefix)), ...pendingWrites])];
}

export function writeUserScopedItem(
  key: string,
  value: string | null,
  store: Store | null | undefined = browserStorage(),
): void {
  if (!key.startsWith(userScopedPrefix)) return;
  if (value === null) volatileItems.delete(key);
  else volatileItems.set(key, value);
  if (quarantined) {
    volatileWrites.add(key);
    return;
  }
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

function persistAccount(store: Store | undefined, account: string): boolean {
  if (store === undefined) return typeof window === "undefined";
  try {
    if (store.getItem(accountStorageKey) !== account) store.setItem(accountStorageKey, account);
    return store.getItem(accountStorageKey) === account;
  } catch {
    return false;
  }
}

/**
 * Records the signed-in account and clears data whose ownership is unknown or
 * differs from it before that data can be read. Returns whether the caller
 * must evict account-scoped UI state. Repeated renders keep memory-only drafts
 * when storage cannot be cleared; a reload retries the pending cleanup.
 */
export function reconcileAccount(
  account: string | undefined,
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
): boolean {
  if (account === undefined || account === "") return false;
  if (quarantined && volatileAccount === account) return false;

  const local = readableAccount(store);
  const session = sessionStore === store ? local : readableAccount(sessionStore);
  const changedInPage = volatileAccount !== undefined && volatileAccount !== account;
  let needsClear =
    changedInPage ||
    !local.ok ||
    !session.ok ||
    (local.value !== null && local.value !== account) ||
    (session.value !== null && session.value !== account);

  // An absent marker does not prove that existing payloads belong to this
  // account: a prior marker write or partial sign-out may have failed.
  if (!needsClear) {
    for (const [marker, candidate] of [
      [local, store],
      [session, sessionStore],
    ] as const) {
      if (marker.value !== null) continue;
      const keys = scopedKeys(candidate);
      if (keys === undefined || keys.some((key) => key !== accountStorageKey)) {
        needsClear = true;
        break;
      }
    }
  }

  if (needsClear) {
    const localCleared = clearStore(store);
    const sessionCleared = sessionStore === store ? localCleared : clearStore(sessionStore);
    resetVolatileState();
    if (!localCleared || !sessionCleared) {
      quarantined = true;
      volatileAccount = account;
      markCleanupPending(store, sessionStore);
      return true;
    }
  }

  const localRecorded = persistAccount(store, account);
  const sessionRecorded =
    sessionStore === store ? localRecorded : persistAccount(sessionStore, account);
  quarantined = !localRecorded || !sessionRecorded;
  if (quarantined) markCleanupPending(store, sessionStore);
  volatileAccount = account;
  return needsClear;
}
