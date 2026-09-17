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
import { applySessionTitle, type SessionTitleUpdate } from "../session-title";
import { onSessionsChanged } from "../sessions-changed";
import type { AgentSession, CreateSessionOpts } from "../types";

const POLL_INTERVAL_MS = 20_000;

function toAgentSession(summary: SessionSummary): AgentSession {
  return {
    id: summary.sessionId,
    // A run row keeps an empty title: its list row falls back to what the
    // run IS (`describeRelationship`), not to a chat's placeholder.
    title: summary.title || (summary.isChat ? "Untitled chat" : ""),
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
    canCopyId: summary.canCopyId,
    canFork: summary.canFork,
    renameReason: summary.renameReason,
    deleteReason: summary.deleteReason,
    copyIdReason: summary.copyIdReason,
    forkReason: summary.forkReason,
    titleProvenance: summary.titleProvenance,
    titleRevision: summary.titleRevision,
    debugTargetSessionId: summary.debugTargetSessionId,
    kind: summary.kind,
    activityState: summary.activityState,
    isChat: summary.isChat,
    placementLabel: summary.placement?.label ?? "",
    placementBranch: summary.placement?.branch ?? "",
    relationship: summary.relationship,
    canInspect: summary.canInspect,
    canViewTranscript: summary.canViewTranscript,
    publicChatReason: summary.publicChatReason,
    viewTranscriptReason: summary.viewTranscriptReason,
  };
}

/** Splits one inventory page into chat rows and inspect-only run rows. */
function splitPage(page: readonly SessionSummary[]): {
  chats: AgentSession[];
  runs: AgentSession[];
} {
  const chats: AgentSession[] = [];
  const runs: AgentSession[] = [];
  for (const summary of page) {
    (summary.isChat ? chats : runs).push(toAgentSession(summary));
  }
  return { chats, runs };
}

/**
 * What the sidebar knows about the inventory walk: the progress of the
 * VISIBLE walk (the first load, a reconnect, or an explicit retry — the
 * background poll refreshes silently and never sets `inFlight`) plus what the
 * most recent finished walk proved about the inventory's extent.
 */
export interface SessionInventoryWalk {
  /** A visible walk is running. Background polls never set this. */
  inFlight: boolean;
  /** Pages landed so far in the walk this state describes. */
  pages: number;
  /** Chat rows seen so far in the walk this state describes. */
  rows: number;
  /**
   * Whether the most recent finished walk covered the whole inventory: null
   * until one finishes; false after a page-bounded or cancelled walk.
   */
  complete: boolean | null;
  /** The last visible walk ended because the user stopped it. */
  cancelled: boolean;
}

const IDLE_WALK: SessionInventoryWalk = {
  inFlight: false,
  pages: 0,
  rows: 0,
  complete: null,
  cancelled: false,
};

/** Partial-walk merge: update the rows seen, keep the rows not seen. */
function mergeRows(
  previous: AgentSession[],
  chats: AgentSession[],
): AgentSession[] {
  const seen = new Map(chats.map((chat) => [chat.id, chat]));
  const merged = previous.map((chat) => seen.get(chat.id) ?? chat);
  const known = new Set(previous.map((chat) => chat.id));
  return [...merged, ...chats.filter((chat) => !known.has(chat.id))];
}

/**
 * The chat list, backed by the daemon's session store — the record of chats.
 *
 * Invariants (from the server-backed-chats design):
 * - `sessions` holds only chats: rows whose one not-a-chat reason is
 *   `inspect_only_kind` (subagents, parallel branches, team members,
 *   scheduled fires) land in `runs` instead — the read-only inventory the
 *   sidebar's Runs / Scheduled / Other tabs list. Nothing the store returned
 *   is dropped.
 * - A row is removed only when a COMPLETE inventory walk proves it gone; a
 *   partial walk merges and never deletes. Both lists obey this.
 * - Action eligibility (rename/delete) comes from the row's capabilities,
 *   never re-derived client-side.
 * - A rename is optimistic but adopts the daemon's clamped title echo, and
 *   rolls back when the daemon refuses.
 */
export function useAgentSessions() {
  const { connected } = useRuntimeStatus();
  const [sessions, setSessions] = useState<AgentSession[]>([]);
  const [runs, setRuns] = useState<AgentSession[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [walk, setWalk] = useState<SessionInventoryWalk>(IDLE_WALK);
  const loadedOnce = useRef(false);
  // The mount-scoped controller (aborted on unmount/disconnect) so a retry or
  // refresh started from an event handler still dies with the hook.
  const mountRef = useRef<AbortController | null>(null);
  // The visible walk's own controller: Cancel aborts THIS walk only, never
  // the mount-scoped signal the 20-second poll keeps using.
  const visibleWalkRef = useRef<AbortController | null>(null);

  const load = useCallback(
    async (parentSignal?: AbortSignal, visible = false) => {
      if (parentSignal?.aborted) return;
      const controller = new AbortController();
      const onParentAbort = () => controller.abort();
      parentSignal?.addEventListener("abort", onParentAbort);
      if (visible) {
        visibleWalkRef.current?.abort();
        visibleWalkRef.current = controller;
        setWalk((previous) => ({
          ...previous,
          inFlight: true,
          pages: 0,
          rows: 0,
          cancelled: false,
        }));
      }
      let pagesSeen = 0;
      let chatRows = 0;
      try {
        const result = await fetchAllSessions(
          controller.signal,
          25,
          (progress) => {
            if (controller.signal.aborted) return;
            const { chats, runs: pageRuns } = splitPage(progress.page);
            pagesSeen = progress.pages;
            chatRows += chats.length;
            // Each page lands as it arrives (a partial merge never deletes),
            // so rows show from the first page on, not only after the walk.
            if (chats.length > 0) {
              setSessions((previous) => mergeRows(previous, chats));
            }
            if (pageRuns.length > 0) {
              setRuns((previous) => mergeRows(previous, pageRuns));
            }
            if (visible) {
              setWalk((previous) => ({
                ...previous,
                pages: pagesSeen,
                rows: chatRows,
              }));
            }
            loadedOnce.current = true;
            setIsLoading(false);
          },
        );
        if (parentSignal?.aborted) return;
        const { chats, runs: allRuns } = splitPage(result.sessions);
        // Only a COMPLETE walk may drop a row; a bounded walk merges.
        setSessions((previous) =>
          result.complete ? chats : mergeRows(previous, chats),
        );
        setRuns((previous) =>
          result.complete ? allRuns : mergeRows(previous, allRuns),
        );
        setError(null);
        loadedOnce.current = true;
        setWalk((previous) => ({
          inFlight: visible ? false : previous.inFlight,
          pages: pagesSeen,
          rows: chats.length,
          complete: result.complete,
          cancelled: false,
        }));
      } catch (caught) {
        if (parentSignal?.aborted) {
          // Unmounted or disconnected: nothing to report, but a visible walk
          // must not read as still loading once the hook comes back.
          if (visible) {
            setWalk((previous) => ({ ...previous, inFlight: false }));
          }
          return;
        }
        if (controller.signal.aborted) {
          // The user stopped this walk: the pages merged so far stand, and
          // the inventory's extent is unproven.
          setWalk((previous) => ({
            ...previous,
            inFlight: false,
            pages: pagesSeen,
            rows: chatRows,
            complete: false,
            cancelled: true,
          }));
          return;
        }
        setError(caught instanceof Error ? caught.message : String(caught));
        if (visible) {
          setWalk((previous) => ({ ...previous, inFlight: false }));
        }
      } finally {
        parentSignal?.removeEventListener("abort", onParentAbort);
        if (visibleWalkRef.current === controller) {
          visibleWalkRef.current = null;
        }
        if (!parentSignal?.aborted) setIsLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    mountRef.current = controller;
    // The first walk after mount/reconnect is the visible one; the poll and
    // the sessions-changed re-walk refresh silently.
    void load(controller.signal, true);
    const timer = setInterval(() => {
      void load(controller.signal);
    }, POLL_INTERVAL_MS);
    // A bulk clean-up on the Storage page deletes rows behind this hook's
    // back; its signal re-walks the inventory now instead of on the next poll.
    const unsubscribe = onSessionsChanged(() => {
      void load(controller.signal);
    });
    return () => {
      controller.abort();
      if (mountRef.current === controller) mountRef.current = null;
      clearInterval(timer);
      unsubscribe();
    };
  }, [connected, load]);

  const refreshSessions = useCallback(async () => {
    await load(mountRef.current?.signal);
  }, [load]);

  /** Stops the visible inventory walk; the rows merged so far stay. */
  const cancelLoad = useCallback(() => {
    visibleWalkRef.current?.abort();
  }, []);

  /** Re-walks the inventory visibly (after an error, a cancel, or a bounded walk). */
  const retry = useCallback(async () => {
    setError(null);
    await load(mountRef.current?.signal, true);
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
    setRuns((previous) => previous.filter((s) => s.id !== id));
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

  // A live `session.title` event (the daemon's first-prompt seed, an
  // auto-title, a rename from another client — heard on a watch or the
  // prompt stream). The daemon is authoritative, so the row adopts its word
  // at once instead of waiting for the next poll; the title lifecycle
  // revision keeps a replayed older title from regressing the row. This is
  // NOT the auto-rename path the F4 note above guards against: nothing here
  // originates a title, it mirrors one the daemon already applied.
  const applyTitle = useCallback((id: string, update: SessionTitleUpdate) => {
    setSessions((previous) => applySessionTitle(previous, id, update));
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
    /** Every inspect-only row (child runs, scheduled fires, unknown kinds). */
    runs,
    isLoading: isLoading && !loadedOnce.current,
    error,
    walk,
    cancelLoad,
    retry,
    createSession,
    deleteSession,
    renameSession,
    /** Adopts a live `session.title` event onto its row (revision-guarded). */
    applyTitle,
    pinSession,
    archiveSession,
    refreshSessions,
  };
}
