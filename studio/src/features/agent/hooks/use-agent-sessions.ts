"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  createHarnessSession,
  deleteHarnessSession,
  fetchAllSessions,
  renameHarnessSession,
} from "@/lib/harness/client";
import type { SessionSummary } from "@/lib/protocol";
import { useRuntimeStatus } from "../runtime-status";
import type { AgentSession, CreateSessionOpts } from "../types";

const POLL_INTERVAL_MS = 20_000;

function toAgentSession(summary: SessionSummary): AgentSession {
  return {
    id: summary.sessionId,
    title: summary.title || "Untitled chat",
    projectId: null,
    model: summary.modelId,
    createdAt: summary.createdAt,
    updatedAt: summary.modifiedAt,
    pinned: false,
    archived: false,
    messageCount: summary.turns,
    isStreaming: summary.state === "running",
    inputTokens: 0,
    outputTokens: 0,
    unread: false,
    estimatedCost: null,
    contextLength: null,
    lastPromptTokens: null,
    thresholdTokens: null,
    state: summary.state,
    canRename: summary.canRename,
    canDelete: summary.canDelete,
    renameReason: summary.renameReason,
    deleteReason: summary.deleteReason,
    titleProvenance: summary.titleProvenance,
    debugTargetSessionId: summary.debugTargetSessionId,
  };
}

/**
 * The chat list, backed by the daemon's session store — the record of chats.
 *
 * Invariants (from the server-backed-chats design):
 * - Only chats appear: rows whose one not-a-chat reason is
 *   `inspect_only_kind` (subagents, team members, scheduled fires) are
 *   filtered by the decoder.
 * - A row is removed only when a COMPLETE inventory walk proves it gone; a
 *   partial walk merges and never deletes.
 * - Action eligibility (rename/delete) comes from the row's capabilities,
 *   never re-derived client-side.
 * - A rename is optimistic but adopts the daemon's clamped title echo, and
 *   rolls back when the daemon refuses.
 */
export function useAgentSessions() {
  const { connected } = useRuntimeStatus();
  const [sessions, setSessions] = useState<AgentSession[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const loadedOnce = useRef(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const walk = await fetchAllSessions(signal);
      if (signal?.aborted) return;
      const chats = walk.sessions
        .filter((summary) => summary.isChat)
        .map(toAgentSession);
      setSessions((previous) => {
        if (walk.complete) return chats;
        // Incomplete walk: update what we saw, keep what we did not.
        const seen = new Map(chats.map((chat) => [chat.id, chat]));
        const merged = previous.map((chat) => seen.get(chat.id) ?? chat);
        const known = new Set(previous.map((chat) => chat.id));
        return [...merged, ...chats.filter((chat) => !known.has(chat.id))];
      });
      setError(null);
      loadedOnce.current = true;
    } catch (caught) {
      if (signal?.aborted) return;
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    const timer = setInterval(() => {
      void load(controller.signal);
    }, POLL_INTERVAL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [connected, load]);

  const refreshSessions = useCallback(async () => {
    await load();
  }, [load]);

  /** Creates a daemon session and returns its row. The id IS the daemon id. */
  const createSession = useCallback(
    async (_opts: CreateSessionOpts = {}) => {
      const sessionId = await createHarnessSession("default");
      const session: AgentSession = {
        id: sessionId,
        title: "Untitled chat",
        projectId: null,
        model: "",
        createdAt: Date.now(),
        updatedAt: Date.now(),
        pinned: false,
        archived: false,
        unread: false,
        messageCount: 0,
        isStreaming: false,
        inputTokens: 0,
        outputTokens: 0,
        estimatedCost: null,
        contextLength: null,
        lastPromptTokens: null,
        thresholdTokens: null,
      };
      setSessions((previous) => [session, ...previous]);
      void load();
      return session;
    },
    [load],
  );

  const deleteSession = useCallback(async (id: string) => {
    try {
      await deleteHarnessSession(id);
    } catch (caught) {
      // Only a 404 proves the session is already gone; any other refusal
      // keeps the row (the daemon may recover it).
      const message = caught instanceof Error ? caught.message : String(caught);
      if (!/not found|404/i.test(message)) {
        setError(message);
        throw caught;
      }
    }
    setSessions((previous) => previous.filter((s) => s.id !== id));
  }, []);

  // Title-provenance rule (F4): renames HERE are always operator-initiated
  // (the rename dialog), so clobbering is impossible by construction — the
  // daemon stamps the echo `title_provenance: "operator"`, mirrored in the
  // optimistic update below. Any FUTURE auto-titling (background summarizers,
  // unread-driven renames, …) must instead check the row first and skip when
  // `titleProvenance === "operator"` — an auto-rename must never clobber a
  // hand-set title. (Today's other rename callers — the model-switch fork and
  // thread creation — rename only their own freshly-minted session, so no
  // clobber path exists; this note is the guard for the next caller.)
  const renameSession = useCallback(async (id: string, title: string) => {
    let previousTitle = "";
    let previousProvenance: string | undefined;
    setSessions((previous) =>
      previous.map((session) => {
        if (session.id !== id) return session;
        previousTitle = session.title;
        previousProvenance = session.titleProvenance;
        return {
          ...session,
          title,
          titleProvenance: "operator",
          updatedAt: Date.now(),
        };
      }),
    );
    try {
      const echoed = await renameHarnessSession(id, title);
      setSessions((previous) =>
        previous.map((session) =>
          session.id === id ? { ...session, title: echoed } : session,
        ),
      );
      return undefined;
    } catch (caught) {
      setSessions((previous) =>
        previous.map((session) =>
          session.id === id
            ? {
                ...session,
                title: previousTitle,
                titleProvenance: previousProvenance,
              }
            : session,
        ),
      );
      setError(caught instanceof Error ? caught.message : String(caught));
      return undefined;
    }
  }, []);

  // The daemon has no pin/archive concept; these are client-side niceties
  // that live only for the current page.
  const pinSession = useCallback(async (id: string, pinned: boolean) => {
    setSessions((previous) =>
      previous.map((session) =>
        session.id === id ? { ...session, pinned } : session,
      ),
    );
    return undefined;
  }, []);

  const archiveSession = useCallback(async (id: string, archived: boolean) => {
    setSessions((previous) =>
      previous.map((session) =>
        session.id === id ? { ...session, archived } : session,
      ),
    );
    return undefined;
  }, []);

  return {
    sessions,
    isLoading: isLoading && !loadedOnce.current,
    error,
    createSession,
    deleteSession,
    renameSession,
    pinSession,
    archiveSession,
    refreshSessions,
  };
}
