"use client";

import { useEffect, useState } from "react";
import type { AgentMessage } from "@/features/agent";

/**
 * Browser-local map of message threads. The daemon has no thread concept — a
 * thread is an ordinary session seeded from the parent conversation
 * (source_session_id carryover) — so which daemon session backs the thread
 * branched off a given message is a client-side view association, stored in
 * localStorage per PARENT session (same tier as the profile preferences).
 * Each entry also carries enough activity summary (reply count, last reply
 * time) to draw the Slack-style reply indicator without opening the thread.
 */

export interface ThreadSummary {
  /** The daemon session backing the thread. */
  sessionId: string;
  replyCount: number;
  /** Epoch ms of the newest reply; 0 until a timestamped one lands. */
  lastReplyAt: number;
}

/** Message key → thread summary, for one parent session. */
export type ThreadMap = Record<string, ThreadSummary>;

const KEY_PREFIX = "mecatl-studio.threads.";
/**
 * Flat registry of EVERY thread-backing session id, across all parents. The
 * per-parent maps above answer "which session backs this message's thread";
 * this set answers the sessions layer's cheaper question — "is this session
 * a thread at all?" — so the chat list, keyboard order, and global search can
 * hide thread sessions without walking every parent's map.
 */
const SESSIONS_KEY = "mecatl-studio.thread-sessions";
const THREADS_CHANGED_EVENT = "mecatl-studio:threads-changed";

const storageKey = (parentSessionId: string) =>
  `${KEY_PREFIX}${parentSessionId}`;

// ── Pure helpers ─────────────────────────────────────────────────────────────

/**
 * Stable key for a transcript message. Live-streamed messages and rehydrated
 * transcript messages get DIFFERENT ids (timestamp-minted vs positional), so
 * a thread keyed on the raw message id would detach from its message on
 * reload; role plus a content hash survives both renderings. Two byte-equal
 * messages in one chat share a thread — an accepted, unlikely collision.
 */
export function threadKeyForMessage(
  message: Pick<AgentMessage, "role" | "content">,
): string {
  // djb2 over UTF-16 code units, kept in uint32.
  let hash = 5381;
  for (let i = 0; i < message.content.length; i += 1) {
    hash = ((hash << 5) + hash + message.content.charCodeAt(i)) >>> 0;
  }
  return `${message.role}:${hash.toString(36)}:${message.content.length}`;
}

const TITLE_SNIPPET_MAX = 40;

/** Sidebar title for a thread session: "Thread: " + a single-line root snippet. */
export function threadTitleFromRoot(rootContent: string): string {
  const line = rootContent.replace(/\s+/g, " ").trim();
  const snippet =
    line.length > TITLE_SNIPPET_MAX
      ? `${line.slice(0, TITLE_SNIPPET_MAX).trimEnd()}…`
      : line;
  return `Thread: ${snippet || "(empty message)"}`;
}

const QUOTE_CLAMP = 500;

/**
 * The root message as a "> " quote block: clamped so a huge root cannot
 * balloon the thread prompt, newlines carried as "> " continuation lines.
 */
function quoteRoot(rootContent: string): string {
  const clamped =
    rootContent.length > QUOTE_CLAMP
      ? `${rootContent.slice(0, QUOTE_CLAMP)}…`
      : rootContent;
  return clamped
    .split("\n")
    .map((line) => (line ? `> ${line}` : ">"))
    .join("\n");
}

/**
 * The FIRST message sent into a thread: the quoted root message (so the model
 * sees exactly what the thread is scoped to — rendered honestly as part of
 * the message, not injected invisibly) followed by the user's own text.
 */
export function composeThreadPrompt(
  rootContent: string,
  userText: string,
): string {
  return `${quoteRoot(rootContent)}\n\n${userText}`;
}

/**
 * A thread session's transcript starts with the parent history it was seeded
 * from; the thread's own exchange begins at the first user message that opens
 * with the root quote block (the deterministic composeThreadPrompt prefix).
 * Returns the messages from that boundary — empty when no reply landed yet.
 */
/**
 * Display transform for thread replies: the root-quote block
 * composeThreadPrompt prepends is REDUNDANT inside the panel (the root
 * message is pinned right above), so a user reply that opens with the
 * root's own quote renders without it. Quotes of OTHER text (add-to-thread
 * selections) are meaningful and stay.
 */
export function stripRootQuote<T extends { role: string; content: string }>(
  replies: T[],
  rootContent: string,
): T[] {
  const rootQuote = quoteRoot(rootContent);
  return replies.map((reply) => {
    if (reply.role !== "user") return reply;
    if (!reply.content.startsWith(rootQuote)) return reply;
    const rest = reply.content.slice(rootQuote.length).replace(/^\n+/, "");
    return rest ? { ...reply, content: rest } : reply;
  });
}

export function sliceThreadReplies(
  messages: AgentMessage[],
  rootContent: string,
): AgentMessage[] {
  const boundary = `${quoteRoot(rootContent)}\n\n`;
  const index = messages.findIndex(
    (message) =>
      message.role === "user" && message.content.startsWith(boundary),
  );
  return index === -1 ? [] : messages.slice(index);
}

// ── Storage ──────────────────────────────────────────────────────────────────

function readStorage(key: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Storage disabled or full — the thread association just doesn't persist.
  }
}

/** Reads (and defensively re-validates) one parent session's thread map. */
export function readThreadMap(parentSessionId: string): ThreadMap {
  const raw = readStorage(storageKey(parentSessionId));
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return {};
    }
    const map: ThreadMap = {};
    for (const [key, value] of Object.entries(
      parsed as Record<string, unknown>,
    )) {
      const entry = value as Partial<ThreadSummary> | null;
      if (!entry || typeof entry.sessionId !== "string" || !entry.sessionId) {
        continue;
      }
      map[key] = {
        sessionId: entry.sessionId,
        replyCount:
          typeof entry.replyCount === "number" && entry.replyCount > 0
            ? Math.floor(entry.replyCount)
            : 0,
        lastReplyAt:
          typeof entry.lastReplyAt === "number" && entry.lastReplyAt > 0
            ? entry.lastReplyAt
            : 0,
      };
    }
    return map;
  } catch {
    return {};
  }
}

function writeThreadMap(parentSessionId: string, map: ThreadMap) {
  writeStorage(storageKey(parentSessionId), JSON.stringify(map));
  if (typeof window !== "undefined") {
    window.dispatchEvent(
      new CustomEvent<string>(THREADS_CHANGED_EVENT, {
        detail: parentSessionId,
      }),
    );
  }
}

/** The daemon session backing this message's thread, if one was ever minted. */
export function getThreadSession(
  parentSessionId: string,
  messageKey: string,
): string | null {
  return readThreadMap(parentSessionId)[messageKey]?.sessionId ?? null;
}

/** Reads (and defensively re-validates) the flat thread-session-id registry. */
function readThreadSessionIds(): Set<string> {
  const ids = new Set<string>();
  const raw = readStorage(SESSIONS_KEY);
  if (raw) {
    try {
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        for (const id of parsed) {
          if (typeof id === "string" && id !== "") ids.add(id);
        }
      }
    } catch {
      // Corrupt registry: fall through to the per-parent sweep below.
    }
  }
  // Threads minted before the flat registry existed are recorded only in the
  // per-parent maps; sweep those keys too so they hide without a migration.
  if (typeof window !== "undefined") {
    try {
      for (let i = 0; i < window.localStorage.length; i += 1) {
        const key = window.localStorage.key(i);
        if (!key?.startsWith(KEY_PREFIX)) continue;
        const map = readThreadMap(key.slice(KEY_PREFIX.length));
        for (const entry of Object.values(map)) ids.add(entry.sessionId);
      }
    } catch {
      // Storage disabled: whatever the registry read yielded stands.
    }
  }
  return ids;
}

/**
 * True when this session id was minted to back a message thread. Membership
 * in the registry — never the "Thread: " title — is the test, so a chat the
 * user happened to name "Thread: …" themselves is never hidden.
 */
export function isThreadSession(sessionId: string): boolean {
  return readThreadSessionIds().has(sessionId);
}

/** Records a freshly minted thread session for a root message. */
export function registerThreadSession(
  parentSessionId: string,
  messageKey: string,
  threadSessionId: string,
) {
  const map = readThreadMap(parentSessionId);
  map[messageKey] = {
    sessionId: threadSessionId,
    replyCount: 0,
    lastReplyAt: 0,
  };
  // Mirror the id into the flat registry BEFORE the map write: the map write
  // fires the change event listeners, which must already see the new id.
  const ids = readThreadSessionIds();
  if (!ids.has(threadSessionId)) {
    ids.add(threadSessionId);
    writeStorage(SESSIONS_KEY, JSON.stringify([...ids]));
  }
  writeThreadMap(parentSessionId, map);
}

/**
 * Detaches a thread from its root message — the "convert to a full chat"
 * path: the id leaves the flat registry (so the sidebar shows it) and the
 * per-parent record goes (so the reply indicator does too). The session
 * itself is untouched; it was always a real daemon session.
 */
export function unregisterThreadSession(
  parentSessionId: string,
  threadSessionId: string,
) {
  const ids = readThreadSessionIds();
  if (ids.delete(threadSessionId)) {
    writeStorage(SESSIONS_KEY, JSON.stringify([...ids]));
  }
  const map = readThreadMap(parentSessionId);
  let changed = false;
  for (const [key, record] of Object.entries(map)) {
    if (record.sessionId === threadSessionId) {
      delete map[key];
      changed = true;
    }
  }
  if (changed || ids.size >= 0) writeThreadMap(parentSessionId, map);
}

/**
 * Mirrors the thread's observed activity into the map. Set semantics for the
 * count — the caller derives it from the authoritative reply list, so it
 * self-heals on rehydration — while lastReplyAt only ever advances, because
 * rehydrated transcripts carry no timestamps and a 0 must not erase a real
 * one. A no-op for unknown keys and for writes that would change nothing.
 */
export function syncThreadActivity(
  parentSessionId: string,
  messageKey: string,
  replyCount: number,
  lastReplyAt: number,
) {
  const map = readThreadMap(parentSessionId);
  const entry = map[messageKey];
  if (!entry) return;
  const nextLast = Math.max(entry.lastReplyAt, lastReplyAt);
  if (entry.replyCount === replyCount && entry.lastReplyAt === nextLast) {
    return;
  }
  map[messageKey] = { ...entry, replyCount, lastReplyAt: nextLast };
  writeThreadMap(parentSessionId, map);
}

/**
 * Live view of one parent session's thread map, for the transcript's reply
 * indicators. Hydrates after mount (the server renders none) and follows
 * writes from this tab (the change event) and other tabs (the storage event).
 */
export function useThreadMap(parentSessionId: string): ThreadMap {
  const [map, setMap] = useState<ThreadMap>({});
  useEffect(() => {
    if (!parentSessionId) {
      setMap({});
      return;
    }
    const refresh = () => setMap(readThreadMap(parentSessionId));
    refresh();
    const onChanged = (event: Event) => {
      if ((event as CustomEvent<string>).detail === parentSessionId) refresh();
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === storageKey(parentSessionId)) {
        refresh();
      }
    };
    window.addEventListener(THREADS_CHANGED_EVENT, onChanged);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(THREADS_CHANGED_EVENT, onChanged);
      window.removeEventListener("storage", onStorage);
    };
  }, [parentSessionId]);
  return map;
}

/**
 * Live view of the flat thread-session-id registry, for the sessions layer's
 * presentation seam (hide thread sessions from the chat list and search).
 * Hydrates after mount (SSR renders an empty set) and follows registrations
 * from this tab (the change event) and other tabs (the storage event).
 */
export function useThreadSessionIds(): ReadonlySet<string> {
  const [ids, setIds] = useState<ReadonlySet<string>>(new Set());
  useEffect(() => {
    const refresh = () => setIds(readThreadSessionIds());
    refresh();
    const onStorage = (event: StorageEvent) => {
      if (
        event.key === null ||
        event.key === SESSIONS_KEY ||
        event.key.startsWith(KEY_PREFIX) // legacy per-parent-map threads
      ) {
        refresh();
      }
    };
    window.addEventListener(THREADS_CHANGED_EVENT, refresh);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(THREADS_CHANGED_EVENT, refresh);
      window.removeEventListener("storage", onStorage);
    };
  }, []);
  return ids;
}
