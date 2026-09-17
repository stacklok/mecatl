"use client";

import { useSyncExternalStore } from "react";

/**
 * Folders for chats — the real thing behind the Labs mock's "project
 * folders", STUDIO-OWNED and browser-local. The daemon has no notion of a
 * folder and its session API is untouched: a folder is a name in this
 * browser's localStorage plus a map of daemon session id → folder id that
 * the sidebar reads back when it groups the inventory. Nothing here reaches
 * the daemon or the controller, and a chat is never changed by filing it.
 *
 * One JSON document under one key, validated on every read (malformed
 * entries and assignments to unknown folders are dropped, never trusted),
 * bounded (`MAX_FOLDERS` folders, names trimmed to `FOLDER_NAME_MAX_CHARS`
 * and unique case-insensitively). Every mounted consumer reads the same
 * store through `useSyncExternalStore` (the `useShowToolCalls` pattern), so
 * a move made from one row's menu is seen by the group headers at once, and
 * another tab's change arrives on the `storage` event.
 *
 * The key is session-STATE, not a portable preference: the assignments are
 * keyed by daemon session id, so `preferences-file.ts` excludes it by name
 * (the `session-tool-profiles` precedent).
 */

export const CHAT_FOLDERS_KEY = "mecatl-studio.chat-folders";
export const MAX_FOLDERS = 50;
export const FOLDER_NAME_MAX_CHARS = 60;

export interface ChatFolder {
  id: string;
  name: string;
}

export interface ChatFoldersState {
  /** In creation order — the order the sidebar lists them. */
  folders: readonly ChatFolder[];
  /** Folder id per chat (daemon session) id; a chat absent here is filed nowhere. */
  assignments: Readonly<Record<string, string>>;
}

const EMPTY_STATE: ChatFoldersState = { folders: [], assignments: {} };

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The case-insensitive identity two folder names collide on. */
function nameKey(name: string): string {
  return name.toLowerCase();
}

/**
 * A folder name as stored: trimmed and cut to the cap (by code point, so an
 * emoji is never split); "" when nothing is left.
 */
function normalizeFolderName(name: string): string {
  return Array.from(name.trim())
    .slice(0, FOLDER_NAME_MAX_CHARS)
    .join("")
    .trim();
}

/** The folder `sessionId` is filed in, or null (own keys only). */
export function chatFolderOf(
  assignments: Readonly<Record<string, string>>,
  sessionId: string,
): string | null {
  return Object.hasOwn(assignments, sessionId) ? assignments[sessionId] : null;
}

/**
 * Decodes the stored document, keeping only what is well-formed: folders
 * with a non-empty string id and a non-empty (normalized) name, the first
 * of any duplicate id or case-insensitive duplicate name, at most
 * `MAX_FOLDERS`; assignments only to a kept folder. Anything else — junk,
 * a non-object, a missing key — reads as the empty state.
 */
export function parseChatFolders(raw: string | null): ChatFoldersState {
  if (!raw) return EMPTY_STATE;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return EMPTY_STATE;
  }
  if (!isPlainObject(parsed)) return EMPTY_STATE;

  const folders: ChatFolder[] = [];
  const ids = new Set<string>();
  const names = new Set<string>();
  if (Array.isArray(parsed.folders)) {
    for (const item of parsed.folders) {
      if (folders.length >= MAX_FOLDERS) break;
      if (!isPlainObject(item)) continue;
      const { id, name } = item;
      if (typeof id !== "string" || id === "" || typeof name !== "string") {
        continue;
      }
      const clean = normalizeFolderName(name);
      if (!clean || ids.has(id) || names.has(nameKey(clean))) continue;
      ids.add(id);
      names.add(nameKey(clean));
      folders.push({ id, name: clean });
    }
  }
  if (folders.length === 0) return EMPTY_STATE;

  const assignments: Record<string, string> = {};
  if (isPlainObject(parsed.assignments)) {
    for (const [sessionId, folderId] of Object.entries(parsed.assignments)) {
      if (sessionId === "" || typeof folderId !== "string") continue;
      if (!ids.has(folderId)) continue;
      assignments[sessionId] = folderId;
    }
  }
  return { folders, assignments };
}

// ── the store ────────────────────────────────────────────────────────────────

const listeners = new Set<() => void>();
/** The last raw document seen and its decoded state — `useSyncExternalStore`
 *  needs a referentially stable snapshot for unchanged storage. */
let cache: { raw: string | null; state: ChatFoldersState } = {
  raw: null,
  state: EMPTY_STATE,
};

function readRaw(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(CHAT_FOLDERS_KEY);
  } catch {
    return null;
  }
}

function readState(): ChatFoldersState {
  const raw = readRaw();
  if (raw !== cache.raw) cache = { raw, state: parseChatFolders(raw) };
  return cache.state;
}

/** Persists `next` (removing the key once no folder is left) and notifies
 *  every subscriber. Storage that is disabled or full leaves the previous
 *  state in place — the change simply does not happen. */
function writeState(next: ChatFoldersState): void {
  if (typeof window === "undefined") return;
  const raw = next.folders.length === 0 ? null : JSON.stringify(next);
  try {
    if (raw === null) window.localStorage.removeItem(CHAT_FOLDERS_KEY);
    else window.localStorage.setItem(CHAT_FOLDERS_KEY, raw);
  } catch {
    return;
  }
  cache = { raw, state: next };
  for (const fn of listeners) fn();
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback);
  // Another tab's edit: `key` is null when that tab cleared storage.
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === CHAT_FOLDERS_KEY) callback();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(callback);
    window.removeEventListener("storage", onStorage);
  };
}

function getServerSnapshot(): ChatFoldersState {
  return EMPTY_STATE;
}

function newFolderId(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === "function") return c.randomUUID();
  return `f-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

// ── the mutations (module-level, so every hook instance shares them) ────────

/**
 * Creates a folder and returns its id. A folder that already has this name
 * (case-insensitively) is reused — typing "work" when "Work" exists files
 * into the existing one. Returns null when the name is empty or the
 * `MAX_FOLDERS` cap is reached.
 */
function createFolder(name: string): string | null {
  const clean = normalizeFolderName(name);
  if (!clean) return null;
  const state = readState();
  const existing = state.folders.find(
    (f) => nameKey(f.name) === nameKey(clean),
  );
  if (existing) return existing.id;
  if (state.folders.length >= MAX_FOLDERS) return null;
  const id = newFolderId();
  writeState({
    folders: [...state.folders, { id, name: clean }],
    assignments: state.assignments,
  });
  return id;
}

/**
 * Renames a folder. False when the folder is unknown, the name is empty, or
 * ANOTHER folder already has that name (case-insensitively); a change of
 * case on the folder's own name is a rename like any other.
 */
function renameFolder(id: string, name: string): boolean {
  const clean = normalizeFolderName(name);
  if (!clean) return false;
  const state = readState();
  const target = state.folders.find((f) => f.id === id);
  if (!target) return false;
  const key = nameKey(clean);
  if (state.folders.some((f) => f.id !== id && nameKey(f.name) === key)) {
    return false;
  }
  if (target.name === clean) return true;
  writeState({
    folders: state.folders.map((f) => (f.id === id ? { id, name: clean } : f)),
    assignments: state.assignments,
  });
  return true;
}

/** Removes a folder; the chats in it are unfiled, never touched. */
function deleteFolder(id: string): void {
  const state = readState();
  if (!state.folders.some((f) => f.id === id)) return;
  const assignments: Record<string, string> = {};
  for (const [sessionId, folderId] of Object.entries(state.assignments)) {
    if (folderId !== id) assignments[sessionId] = folderId;
  }
  writeState({
    folders: state.folders.filter((f) => f.id !== id),
    assignments,
  });
}

/** Files a chat in `folderId`, or in no folder (null). An unknown folder is
 *  ignored — a stale menu can never create a dangling assignment. */
function moveChat(sessionId: string, folderId: string | null): void {
  if (!sessionId) return;
  const state = readState();
  if (folderId !== null && !state.folders.some((f) => f.id === folderId)) {
    return;
  }
  if (chatFolderOf(state.assignments, sessionId) === folderId) return;
  const assignments = { ...state.assignments };
  if (folderId === null) delete assignments[sessionId];
  else assignments[sessionId] = folderId;
  writeState({ folders: state.folders, assignments });
}

/**
 * The chat folders and their mutations. Every mounted instance reads the
 * one store; SSR and the hydration frame read the empty state and patch up.
 */
export function useChatFolders() {
  const state = useSyncExternalStore(subscribe, readState, getServerSnapshot);
  return {
    folders: state.folders,
    assignments: state.assignments,
    createFolder,
    renameFolder,
    deleteFolder,
    moveChat,
  };
}

// ── grouping ─────────────────────────────────────────────────────────────────

export interface FolderGroup<T> {
  folder: ChatFolder;
  sessions: T[];
}

/**
 * Splits `sessions` (kept in their given order) into one group per folder
 * — in folder order, an EMPTY folder included so its header can still be
 * renamed or deleted — and the unfiled rest, which the caller buckets by
 * recency as before.
 */
export function partitionByFolder<T extends { id: string }>(
  sessions: readonly T[],
  folders: readonly ChatFolder[],
  assignments: Readonly<Record<string, string>>,
): { filed: FolderGroup<T>[]; unfiled: T[] } {
  const byFolder = new Map<string, T[]>(folders.map((f) => [f.id, []]));
  const unfiled: T[] = [];
  for (const session of sessions) {
    const folderId = chatFolderOf(assignments, session.id);
    const bucket = folderId === null ? undefined : byFolder.get(folderId);
    if (bucket) bucket.push(session);
    else unfiled.push(session);
  }
  return {
    filed: folders.map((folder) => ({
      folder,
      sessions: byFolder.get(folder.id) ?? [],
    })),
    unfiled,
  };
}
