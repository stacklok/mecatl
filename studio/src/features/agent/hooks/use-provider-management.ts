"use client";

import { useCallback, useEffect, useState } from "react";
import {
  createHarnessCustomProvider,
  type HarnessCustomProviderDefinition,
  type HarnessProviderInfo,
  type HarnessProviderRemovalScope,
  type KnownHarnessProvider,
  listHarnessProviders,
  listKnownHarnessProviders,
  removeHarnessProvider,
  restartHarnessDaemon,
  setActiveHarnessProvider,
  startHarnessToolhiveGateway,
  testHarnessProviderKey,
} from "@/lib/harness/client";
import { providerLabel } from "@/lib/provider-label";
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

/** The outcome of a custom-definition write, for the Add dialog to render
 *  in place: `ok` when the controller wrote it, `restarted` when it also
 *  restarted the daemon (a keyless provider), `error` with the controller's
 *  reason on refusal — or, with `ok`, the cause of a failed restart. */
export type CustomProviderSaveResult = {
  ok: boolean;
  restarted: boolean;
  error?: string;
};

/**
 * The controller-owned provider management surface: the provider inventory
 * (the auth.yaml blocks PLUS the operator settings' custom `providers:`
 * definitions, ADR 0238 — names + key-present booleans, NEVER values),
 * guided add (snippet + re-check + restart), the custom DEFINITION write
 * (`providers add`: the non-secret id/flavor/URL/model/auth-method entry
 * lands in the daemon's user-global settings.yaml), server-side key tests
 * (built-in kinds and api_key custom gateways alike), and removal in the
 * TUI's two scopes — the key alone (`providers logout`) or the whole
 * provider (`providers remove`). Managed mode only — external mode owns
 * nothing locally (`manageable` is false and the controller would answer
 * 409 anyway), and every mutation here restarts the daemon, killing
 * in-flight runs, so callers confirm first.
 *
 * There is deliberately NO "add provider with key" action: credentials never
 * cross the browser/controller boundary (Studio rule 3). The definition
 * Studio writes has no secret field; the api_key still travels by hand into
 * auth.yaml on the daemon's machine (for built-ins, the whole block does).
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

  /**
   * Writes a custom provider DEFINITION (non-secret: id, flavor, base URL,
   * default model, auth method) into the daemon's user-global settings.yaml
   * through the controller, then re-reads the inventory. The daemon is
   * restarted by the controller only for a keyless provider; an api_key one
   * needs its key in auth.yaml first, then the Restart button. Busy is
   * `add:<id>`. The result is returned as well as noticed, so the dialog
   * can render it in place (the section's notice sits behind the modal).
   */
  const addCustomProvider = useCallback(
    async (
      definition: HarnessCustomProviderDefinition,
    ): Promise<CustomProviderSaveResult> => {
      setBusy(`add:${definition.id}`);
      setError(null);
      setNotice(null);
      try {
        const saved = await createHarnessCustomProvider(definition);
        await load();
        if (definition.authMethod === "api_key") {
          setNotice(
            `Saved. Add the key for ${providerLabel(definition.id)} to the agent's key file, then restart the agent.`,
          );
        } else if (saved.restarted) {
          setNotice("Saved. The agent restarted.");
        } else {
          setNotice("Saved.");
          if (saved.restartError)
            setError(`The agent could not restart: ${saved.restartError}`);
        }
        return {
          ok: true,
          restarted: saved.restarted,
          error: saved.restartError || undefined,
        };
      } catch (caught) {
        const message =
          caught instanceof Error ? caught.message : String(caught);
        setError(message);
        return { ok: false, restarted: false, error: message };
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  /** Removes a provider — its whole auth.yaml block plus a custom
   *  definition (`scope: "all"`, the default) or only its api_key line
   *  (`scope: "credential"`). RESTARTS the daemon. */
  const removeProvider = useCallback(
    async (name: string, scope: HarnessProviderRemovalScope = "all") => {
      setBusy(`remove:${name}`);
      setError(null);
      setNotice(null);
      try {
        await removeHarnessProvider(name, scope);
        setHealth((previous) => {
          const { [name]: _dropped, ...rest } = previous;
          return rest;
        });
        await load();
        setNotice(
          scope === "credential"
            ? "Key removed. The agent restarted."
            : `${providerLabel(name)} removed. The agent restarted.`,
        );
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
      setNotice("The agent restarted.");
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
            ? "Switched to offline mode. The agent restarted."
            : `Switched to ${providerLabel(kind)}. The agent restarted.`,
        );
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  /**
   * Starts the ToolHive LLM gateway proxy through the controller (`thv llm
   * proxy start`, detached) and re-reads the inventory so the ToolHive row
   * reflects the probe. Does NOT restart the daemon. A proxy that did not
   * answer within the controller's bounded poll is a NOTICE carrying the
   * controller's hint (typically: run `thv llm login` in a terminal first),
   * not an error — the spawn itself succeeded.
   */
  const startToolhive = useCallback(async () => {
    setBusy("toolhive:start");
    setError(null);
    setNotice(null);
    try {
      const result = await startHarnessToolhiveGateway();
      await load();
      setNotice(
        result.available
          ? "ToolHive gateway is reachable. Set it as active to use it."
          : result.hint ||
              "The ToolHive gateway did not answer yet — re-check in a moment.",
      );
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy("");
    }
  }, [load]);

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
    addCustomProvider,
    removeProvider,
    restartDaemon,
    setActiveProvider,
    startToolhive,
  };
}
