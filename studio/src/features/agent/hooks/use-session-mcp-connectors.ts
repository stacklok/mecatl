"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  fetchSessionConnectors,
  type SessionConnectors,
} from "@/lib/harness/enrollment";
import { useRuntimeStatus } from "../runtime-status";

/**
 * One chat's broker connector inventory (`GET /v1/sessions/{id}/mcp/
 * connectors`) — the broker half of mecatui's `/mcp` panel
 * (`renderBrokerMCPPanel`): the enrollment state, each connector's catalogue
 * state and tool count, and the truncation flag.
 *
 * READ-ONLY. Connecting and cancelling the workspace-services enrollment
 * (and the 3 s observe poll of a pending one) belong to
 * `useWorkspaceEnrollment`, which the chat already runs; a panel passes that
 * hook's phase in as `revision` so the inventory re-reads whenever the
 * enrollment settles, instead of running a second poll loop of its own.
 *
 * `inventory === null` after a settled read means the daemon refused or
 * lacks inspection (`fetchSessionConnectors` folds 401/403/404/412 to null):
 * the UI says "Status unavailable", never guesses.
 */
export interface SessionMcpConnectorsView {
  inventory: SessionConnectors | null;
  /** The first read (or a read after a session change) is in flight. */
  isLoading: boolean;
  /** A manual re-read is in flight; the shown inventory stays on screen. */
  refreshing: boolean;
  /** A read failed for a reason other than "inspection unavailable". */
  error: string | null;
  refresh: () => Promise<void>;
}

export function useSessionMcpConnectors(
  sessionId: string | null,
  options?: {
    /** The caller's capability gate (`serverCapabilities.mcp_connector_status`). */
    enabled?: boolean;
    /** Any value whose change should trigger a re-read (the enrollment phase). */
    revision?: unknown;
  },
): SessionMcpConnectorsView {
  const { connected } = useRuntimeStatus();
  const enabled = options?.enabled ?? true;
  const revision = options?.revision;
  const active = connected && enabled && Boolean(sessionId);

  const [inventory, setInventory] = useState<SessionConnectors | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async (id: string, manual: boolean) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const { signal } = controller;
    if (manual) setRefreshing(true);
    try {
      const result = await fetchSessionConnectors(id, signal);
      if (signal.aborted) return;
      setInventory(result);
      setError(null);
    } catch (caught) {
      if (signal.aborted) return;
      setInventory(null);
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (!signal.aborted) {
        setIsLoading(false);
        if (manual) setRefreshing(false);
      }
    }
  }, []);

  // A session change resets the view to loading before the new read lands so
  // the previous chat's connectors never show under the new chat's heading.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the reset keys on the session id by design
  useEffect(() => {
    setInventory(null);
    setError(null);
    setIsLoading(true);
  }, [sessionId]);

  // `revision` is a deliberate extra trigger: the enrollment phase changing
  // (pending → connected / cancelled) is when the inventory changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision re-triggers the read by design
  useEffect(() => {
    if (!active || !sessionId) {
      abortRef.current?.abort();
      return;
    }
    void load(sessionId, false);
    return () => abortRef.current?.abort();
  }, [active, sessionId, revision, load]);

  const refresh = useCallback(async () => {
    if (!active || !sessionId) return;
    await load(sessionId, true);
  }, [active, sessionId, load]);

  return useMemo(
    () => ({ inventory, isLoading, refreshing, error, refresh }),
    [inventory, isLoading, refreshing, error, refresh],
  );
}
