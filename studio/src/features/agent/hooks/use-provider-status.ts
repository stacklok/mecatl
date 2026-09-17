"use client";

import { useCallback, useEffect, useState } from "react";
import {
  type HarnessProviderStatus,
  listHarnessModelInventory,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The DAEMON's per-provider status hints (`ListModelsResponse.provider_status`,
 * issue #262): for operator-actionable live-inventory providers — the
 * ToolHive gateway, openai-codex — the daemon reports its last listing
 * outcome ("ok" | "unreachable" | "unauthorized" | "empty"), a short
 * remediation hint, whether it auto-selected the default model, the model
 * count, and whether a reachable gateway is out-ranked by a keyed default.
 *
 * Read-only and daemon-owned, so it renders in BOTH modes: managed mode
 * merges each row into the controller's provider inventory; external mode
 * lists the rows on their own under the managed note. An older daemon
 * without the field yields an empty list (no gate needed — the field is
 * additive on the models list Studio already reads). A failed read is
 * folded into `error`, never into fabricated rows.
 */
export function useProviderStatus() {
  const { connected } = useRuntimeStatus();
  const [rows, setRows] = useState<HarnessProviderStatus[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setIsLoading(true);
    try {
      const inventory = await listHarnessModelInventory(signal);
      if (signal?.aborted) return;
      setRows(inventory.providerStatus);
      setError(null);
    } catch (caught) {
      if (signal?.aborted) return;
      setRows([]);
      setError(caught instanceof Error ? caught.message : String(caught));
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

  const refresh = useCallback(() => load(), [load]);

  /** The daemon's status row for one provider id, if it surfaced one. */
  const forProvider = useCallback(
    (providerId: string) =>
      rows.find((row) => row.providerId === providerId) ?? null,
    [rows],
  );

  return {
    live: connected,
    rows,
    isLoading,
    error,
    refresh,
    forProvider,
  };
}
