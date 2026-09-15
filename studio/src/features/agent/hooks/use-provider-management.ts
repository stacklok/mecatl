"use client";

import { useCallback, useEffect, useState } from "react";
import {
  type HarnessProviderInfo,
  type KnownHarnessProvider,
  listHarnessProviders,
  listKnownHarnessProviders,
  removeHarnessProvider,
  restartHarnessDaemon,
  setActiveHarnessProvider,
  testHarnessProviderKey,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

/** The per-provider key-health dot, session-local: tests are on demand and
 *  their verdicts are not persisted anywhere. */
export type ProviderKeyHealth =
  | { state: "untested" }
  | { state: "ok" }
  /** The provider answered 401/403 — the KEY is bad. */
  | { state: "rejected"; detail: string }
  /** The probe could not complete (timeout, outage, non-auth error). */
  | { state: "error"; detail: string };

/**
 * The controller-owned provider management surface: the provider inventory
 * (the auth.yaml blocks PLUS the operator settings' custom `providers:`
 * definitions, ADR 0238 — names + key-present booleans, NEVER values),
 * guided add (snippet + re-check + restart), server-side key tests (built-in
 * kinds and api_key custom gateways alike), and auth.yaml block removal.
 * Managed mode only — external mode owns nothing locally (`manageable` is
 * false and the controller would answer 409 anyway), and every mutation here
 * restarts the daemon, killing in-flight runs, so callers confirm first.
 *
 * There is deliberately NO "add provider with key" action: credentials never
 * cross the browser/controller boundary (Studio rule 3). Adding a provider
 * is a guided copy — auth.yaml for built-ins; the settings `providers:`
 * block (and, for api_key auth, the auth.yaml key block) for custom
 * gateways — on the daemon's machine.
 */
export function useProviderManagement() {
  const { connected, mode } = useRuntimeStatus();
  const manageable = mode === "managed";
  const [providers, setProviders] = useState<HarnessProviderInfo[]>([]);
  const [known, setKnown] = useState<KnownHarnessProvider[]>([]);
  const [health, setHealth] = useState<Record<string, ProviderKeyHealth>>({});
  const [isLoading, setIsLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!manageable) {
        setIsLoading(false);
        return [] as HarnessProviderInfo[];
      }
      try {
        const [inventory, kinds] = await Promise.all([
          listHarnessProviders(signal),
          // The known-kind registry is static decoration; a failure there
          // must not break the inventory (Add just lists nothing).
          listKnownHarnessProviders(signal).catch(
            () => [] as KnownHarnessProvider[],
          ),
        ]);
        if (signal?.aborted) return [];
        setProviders(inventory);
        setKnown(kinds);
        setError(null);
        return inventory;
      } catch (caught) {
        if (signal?.aborted) return [];
        setProviders([]);
        setError(caught instanceof Error ? caught.message : String(caught));
        return [];
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

  /** Re-reads the inventory (the Add dialog's "Re-check"); returns the fresh
   *  rows so the caller can see whether a just-added provider appeared. */
  const reload = useCallback(() => load(), [load]);

  /** Tests a provider's stored key server-side; the verdict lands in
   *  `health[name]`. The browser only ever sees ok/rejected/error. */
  const testKey = useCallback(async (name: string) => {
    setBusy(`test:${name}`);
    setError(null);
    setNotice(null);
    try {
      const verdict = await testHarnessProviderKey(name);
      setHealth((previous) => ({
        ...previous,
        [name]: verdict.ok
          ? { state: "ok" }
          : verdict.rejected
            ? { state: "rejected", detail: verdict.error }
            : { state: "error", detail: verdict.error },
      }));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy("");
    }
  }, []);

  /** Removes a provider's auth.yaml block. RESTARTS the daemon. */
  const removeProvider = useCallback(
    async (name: string) => {
      setBusy(`remove:${name}`);
      setError(null);
      setNotice(null);
      try {
        await removeHarnessProvider(name);
        setHealth((previous) => {
          const { [name]: _dropped, ...rest } = previous;
          return rest;
        });
        await load();
        setNotice(`Provider ${name} removed. The daemon restarted without it.`);
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        // The removal may have stood even when the restart failed — re-read
        // so the list tells the truth either way.
        await load();
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  /** Restarts the daemon so a just-added auth.yaml block takes effect. */
  const restartDaemon = useCallback(async () => {
    setBusy("restart");
    setError(null);
    setNotice(null);
    try {
      await restartHarnessDaemon();
      await load();
      setNotice("Daemon restarted with the current auth.yaml.");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy("");
    }
  }, [load]);

  /** Switches the daemon's active provider ("mock" or a configured name)
   *  and restarts it. The live equivalent of setting MECATL_STUDIO_PROVIDER
   *  and restarting `npm run dev`. */
  const setActiveProvider = useCallback(
    async (kind: string) => {
      setBusy(`activate:${kind}`);
      setError(null);
      setNotice(null);
      try {
        await setActiveHarnessProvider(kind);
        await load();
        setNotice(
          kind === "mock"
            ? "Switched to the offline mock. The daemon restarted."
            : `Switched to ${kind}. The daemon restarted.`,
        );
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  return {
    live: connected,
    /** False in external mode: the deployment owns its providers. */
    manageable,
    providers,
    known,
    health,
    isLoading,
    busy,
    error,
    notice,
    reload,
    testKey,
    removeProvider,
    restartDaemon,
    setActiveProvider,
  };
}
