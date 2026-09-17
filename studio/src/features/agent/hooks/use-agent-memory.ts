"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchHarnessUserModel,
  fetchHarnessUserModelEntry,
  type HarnessUserModelDetail,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";
import type { MemoryEntry } from "../types";

/** The store footprint the index read reports: N facts · M bytes · sha256. */
export interface MemoryStoreFootprint {
  /** Number of facts in the index. */
  count: number;
  /** Aggregate byte length of the rendered entries (key + description). */
  sizeBytes: number;
  /** Lowercase-hex SHA-256 over the rendered entries; "" when not reported. */
  sha256: string;
}

const EMPTY_FOOTPRINT: MemoryStoreFootprint = {
  count: 0,
  sizeBytes: 0,
  sha256: "",
};

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
 *
 * `isLoading` stays true while the runtime is still CONNECTING: the index
 * read cannot start before the probe settles, and an empty `entries` in that
 * window is "not read yet", never "the store is empty" — the detail page
 * declared a fact missing (and 404ed) on exactly that first render. Only an
 * OFFLINE runtime reports not-loading, because nothing is in flight.
 */
export function useAgentMemory() {
  const { connected, state } = useRuntimeStatus();
  const [entries, setEntries] = useState<MemoryEntry[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [disabledReason, setDisabledReason] = useState<string | null>(null);
  const [store, setStore] = useState<MemoryStoreFootprint>(EMPTY_FOOTPRINT);

  const load = useCallback(async (signal?: AbortSignal) => {
    setIsLoading(true);
    try {
      const model = await fetchHarnessUserModel(signal);
      if (signal?.aborted) return;
      setDisabledReason(null);
      setStore({
        count: model.entries.length,
        sizeBytes: model.sizeBytes,
        sha256: model.sha256,
      });
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
    isLoading: isLoading && state !== "offline",
    refresh,
    isSupported: disabledReason === null,
    /** Why the user model is unavailable (e.g. --no-user-model), when it is. */
    disabledReason,
    /** The index read's footprint: fact count, byte size, content digest. */
    store,
    harnessLive: connected,
    canWrite: false,
  };
}

/** What `useMemoryEntryDetail` knows about one fact right now. */
export interface MemoryEntryDetailState {
  /** The fact's value and revisions once read; null while loading, on an
   *  error, or when the key no longer matches an entry (`stale`). */
  detail: HarnessUserModelDetail | null;
  isLoading: boolean;
  /** The daemon's refusal (e.g. --no-user-model), when the read failed. */
  error: string | null;
  /** True once the daemon answered WITHOUT a detail: the index row that led
   *  here no longer matches an entry (the agent forgot or renamed it). */
  stale: boolean;
}

/**
 * Lazily reads ONE fact's value and bounded revision history — the TUI's
 * `enter` step on the /usermodel viewer — for the detail page. Reads on
 * mount and whenever `key` changes; the read is aborted on unmount or key
 * change so a slow answer for an earlier key never lands on a later one.
 *
 * Read-only like the index (memory rule 8): there is nothing to write back.
 */
export function useMemoryEntryDetail(key: string): MemoryEntryDetailState {
  const { connected } = useRuntimeStatus();
  const [state, setState] = useState<MemoryEntryDetailState>({
    detail: null,
    isLoading: true,
    error: null,
    stale: false,
  });

  useEffect(() => {
    if (!connected || key === "") return;
    const controller = new AbortController();
    const { signal } = controller;
    setState({ detail: null, isLoading: true, error: null, stale: false });
    fetchHarnessUserModelEntry(key, signal).then(
      (detail) => {
        if (signal.aborted) return;
        setState({
          detail,
          isLoading: false,
          error: null,
          stale: detail === null,
        });
      },
      (caught: unknown) => {
        if (signal.aborted) return;
        setState({
          detail: null,
          isLoading: false,
          error: caught instanceof Error ? caught.message : String(caught),
          stale: false,
        });
      },
    );
    return () => controller.abort();
  }, [connected, key]);

  return {
    ...state,
    // Off-line there is nothing in flight; the page shows its offline state.
    isLoading: state.isLoading && connected && key !== "",
  };
}
