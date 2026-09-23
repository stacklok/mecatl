// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from "react";

export interface ThreadAssociation {
  sessionId: string;
}

export type ThreadMap = Record<string, ThreadAssociation>;

const mapKeyPrefix = "studio.chat.threads.";
const sessionIdsKey = "studio.chat.threadSessions";
const threadsChangedEvent = "studio:threads-changed";

export function threadKeyForMessage(message: { content: string; role: string }) {
  let hash = 5381;
  for (let index = 0; index < message.content.length; index += 1) {
    hash = ((hash << 5) + hash + message.content.charCodeAt(index)) >>> 0;
  }
  return `${message.role}:${hash.toString(36)}:${message.content.length}`;
}

export function threadTitleFromRoot(content: string) {
  const line = content.replace(/\s+/gu, " ").trim();
  const snippet = line.length > 40 ? `${line.slice(0, 40).trimEnd()}…` : line;
  return `Thread: ${snippet || "(empty message)"}`;
}

export function parseThreadMap(raw: string | null): ThreadMap {
  if (!raw) return {};
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    const map: ThreadMap = {};
    for (const [key, candidate] of Object.entries(value)) {
      if (!candidate || typeof candidate !== "object") continue;
      const sessionId = (candidate as Partial<ThreadAssociation>).sessionId;
      if (typeof sessionId === "string" && sessionId) map[key] = { sessionId };
    }
    return map;
  } catch {
    return {};
  }
}

export function registerThreadSession(
  parentSessionId: string,
  messageKey: string,
  threadSessionId: string,
) {
  const map = readThreadMap(parentSessionId);
  map[messageKey] = { sessionId: threadSessionId };
  const ids = readThreadSessionIds();
  ids.add(threadSessionId);
  writeStorage(sessionIdsKey, JSON.stringify([...ids].sort()));
  writeThreadMap(parentSessionId, map);
}

export function useThreadMap(parentSessionId: string) {
  const [map, setMap] = useState<ThreadMap>(() => readThreadMap(parentSessionId));
  useEffect(() => {
    const refresh = () => setMap(readThreadMap(parentSessionId));
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

export function useThreadSessionIds() {
  const [ids, setIds] = useState<ReadonlySet<string>>(readThreadSessionIds);
  useEffect(() => {
    const refresh = () => setIds(readThreadSessionIds());
    const onStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === sessionIdsKey || event.key.startsWith(mapKeyPrefix)) {
        refresh();
      }
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

function readThreadMap(parentSessionId: string) {
  if (!parentSessionId) return {};
  return parseThreadMap(readStorage(mapStorageKey(parentSessionId)));
}

function writeThreadMap(parentSessionId: string, map: ThreadMap) {
  writeStorage(mapStorageKey(parentSessionId), JSON.stringify(map));
  window.dispatchEvent(new CustomEvent(threadsChangedEvent, { detail: parentSessionId }));
}

function readThreadSessionIds() {
  const ids = new Set<string>();
  try {
    const value: unknown = JSON.parse(readStorage(sessionIdsKey) ?? "[]");
    if (Array.isArray(value)) {
      for (const id of value) if (typeof id === "string" && id) ids.add(id);
    }
    for (let index = 0; index < window.localStorage.length; index += 1) {
      const key = window.localStorage.key(index);
      if (!key?.startsWith(mapKeyPrefix)) continue;
      for (const entry of Object.values(parseThreadMap(readStorage(key)))) ids.add(entry.sessionId);
    }
  } catch {
    // Keep the valid associations collected before storage failed.
  }
  return ids;
}

function mapStorageKey(parentSessionId: string) {
  return `${mapKeyPrefix}${encodeURIComponent(parentSessionId)}`;
}

function readStorage(key: string) {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Thread associations are browser-local and remain optional when storage is unavailable.
  }
}
