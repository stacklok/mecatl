"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchHarnessDaemonOptions,
  type HarnessDaemonOptions,
  type HarnessDaemonOptionsDoc,
  saveHarnessDaemonOptions,
} from "@/lib/harness/daemon-options";
import { useRuntimeStatus } from "../runtime-status";

/** A partial document: any section, any field, merged over the saved one. */
export type DaemonOptionsPatch = {
  [Section in keyof HarnessDaemonOptions]?: Partial<
    HarnessDaemonOptions[Section]
  >;
};

/** Deep-merges a patch over a document, section by section. */
export function mergeDaemonOptions(
  base: HarnessDaemonOptions,
  patch: DaemonOptionsPatch,
): HarnessDaemonOptions {
  return {
    projectMemory: { ...base.projectMemory, ...patch.projectMemory },
    userModel: { ...base.userModel, ...patch.userModel },
    skills: { ...base.skills, ...patch.skills },
    commands: { ...base.commands, ...patch.commands },
    mcp: { ...base.mcp, ...patch.mcp },
  };
}

/**
 * The controller-owned daemon options — the two memory stores, skill
 * discovery, slash commands and MCP discovery as mecated spawn flags.
 * Managed mode only: external mode owns its own flags (`manageable` is
 * false, nothing is fetched, and the controller would answer 409 anyway).
 * Every write here RESTARTS the daemon, killing in-flight runs, so callers
 * confirm first; `save` re-reads afterwards and re-probes the daemon's
 * capabilities, because the restarted daemon advertises the result
 * (`memory`, `user_model`, `skills`, `slash_commands` flip) and those
 * capability flags — not this document — are what the UI shows as "on".
 */
export function useDaemonOptions() {
  const { connected, mode, refresh: refreshRuntime } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [doc, setDoc] = useState<HarnessDaemonOptionsDoc | null>(null);
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
        const next = await fetchHarnessDaemonOptions(signal);
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
   *  RESTARTS the daemon. A refusal (a directory outside the allowed roots,
   *  mecated would not start and the controller rolled back) lands in
   *  `error` and leaves `doc` as the controller reports it. */
  const save = useCallback(
    async (patch: DaemonOptionsPatch) => {
      if (!doc) return false;
      setBusy(true);
      setError(null);
      setNotice(null);
      try {
        await saveHarnessDaemonOptions(mergeDaemonOptions(doc.options, patch));
        await load();
        // The restarted daemon advertises the new tool catalog and stores:
        // re-probe now so the capability-gated status lines and pages update
        // without waiting for the poll. Best-effort — the save stood.
        await refreshRuntime().catch(() => undefined);
        setNotice("Saved. The agent restarted.");
        return true;
      } catch (caught) {
        const message =
          caught instanceof Error ? caught.message : String(caught);
        // Re-read the document the daemon is actually running on (the
        // controller rolled back), then restate the refusal — `load` clears
        // the error on success and the user must still see why.
        await load();
        setError(message);
        return false;
      } finally {
        setBusy(false);
      }
    },
    [doc, load, refreshRuntime],
  );

  return {
    live: connected,
    /** False in external mode: the deployment owns its own flags. */
    manageable,
    doc,
    isLoading,
    busy,
    error,
    notice,
    refresh,
    save,
  };
}
