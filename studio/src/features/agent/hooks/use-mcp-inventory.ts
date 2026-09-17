"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  isNoMcpProvider,
  listHarnessMcpSources,
  listHarnessToolHiveGroups,
  type McpSourceView,
  NO_MCP_PROVIDER_TEXT,
} from "@/lib/harness/mcp";
import { isUnsupportedByDaemon } from "@/lib/harness/sdk";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The daemon's resolved MCP source inventory plus the ToolHive groups — the
 * data behind mecatui's `/mcp` panel (cmd/mecatui/ui/mcp.go `openMCP`).
 *
 * Sources and groups load in PARALLEL on connect; the groups read is
 * best-effort (`groupsError`) so a ToolHive-less daemon still lists its
 * static sources, exactly as the TUI degrades. The inventory is the daemon's
 * STARTUP snapshot: `refresh()` re-reads it, and `refreshed` flips true once
 * a manual re-read lands so the footer can read "updated" instead of the
 * startup-snapshot caveat (the TUI's `refreshing` → `refreshed` indicator).
 *
 * `enabled` is the caller's capability gate (`serverCapabilities.mcp`): a
 * daemon that only grants `mcp_connector_status` must never receive these
 * direct source/group reads (the TUI invariant), so the hook stays idle.
 */
export interface McpInventoryView {
  sources: McpSourceView[];
  groups: string[];
  /** The groups read failed — sources still render; groups say "unavailable". */
  groupsError: boolean;
  /** A groups result (success or failure) has landed at least once. */
  groupsLoaded: boolean;
  /** The FIRST load is in flight (nothing shown yet). */
  isLoading: boolean;
  /** A manual re-read is in flight (already-shown sources stay on screen). */
  refreshing: boolean;
  /** A manual re-read has landed: the list reflects live source status. */
  refreshed: boolean;
  /** The sources read failed, in the user's words; null when it succeeded. */
  error: string | null;
  refresh: () => Promise<void>;
}

export const MCP_INVENTORY_UNSUPPORTED_TEXT =
  "This agent can't list its MCP tools.";

function sourcesErrorText(error: unknown): string {
  if (isNoMcpProvider(error)) return NO_MCP_PROVIDER_TEXT;
  if (isUnsupportedByDaemon(error)) return MCP_INVENTORY_UNSUPPORTED_TEXT;
  return error instanceof Error ? error.message : String(error);
}

export function useMcpInventory(options?: {
  enabled?: boolean;
}): McpInventoryView {
  const { connected } = useRuntimeStatus();
  const enabled = options?.enabled ?? true;
  const active = connected && enabled;

  const [sources, setSources] = useState<McpSourceView[]>([]);
  const [groups, setGroups] = useState<string[]>([]);
  const [groupsError, setGroupsError] = useState(false);
  const [groupsLoaded, setGroupsLoaded] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshed, setRefreshed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Retires a stale load when the daemon reconnects or the hook unmounts.
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async (manual: boolean) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const { signal } = controller;
    if (manual) setRefreshing(true);

    const groupsRead = listHarnessToolHiveGroups(signal).then(
      (list) => ({ ok: true as const, list }),
      () => ({ ok: false as const, list: [] as string[] }),
    );
    let sourcesOk = false;
    try {
      const list = await listHarnessMcpSources(signal);
      if (signal.aborted) return;
      setSources(list);
      setError(null);
      sourcesOk = true;
    } catch (caught) {
      if (signal.aborted) return;
      setSources([]);
      setError(sourcesErrorText(caught));
    }
    const groupsResult = await groupsRead;
    if (signal.aborted) return;
    setGroups(groupsResult.list);
    setGroupsError(!groupsResult.ok);
    setGroupsLoaded(true);
    setIsLoading(false);
    if (manual) {
      setRefreshing(false);
      if (sourcesOk) setRefreshed(true);
    }
  }, []);

  useEffect(() => {
    if (!active) {
      abortRef.current?.abort();
      return;
    }
    void load(false);
    return () => abortRef.current?.abort();
  }, [active, load]);

  const refresh = useCallback(async () => {
    if (!active) return;
    await load(true);
  }, [active, load]);

  return useMemo(
    () => ({
      sources,
      groups,
      groupsError,
      groupsLoaded,
      isLoading,
      refreshing,
      refreshed,
      error,
      refresh,
    }),
    [
      sources,
      groups,
      groupsError,
      groupsLoaded,
      isLoading,
      refreshing,
      refreshed,
      error,
      refresh,
    ],
  );
}
