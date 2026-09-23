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
let peerQuarantined = false;
let accountStore: Store | undefined;

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

type AccountAccess = "verified" | "memory" | "blocked";

function markerAccess(store: Pick<Storage, "getItem"> | null | undefined): AccountAccess {
  if (store == null) return "memory";
  try {
    const marker = store.getItem(accountStorageKey);
    if (marker === volatileAccount) return "verified";
    // A failed cleanup can still hold this page's drafts in memory. An absent
    // marker or another account's marker cannot authorize access to the store.
    return marker === cleanupPending ? "memory" : "blocked";
  } catch {
    return "memory";
  }
}

/** Recheck the shared marker at the storage seam; storage has no cross-tab CAS. */
function accountAccess(store: Pick<Storage, "getItem"> | null | undefined): AccountAccess {
  if (peerQuarantined) return "blocked";
  if (volatileAccount === undefined) return "verified";
  // A failed cleanup has already detached this page from both physical stores.
  if (quarantined) return "memory";
  const shared = accountStore ?? browserStorage();
  const sharedAccess = markerAccess(shared);
  if (sharedAccess !== "verified") return sharedAccess;
  return store === shared ? "verified" : markerAccess(store);
}

export function clearUserScopedStorage(
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
) {
  const localCleared = clearStore(store);
  const sessionCleared = sessionStore === store ? localCleared : clearStore(sessionStore);
  resetVolatileState();
  accountStore = undefined;
  peerQuarantined = false;
  quarantined = !localCleared || !sessionCleared;
  if (quarantined) markCleanupPending(store, sessionStore);
}

/** A peer owns the shared marker now; discard only this tab's data before auth is rechecked. */
export function quarantinePeerAccount(
  store: Store | undefined = browserStorage(),
  sessionStore: Store | undefined = browserSessionStorage(),
): void {
  resetVolatileState();
  accountStore = store;
  peerQuarantined = true;
  quarantined = false;
  if (sessionStore !== store && !clearStore(sessionStore)) {
    markCleanupPending(undefined, sessionStore);
  }
}

/** An unreadable marker uses memory fallback; a known other owner cannot be adopted. */
export function sharedMarkerConflicts(
  account: string,
  store: Store | undefined = browserStorage(),
): boolean {
  const marker = readableAccount(store);
  return (
    marker.ok &&
    marker.value !== null &&
    marker.value !== cleanupPending &&
    marker.value !== account
  );
}

export function sharedMarkerIsAbsent(store: Store | undefined = browserStorage()): boolean {
  const marker = readableAccount(store);
  return marker.ok && marker.value === null;
}

/** Keeps account data usable during a storage exception for this open page. */
export function readUserScopedItem(
  key: string,
  store: Pick<Storage, "getItem"> | null | undefined = browserStorage(),
): string | null {
  if (!key.startsWith(userScopedPrefix)) return null;
  const access = accountAccess(store);
  if (access === "blocked") return null;
  if (access === "memory" || quarantined || volatileWrites.has(key)) {
    return volatileItems.get(key) ?? null;
  }
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
  store: Pick<Storage, "getItem" | "key" | "length"> | null | undefined = browserStorage(),
): string[] {
  if (!prefix.startsWith(userScopedPrefix)) return [];
  const access = accountAccess(store);
  if (access === "blocked") return [];
  const memoryKeys = [...volatileItems.keys()].filter((key) => key.startsWith(prefix));
  if (access === "memory" || quarantined) return memoryKeys;
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
  const access = accountAccess(store);
  if (access === "blocked") return;
  if (value === null) volatileItems.delete(key);
  else volatileItems.set(key, value);
  if (access === "memory" || quarantined) {
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
  accountStore = store;
  if (quarantined && volatileAccount === account && !peerQuarantined) return false;

  const local = readableAccount(store);
  const session = sessionStore === store ? local : readableAccount(sessionStore);
  const changedInPage = volatileAccount !== undefined && volatileAccount !== account;
  const needsStoreClear = (marker: typeof local, candidate: Store | undefined): boolean => {
    if (!marker.ok) return true;
    if (marker.value !== null) return marker.value !== account;
    // An absent marker cannot establish ownership of existing payloads.
    const keys = scopedKeys(candidate);
    return keys === undefined || keys.some((key) => key !== accountStorageKey);
  };
  const clearLocal = needsStoreClear(local, store);
  const clearSession = sessionStore === store ? clearLocal : needsStoreClear(session, sessionStore);
  const needsClear = changedInPage || clearLocal || clearSession;

  if (needsClear) {
    // The peer may already have marked and written the new account's shared
    // data. Identity change still evicts old memory, without removing it.
    const localCleared = !clearLocal || (local.ok && clearStore(store));
    const sessionCleared =
      sessionStore === store
        ? localCleared
        : !clearSession || (session.ok && clearStore(sessionStore));
    resetVolatileState();
    if (!localCleared || !sessionCleared) {
      peerQuarantined = false;
      quarantined = true;
      volatileAccount = account;
      markCleanupPending(
        localCleared || !local.ok ? undefined : store,
        (sessionCleared && !clearSession) || !session.ok ? undefined : sessionStore,
      );
      return true;
    }
  }

  const localRecorded = persistAccount(store, account);
  const sessionRecorded =
    sessionStore === store ? localRecorded : persistAccount(sessionStore, account);
  peerQuarantined = false;
  quarantined = !localRecorded || !sessionRecorded;
  if (quarantined) {
    markCleanupPending(
      localRecorded ? undefined : store,
      sessionRecorded ? undefined : sessionStore,
    );
  }
  volatileAccount = account;
  return needsClear;
}
