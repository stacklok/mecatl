// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { useCallback, useEffect, useState } from "react";
import { readUserScopedItem, writeUserScopedItem } from "../../lib/account-storage";

const storageKey = "studio.chat.folders";
const maximumFolders = 50;
const maximumFolderNameLength = 60;

export interface ChatFolder {
  id: string;
  name: string;
}

export interface ChatFolderState {
  assignments: Record<string, string>;
  folders: ChatFolder[];
}

export interface SessionGroup {
  id: string;
  items: SessionSummaryResponse[];
  label: string;
}

const emptyState: ChatFolderState = { assignments: {}, folders: [] };

export function useChatFolders() {
  const [state, setState] = useState<ChatFolderState>(readChatFolders);

  useEffect(() => {
    const synchronize = (event: StorageEvent) => {
      if (event.key === null || event.key === storageKey) {
        setState(parseChatFolders(event.newValue));
      }
    };
    window.addEventListener("storage", synchronize);
    return () => window.removeEventListener("storage", synchronize);
  }, []);

  const update = useCallback((change: (current: ChatFolderState) => ChatFolderState) => {
    setState((current) => {
      const next = change(current);
      writeUserScopedItem(storageKey, next.folders.length === 0 ? null : JSON.stringify(next));
      return next;
    });
  }, []);

  const create = useCallback(
    (name: string, sessionId?: string) => {
      const normalized = normalizeName(name);
      if (!normalized) return;
      update((current) => {
        const existing = current.folders.find(
          (folder) => nameKey(folder.name) === nameKey(normalized),
        );
        if (existing) {
          return sessionId === undefined
            ? current
            : assignChatFolder(current, sessionId, existing.id);
        }
        if (current.folders.length >= maximumFolders) return current;
        const folder = { id: crypto.randomUUID(), name: normalized };
        return {
          assignments:
            sessionId === undefined
              ? current.assignments
              : { ...current.assignments, [sessionId]: folder.id },
          folders: [...current.folders, folder],
        };
      });
    },
    [update],
  );

  const rename = useCallback(
    (folderId: string, name: string) => {
      const normalized = normalizeName(name);
      if (!normalized) return;
      update((current) => {
        if (
          current.folders.some(
            (folder) => folder.id !== folderId && nameKey(folder.name) === nameKey(normalized),
          )
        ) {
          return current;
        }
        return {
          ...current,
          folders: current.folders.map((folder) =>
            folder.id === folderId ? { ...folder, name: normalized } : folder,
          ),
        };
      });
    },
    [update],
  );

  const remove = useCallback(
    (folderId: string) => {
      update((current) => deleteChatFolder(current, folderId));
    },
    [update],
  );

  const move = useCallback(
    (sessionId: string, folderId?: string) => {
      update((current) => assignChatFolder(current, sessionId, folderId));
    },
    [update],
  );

  return { create, move, remove, rename, state };
}

export function parseChatFolders(raw: string | null): ChatFolderState {
  if (!raw) return emptyState;
  try {
    const value: unknown = JSON.parse(raw);
    if (!isRecord(value) || !Array.isArray(value.folders) || !isRecord(value.assignments)) {
      return emptyState;
    }
    const seen = new Set<string>();
    const names = new Set<string>();
    const folders = value.folders.flatMap((entry) => {
      if (!isRecord(entry) || typeof entry.id !== "string" || typeof entry.name !== "string") {
        return [];
      }
      const name = normalizeName(entry.name);
      const normalizedName = nameKey(name);
      if (
        !entry.id ||
        !name ||
        seen.has(entry.id) ||
        names.has(normalizedName) ||
        seen.size >= maximumFolders
      ) {
        return [];
      }
      seen.add(entry.id);
      names.add(normalizedName);
      return [{ id: entry.id, name }];
    });
    const assignments = Object.fromEntries(
      Object.entries(value.assignments).flatMap(([sessionId, folderId]) =>
        sessionId && typeof folderId === "string" && seen.has(folderId)
          ? [[sessionId, folderId]]
          : [],
      ),
    );
    return { assignments, folders };
  } catch {
    return emptyState;
  }
}

export function assignChatFolder(
  state: ChatFolderState,
  sessionId: string,
  folderId?: string,
): ChatFolderState {
  if (!sessionId) return state;
  const assignments = { ...state.assignments };
  if (folderId && !state.folders.some((folder) => folder.id === folderId)) {
    return state;
  }
  if (folderId) {
    assignments[sessionId] = folderId;
  } else {
    delete assignments[sessionId];
  }
  return { ...state, assignments };
}

export function deleteChatFolder(state: ChatFolderState, folderId: string): ChatFolderState {
  return {
    assignments: Object.fromEntries(
      Object.entries(state.assignments).filter(([, assignment]) => assignment !== folderId),
    ),
    folders: state.folders.filter((folder) => folder.id !== folderId),
  };
}

export function groupSessions(
  sessions: SessionSummaryResponse[],
  folders: ChatFolderState,
  now = new Date(),
): SessionGroup[] {
  const assignedIds = new Set<string>();
  const groups = folders.folders.map((folder) => {
    const items = sessions.filter((session) => folders.assignments[session.id] === folder.id);
    for (const item of items) assignedIds.add(item.id);
    return { id: `folder:${folder.id}`, items, label: folder.name };
  });
  const recency = new Map<string, SessionSummaryResponse[]>([
    ["today", []],
    ["week", []],
    ["earlier", []],
  ]);
  for (const session of sessions) {
    if (assignedIds.has(session.id)) continue;
    recency.get(recencyBucket(session.updatedAt, now))?.push(session);
  }
  for (const [id, label] of [
    ["today", "Today"],
    ["week", "This week"],
    ["earlier", "Earlier"],
  ] as const) {
    const items = recency.get(id) ?? [];
    if (items.length > 0) groups.push({ id, items, label });
  }
  return groups;
}

function readChatFolders(): ChatFolderState {
  if (typeof window === "undefined") return emptyState;
  return parseChatFolders(readUserScopedItem(storageKey));
}

function normalizeName(name: string): string {
  return Array.from(name.trim()).slice(0, maximumFolderNameLength).join("").trim();
}

function nameKey(name: string): string {
  return name.toLocaleLowerCase();
}

function recencyBucket(updatedAt: string, now: Date): "earlier" | "today" | "week" {
  const updated = new Date(updatedAt);
  if (Number.isNaN(updated.getTime())) return "earlier";
  if (updated.toDateString() === now.toDateString()) return "today";
  const elapsed = now.getTime() - updated.getTime();
  return elapsed >= 0 && elapsed < 7 * 24 * 60 * 60 * 1_000 ? "week" : "earlier";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
