"use client";

import { useCallback, useEffect, useState } from "react";
import {
  approveHarnessSoulBaseline,
  fetchHarnessRuntimeSettings,
  type HarnessRuntimeSettings,
  type HarnessRuntimeSettingsDoc,
  saveHarnessRuntimeSettings,
} from "@/lib/harness/runtime-settings";
import { useRuntimeStatus } from "../runtime-status";

/** A partial document: any section, any field, merged over the saved one. */
export type RuntimeSettingsPatch = {
  [Section in keyof HarnessRuntimeSettings]?: Partial<
    HarnessRuntimeSettings[Section]
  >;
};

export type RuntimeSettingsBusy = "" | "save" | "approve-soul";

/** Deep-merges a patch over a document, section by section. */
export function mergeRuntimeSettings(
  base: HarnessRuntimeSettings,
  patch: RuntimeSettingsPatch,
): HarnessRuntimeSettings {
  return {
    learning: { ...base.learning, ...patch.learning },
    steer: { ...base.steer, ...patch.steer },
    soul: { ...base.soul, ...patch.soul },
  };
}

/**
 * The controller-owned runtime settings — learning mode/sensitivity, the
 * steer opt-out, the soul flags — and the one-shot soul baseline approval.
 * Managed mode only: external mode owns its own flags (`manageable` is
 * false, nothing is fetched, and the controller would answer 409 anyway).
 * Every write here RESTARTS the daemon, killing in-flight runs, so callers
 * confirm first; `save` re-reads afterwards so the effective values (what
 * mecated actually runs with, inherited settings.yaml included) tell the
 * truth even when the controller adjusted nothing.
 *
 * The soul's PROVENANCE (user / project / driver) and body are the daemon's
 * own `GET /v1/soul` (`client.soul`), not this hook: offer `approveSoul`
 * only for a user/project soul (`--approve-soul` is a no-op for a driver
 * soul), and gate the whole persona card on `soulSupported`.
 */
export function useRuntimeSettings() {
  const {
    connected,
    mode,
    serverCapabilities,
    refresh: refreshRuntime,
  } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [doc, setDoc] = useState<HarnessRuntimeSettingsDoc | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [busy, setBusy] = useState<RuntimeSettingsBusy>("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!manageable) {
        setIsLoading(false);
        return null;
      }
      try {
        const next = await fetchHarnessRuntimeSettings(signal);
        if (signal?.aborted) return null;
        setDoc(next);
        setError(null);
        return next;
      } catch (caught) {
        if (signal?.aborted) return null;
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

  const refresh = useCallback(() => load(), [load]);

  /** Merges `patch` over the saved document and PUTs the whole thing.
   *  RESTARTS the daemon. A refusal (mecated would not start; learning is
   *  operator-managed; the soul path is outside the allowed directories)
   *  lands in `error` and leaves `doc` as it was. */
  const save = useCallback(
    async (patch: RuntimeSettingsPatch) => {
      if (!doc) return false;
      setBusy("save");
      setError(null);
      setNotice(null);
      try {
        await saveHarnessRuntimeSettings(
          mergeRuntimeSettings(doc.config, patch),
        );
        await load();
        // The restarted daemon may advertise different capabilities
        // (learning.mode flips learning_proposals / reflection): re-probe
        // now so the gates update without waiting for the poll. Best-effort
        // — the save itself has already succeeded.
        await refreshRuntime().catch(() => undefined);
        setNotice("Saved. The agent restarted.");
        return true;
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        return false;
      } finally {
        setBusy("");
      }
    },
    [doc, load, refreshRuntime],
  );

  /** Accepts the current soul as the drift baseline (one restart with
   *  `--approve-soul`). RESTARTS the daemon. */
  const approveSoul = useCallback(async () => {
    setBusy("approve-soul");
    setError(null);
    setNotice(null);
    try {
      await approveHarnessSoulBaseline();
      await load();
      setNotice("Saved. The agent restarted.");
      return true;
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
      return false;
    } finally {
      setBusy("");
    }
  }, [load]);

  return {
    live: connected,
    /** False in external mode: the deployment owns its own flags. */
    manageable,
    /** The daemon advertises a persona surface (`capabilities.soul`). */
    soulSupported: serverCapabilities.soul === true,
    doc,
    isLoading,
    busy,
    error,
    notice,
    refresh,
    save,
    approveSoul,
  };
}
