"use client";

import { useCallback, useEffect, useState } from "react";
import { fetchHarnessUserModel } from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";
import type { MemoryEntry } from "../types";

/**
 * Agent memory for the current operator: the daemon's user model — durable
 * facts the agent stored, shared across every project.
 *
 * `canWrite` is always false: the daemon exposes no write endpoint for the
 * user model, because the agent curates it through injection-scanned tool
 * calls. A value typed by hand would land in the model's turn-0 context
 * without passing that check, so the UI must not offer editing.
 *
 * A daemon running with `--no-user-model` answers the index read with an
 * error; that is a distinct DISABLED state, rendered as such — never as
 * placeholder content.
 */
export function useAgentMemory() {
  const { connected } = useRuntimeStatus();
  const [entries, setEntries] = useState<MemoryEntry[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [disabledReason, setDisabledReason] = useState<string | null>(null);
  const [store, setStore] = useState({ sizeBytes: 0, sha256: "" });

  const load = useCallback(async (signal?: AbortSignal) => {
    setIsLoading(true);
    try {
      const model = await fetchHarnessUserModel(signal);
      if (signal?.aborted) return;
      setDisabledReason(null);
      setStore({ sizeBytes: model.sizeBytes, sha256: model.sha256 });
      setEntries(
        model.entries.map((fact) => ({
          id: fact.key,
          title: fact.key,
          content: fact.description,
          section: "user model",
          updatedAt: 0,
        })),
      );
    } catch (caught) {
      if (signal?.aborted) return;
      setEntries([]);
      setDisabledReason(
        caught instanceof Error ? caught.message : String(caught),
      );
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  /** Re-reads the store; the agent may have written facts since the last read. */
  const refresh = useCallback(async () => {
    await load();
  }, [load]);

  return {
    entries,
    isLoading: isLoading && connected,
    refresh,
    isSupported: disabledReason === null,
    /** Why the user model is unavailable (e.g. --no-user-model), when it is. */
    disabledReason,
    store,
    harnessLive: connected,
    canWrite: false,
  };
}
