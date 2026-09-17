"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchHarnessDaemonDefaults,
  type HarnessDaemonDefaults,
  saveHarnessDaemonDefaults,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

/** What Save sends: the document minus the controller-owned active provider. */
export type DaemonDefaultsDraft = Omit<HarnessDaemonDefaults, "activeProvider">;

/**
 * The controller-owned DAEMON DEFAULTS surface: the mecated spawn flags an
 * operator would otherwise pass by hand (default/subagent model per
 * provider, reasoning effort, context window, prompt caching, provider base
 * URLs, the ToolHive LLM gateway, aliases/slots, the credentials-file
 * path). Managed mode only — external mode owns nothing locally
 * (`manageable` is false, the load never fires, and the controller would
 * answer 409 anyway) — and every save restarts the daemon, killing in-flight
 * runs, so callers confirm first. Nothing here is a credential.
 */
export function useDaemonDefaults() {
  const { connected, mode } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [defaults, setDefaults] = useState<HarnessDaemonDefaults | null>(null);
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
        const next = await fetchHarnessDaemonDefaults(signal);
        if (signal?.aborted) return null;
        setDefaults(next);
        return next;
      } catch (caught) {
        if (signal?.aborted) return null;
        setDefaults(null);
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
   * Replaces the whole document. RESTARTS the daemon. A document mecated
   * refuses (an unknown --default-model, a bad alias) is rolled back by the
   * controller and lands in `error` with mecated's own refusal; the re-read
   * afterwards shows whichever document the daemon is actually running on.
   * Resolves true when the save stood.
   */
  const save = useCallback(
    async (next: DaemonDefaultsDraft): Promise<boolean> => {
      setBusy(true);
      setError(null);
      setNotice(null);
      try {
        const saved = await saveHarnessDaemonDefaults(next);
        if (saved) setDefaults(saved);
        else await load();
        setNotice("Saved. The agent restarted.");
        return true;
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        await load();
        return false;
      } finally {
        setBusy(false);
      }
    },
    [load],
  );

  return {
    live: connected,
    /** False in external mode: the deployment owns its daemon's flags. */
    manageable,
    defaults,
    isLoading,
    busy,
    error,
    notice,
    reload,
    save,
  };
}
