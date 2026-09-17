"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchHarnessDiagnosticsOptions,
  type HarnessDiagnosticsPatch,
  type HarnessDiagnosticsState,
  saveHarnessDiagnosticsOptions,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The controller-owned DIAGNOSTICS OPTIONS surface — the observability
 * spawn flags of the managed mecated (log level, admin/metrics listener +
 * perf MCP + goroutine alarm, product-metrics opt-out) plus the
 * controller-side `quiet` switch — shared by every card on the Diagnostics
 * settings page. Managed mode only: external mode owns nothing locally
 * (`manageable` is false, the load never fires, and the controller would
 * answer 409 anyway). A save of anything but `quiet` restarts the daemon,
 * so callers confirm first; after a save the runtime status is re-probed,
 * because the restarted daemon may report different capabilities.
 */
export function useDiagnosticsOptions() {
  const { connected, mode, refresh } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [state, setState] = useState<HarnessDiagnosticsState | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!manageable) {
        setIsLoading(false);
        return null;
      }
      try {
        const next = await fetchHarnessDiagnosticsOptions(signal);
        if (signal?.aborted) return null;
        setState(next);
        return next;
      } catch (caught) {
        if (signal?.aborted) return null;
        setState(null);
        setError(caught instanceof Error ? caught.message : String(caught));
        return null;
      } finally {
        if (!signal?.aborted) setIsLoading(false);
      }
    },
    [manageable],
  );

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  const reload = useCallback(() => load(), [load]);

  /**
   * Applies a partial document. RESTARTS the daemon unless only `quiet`
   * changed; a document mecated refuses is rolled back by the controller
   * and lands in `error` with mecated's own refusal, and the re-read shows
   * whichever document the daemon is actually running on. Resolves true
   * when the save stood.
   */
  const save = useCallback(
    async (patch: HarnessDiagnosticsPatch): Promise<boolean> => {
      setBusy(true);
      setError(null);
      setNotice(null);
      try {
        await saveHarnessDiagnosticsOptions(patch);
        await load();
        // The restart may change what the daemon advertises.
        await refresh();
        setNotice(
          Object.keys(patch).every((key) => key === "quiet")
            ? "Saved."
            : "Saved. The agent restarted.",
        );
        return true;
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        await load();
        return false;
      } finally {
        setBusy(false);
      }
    },
    [load, refresh],
  );

  return {
    live: connected,
    /** False in external mode: the deployment owns its daemon's flags. */
    manageable,
    /** The saved document; null in external mode, before the load, or
     *  against a controller without the route. */
    options: state?.options ?? null,
    /** The effective product-metrics verdict (env opt-out wins). */
    productMetrics: state?.productMetrics ?? null,
    isLoading,
    busy,
    error,
    notice,
    reload,
    save,
  };
}
