// SPDX-License-Identifier: Apache-2.0

import type { SessionTranscriptResponse } from "@mecatl-studio/contracts";
import { useEffect, useState } from "react";
import {
  listUserScopedKeys,
  readUserScopedItem,
  writeUserScopedItem,
} from "../../lib/account-storage";
import type { ChatMessage } from "./chat-state";

export interface ThreadRoot {
  ordinal: number;
  role: string;
  digest: string;
}

export interface ThreadAssociation extends ThreadRoot {
  sessionId: string;
}

export interface ThreadMapV2 {
  version: 2;
  entries: ThreadAssociation[];
}

/** The old role/content key is used only to recover browser-local v1 associations. */
type LegacyThreadMap = Record<string, { sessionId: string }>;
type ThreadMap = Record<string, { sessionId: string }>;

export interface ThreadAssociations {
  byKey: ThreadMap;
  legacyCandidates: Record<string, { legacyKey: string; sessionId: string }>;
  source?: SessionTranscriptResponse;
}

const mapKeyPrefix = "studio.chat.threads.";
// Migration bookkeeping only. Canonical associations always live at mapKeyPrefix as v2.
// An ambiguous v1 root stays here until the reader chooses its recorded occurrence.
const pendingLegacyPrefix = "studio.chat.pendingLegacyThreads.";
const sessionIdsKey = "studio.chat.threadSessions";
const threadsChangedEvent = "studio:threads-changed";
const emptyMap = (): ThreadMapV2 => ({ entries: [], version: 2 });
const emptyAssociations = (): ThreadAssociations => ({ byKey: {}, legacyCandidates: {} });

export function threadKeyForMessage(message: { content: string; role: string }) {
  let hash = 5381;
  for (let index = 0; index < message.content.length; index += 1) {
    hash = ((hash << 5) + hash + message.content.charCodeAt(index)) >>> 0;
  }
  return `${message.role}:${hash.toString(36)}:${message.content.length}`;
}

export function threadKeyForRoot(root: ThreadRoot): string {
  return JSON.stringify([root.ordinal, root.role, root.digest]);
}

function rootFromKey(key: string): ThreadRoot | undefined {
  try {
    const parsed: unknown = JSON.parse(key);
    if (!Array.isArray(parsed) || parsed.length !== 3) return undefined;
    const [ordinal, role, digest] = parsed;
    if (
      !Number.isSafeInteger(ordinal) ||
      ordinal < 0 ||
      typeof role !== "string" ||
      !role ||
      typeof digest !== "string" ||
      !/^[a-f0-9]{64}$/u.test(digest)
    )
      return undefined;
    return { digest, ordinal, role };
  } catch {
    return undefined;
  }
}

async function digestForMessage(role: string, text: string): Promise<string> {
  const bytes = new TextEncoder().encode(`${role}\0${text}`);
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** A render-local ID alone never establishes a persisted parent-message identity. */
export async function recordedRootForMessage(
  message: Pick<ChatMessage, "content" | "id" | "recordedOrdinal" | "role">,
  transcript: SessionTranscriptResponse | undefined,
): Promise<ThreadRoot | undefined> {
  const ordinal = message.recordedOrdinal;
  if (
    !transcript?.complete ||
    !Number.isSafeInteger(ordinal) ||
    ordinal === undefined ||
    ordinal < 0
  )
    return undefined;
  const entry = transcript.messages[ordinal];
  if (
    !entry ||
    entry.toolResult ||
    (entry.role !== "user" && entry.role !== "assistant") ||
    entry.role !== message.role ||
    entry.text !== message.content
  )
    return undefined;
  return { digest: await digestForMessage(entry.role, entry.text), ordinal, role: entry.role };
}

function recordedRoots(
  transcript: SessionTranscriptResponse,
  map: ThreadMapV2,
  pending: LegacyThreadMap,
) {
  const associatedOrdinals = new Set(map.entries.map((entry) => entry.ordinal));
  const pendingKeys = new Set(Object.keys(pending));
  return Promise.all(
    transcript.messages.flatMap((entry, ordinal) => {
      if (entry.toolResult || (entry.role !== "user" && entry.role !== "assistant")) return [];
      const legacyKey = threadKeyForMessage({ content: entry.text, role: entry.role });
      if (!associatedOrdinals.has(ordinal) && !pendingKeys.has(legacyKey)) return [];
      return [
        digestForMessage(entry.role, entry.text).then((digest) => ({
          digest,
          legacyKey,
          ordinal,
          role: entry.role,
        })),
      ];
    }),
  );
}

export function threadTitleFromRoot(content: string) {
  const line = content.replace(/\s+/gu, " ").trim();
  const snippet = line.length > 40 ? `${line.slice(0, 40).trimEnd()}…` : line;
  return `Thread: ${snippet || "(empty message)"}`;
}

export function parseThreadMap(raw: string | null): ThreadMapV2 {
  if (!raw) return emptyMap();
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value)) return emptyMap();
    if ((value as Partial<ThreadMapV2>).version !== 2) return emptyMap();
    const entries = (value as Partial<ThreadMapV2>).entries;
    if (!Array.isArray(entries)) return emptyMap();
    const valid: ThreadAssociation[] = [];
    for (const candidate of entries) {
      if (!candidate || typeof candidate !== "object") continue;
      const { digest, ordinal, role, sessionId } = candidate as Partial<ThreadAssociation>;
      if (
        !Number.isSafeInteger(ordinal) ||
        ordinal === undefined ||
        ordinal < 0 ||
        typeof role !== "string" ||
        !role ||
        typeof digest !== "string" ||
        !/^[a-f0-9]{64}$/u.test(digest) ||
        typeof sessionId !== "string" ||
        !sessionId
      )
        continue;
      valid.push({ digest, ordinal, role, sessionId });
    }
    return { entries: valid, version: 2 };
  } catch {
    return emptyMap();
  }
}

function parseLegacyMap(raw: string | null): LegacyThreadMap {
  if (!raw) return {};
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value) || "version" in value)
      return {};
    const map: LegacyThreadMap = {};
    for (const [key, candidate] of Object.entries(value)) {
      if (!candidate || typeof candidate !== "object") continue;
      const sessionId = (candidate as { sessionId?: unknown }).sessionId;
      if (typeof sessionId === "string" && sessionId) map[key] = { sessionId };
    }
    return map;
  } catch {
    return {};
  }
}

function isV2(raw: string | null) {
  if (!raw) return false;
  try {
    const value: unknown = JSON.parse(raw);
    return !!value && typeof value === "object" && "version" in value && value.version === 2;
  } catch {
    return false;
  }
}

function mapStorageKey(parentSessionId: string) {
  return `${mapKeyPrefix}${encodeURIComponent(parentSessionId)}`;
}

function pendingStorageKey(parentSessionId: string) {
  return `${pendingLegacyPrefix}${encodeURIComponent(parentSessionId)}`;
}

function notifyChanged(parentSessionId: string) {
  window.dispatchEvent(new CustomEvent(threadsChangedEvent, { detail: parentSessionId }));
}

function writeV2(parentSessionId: string, map: ThreadMapV2) {
  writeUserScopedItem(mapStorageKey(parentSessionId), JSON.stringify(map));
  notifyChanged(parentSessionId);
}

function writePending(parentSessionId: string, pending: LegacyThreadMap) {
  writeUserScopedItem(
    pendingStorageKey(parentSessionId),
    Object.keys(pending).length ? JSON.stringify(pending) : null,
  );
  notifyChanged(parentSessionId);
}

function canonicalAndPending(parentSessionId: string) {
  const raw = readUserScopedItem(mapStorageKey(parentSessionId));
  const pending = parseLegacyMap(readUserScopedItem(pendingStorageKey(parentSessionId)));
  if (!isV2(raw) && raw) {
    Object.assign(pending, parseLegacyMap(raw));
    writeUserScopedItem(mapStorageKey(parentSessionId), JSON.stringify(emptyMap()));
    writePending(parentSessionId, pending);
  }
  return { map: parseThreadMap(readUserScopedItem(mapStorageKey(parentSessionId))), pending };
}

export function registerThreadSession(
  parentSessionId: string,
  messageKey: string,
  threadSessionId: string,
) {
  if (!parentSessionId || !threadSessionId) return;
  const root = rootFromKey(messageKey);
  if (root) {
    const { map } = canonicalAndPending(parentSessionId);
    const entries = map.entries.filter((entry) => threadKeyForRoot(entry) !== messageKey);
    entries.push({ ...root, sessionId: threadSessionId });
    entries.sort((left, right) => left.ordinal - right.ordinal);
    writeV2(parentSessionId, { entries, version: 2 });
  } else {
    // Compatibility for a page still holding a v1 key during an account transition.
    // The normal chat path passes only validated v2 occurrence keys.
    const raw = readUserScopedItem(mapStorageKey(parentSessionId));
    if (isV2(raw)) return;
    const map = parseLegacyMap(raw);
    map[messageKey] = { sessionId: threadSessionId };
    writeUserScopedItem(mapStorageKey(parentSessionId), JSON.stringify(map));
    notifyChanged(parentSessionId);
  }
  const ids = readThreadSessionIds();
  ids.add(threadSessionId);
  writeUserScopedItem(sessionIdsKey, JSON.stringify([...ids].sort()));
}

export async function readThreadAssociations(
  parentSessionId: string,
  transcript: SessionTranscriptResponse | undefined,
): Promise<ThreadAssociations> {
  if (!parentSessionId || !transcript?.complete || transcript.sessionId !== parentSessionId)
    return emptyAssociations();
  const { map, pending } = canonicalAndPending(parentSessionId);
  const roots = await recordedRoots(transcript, map, pending);
  const entries = [...map.entries];
  let migrated = false;
  for (const [legacyKey, older] of Object.entries(pending)) {
    const matches = roots.filter((root) => root.legacyKey === legacyKey);
    if (matches.length !== 1) continue;
    const match = matches[0];
    if (!match) continue;
    const key = threadKeyForRoot(match);
    if (entries.some((entry) => threadKeyForRoot(entry) === key)) continue;
    entries.push({
      digest: match.digest,
      ordinal: match.ordinal,
      role: match.role,
      sessionId: older.sessionId,
    });
    delete pending[legacyKey];
    migrated = true;
  }
  if (migrated) {
    entries.sort((left, right) => left.ordinal - right.ordinal);
    writeV2(parentSessionId, { entries, version: 2 });
    writePending(parentSessionId, pending);
  }
  const byKey: ThreadMap = {};
  for (const entry of entries) {
    const root = roots.find((candidate) => candidate.ordinal === entry.ordinal);
    if (!root || root.role !== entry.role || root.digest !== entry.digest) continue;
    byKey[threadKeyForRoot(entry)] = { sessionId: entry.sessionId };
  }
  const legacyCandidates: ThreadAssociations["legacyCandidates"] = {};
  for (const [legacyKey, older] of Object.entries(pending)) {
    for (const root of roots) {
      if (root.legacyKey === legacyKey) {
        legacyCandidates[threadKeyForRoot(root)] = { legacyKey, sessionId: older.sessionId };
      }
    }
  }
  return { byKey, legacyCandidates, source: transcript };
}

/** Finds a proved visible root without hashing during React's render. */
export function matchingThreadKeyForMessage(
  message: Pick<ChatMessage, "content" | "recordedOrdinal" | "role">,
  transcript: SessionTranscriptResponse | undefined,
  associations: ThreadAssociations,
): string | undefined {
  if (!transcript || associations.source !== transcript) return undefined;
  const ordinal = message.recordedOrdinal;
  if (ordinal === undefined || !Number.isSafeInteger(ordinal)) return undefined;
  const row = transcript.messages[ordinal];
  if (!row || row.role !== message.role || row.text !== message.content) return undefined;
  return [...Object.keys(associations.byKey), ...Object.keys(associations.legacyCandidates)].find(
    (key) => {
      const root = rootFromKey(key);
      return root?.ordinal === ordinal && root.role === message.role;
    },
  );
}

/** The caller supplies the same authoritative transcript used to display the choice. */
export async function relinkLegacyThread(
  parentSessionId: string,
  legacyKey: string,
  rootKey: string,
  transcript: SessionTranscriptResponse | undefined,
): Promise<boolean> {
  const choices = await readThreadAssociations(parentSessionId, transcript);
  if (choices.legacyCandidates[rootKey]?.legacyKey !== legacyKey) return false;
  const root = rootFromKey(rootKey);
  if (!root) return false;
  const { map, pending } = canonicalAndPending(parentSessionId);
  const older = pending[legacyKey];
  if (!older) return false;
  const entries = map.entries.filter((entry) => threadKeyForRoot(entry) !== rootKey);
  entries.push({ ...root, sessionId: older.sessionId });
  entries.sort((left, right) => left.ordinal - right.ordinal);
  delete pending[legacyKey];
  writeV2(parentSessionId, { entries, version: 2 });
  writePending(parentSessionId, pending);
  return true;
}

export function useThreadAssociations(
  parentSessionId: string,
  transcript: SessionTranscriptResponse | undefined,
) {
  const [associations, setAssociations] = useState<ThreadAssociations>(emptyAssociations);
  useEffect(() => {
    let active = true;
    const refresh = () => {
      void readThreadAssociations(parentSessionId, transcript).then((next) => {
        if (active) setAssociations(next);
      });
    };
    setAssociations(emptyAssociations());
    refresh();
    const onChanged = (event: Event) => {
      if ((event as CustomEvent<string>).detail === parentSessionId) refresh();
    };
    const onStorage = (event: StorageEvent) => {
      if (
        event.key === null ||
        event.key === mapStorageKey(parentSessionId) ||
        event.key === pendingStorageKey(parentSessionId)
      )
        refresh();
    };
    window.addEventListener(threadsChangedEvent, onChanged);
    window.addEventListener("storage", onStorage);
    return () => {
      active = false;
      window.removeEventListener(threadsChangedEvent, onChanged);
      window.removeEventListener("storage", onStorage);
    };
  }, [parentSessionId, transcript]);
  return associations;
}

/** Kept for account-isolation readers of legacy browser data. */
export function useThreadMap(parentSessionId: string) {
  const [map, setMap] = useState<ThreadMap>(() => readLegacyOrV2Map(parentSessionId));
  useEffect(() => {
    const refresh = () => setMap(readLegacyOrV2Map(parentSessionId));
    refresh();
    const onChanged = (event: Event) => {
      if ((event as CustomEvent<string>).detail === parentSessionId) refresh();
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === mapStorageKey(parentSessionId)) refresh();
    };
    window.addEventListener(threadsChangedEvent, onChanged);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(threadsChangedEvent, onChanged);
      window.removeEventListener("storage", onStorage);
    };
  }, [parentSessionId]);
  return map;
}

function readLegacyOrV2Map(parentSessionId: string): ThreadMap {
  if (!parentSessionId) return {};
  const raw = readUserScopedItem(mapStorageKey(parentSessionId));
  if (!isV2(raw)) return parseLegacyMap(raw);
  return Object.fromEntries(
    parseThreadMap(raw).entries.map((entry) => [
      threadKeyForRoot(entry),
      { sessionId: entry.sessionId },
    ]),
  );
}

export function useThreadSessionIds() {
  const [ids, setIds] = useState<ReadonlySet<string>>(readThreadSessionIds);
  useEffect(() => {
    const refresh = () => setIds(readThreadSessionIds());
    const onStorage = (event: StorageEvent) => {
      if (
        event.key === null ||
        event.key === sessionIdsKey ||
        event.key.startsWith(mapKeyPrefix) ||
        event.key.startsWith(pendingLegacyPrefix)
      )
        refresh();
    };
    window.addEventListener(threadsChangedEvent, refresh);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(threadsChangedEvent, refresh);
      window.removeEventListener("storage", onStorage);
    };
  }, []);
  return ids;
}

function readThreadSessionIds() {
  const ids = new Set<string>();
  try {
    const value: unknown = JSON.parse(readUserScopedItem(sessionIdsKey) ?? "[]");
    if (Array.isArray(value)) {
      for (const id of value) if (typeof id === "string" && id) ids.add(id);
    }
    for (const key of listUserScopedKeys(mapKeyPrefix)) {
      const raw = readUserScopedItem(key);
      if (isV2(raw)) {
        for (const entry of parseThreadMap(raw).entries) ids.add(entry.sessionId);
      } else {
        for (const entry of Object.values(parseLegacyMap(raw))) ids.add(entry.sessionId);
      }
    }
    for (const key of listUserScopedKeys(pendingLegacyPrefix)) {
      for (const entry of Object.values(parseLegacyMap(readUserScopedItem(key))))
        ids.add(entry.sessionId);
    }
  } catch {
    // Keep valid associations collected before storage failed.
  }
  return ids;
}
